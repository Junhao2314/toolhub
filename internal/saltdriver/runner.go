package saltdriver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

const (
	DefaultStateRoot  = "/srv/salt/states"
	remoteStagingRoot = "/var/cache/salt/minion/toolhub-staging"
	maxSaltOutput     = 8 << 20
)

var (
	minionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$`)
	jidPattern    = regexp.MustCompile(`^[0-9]{8,32}$`)
)

type CommandRunner interface {
	CombinedOutput(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	limit := &outputLimit{remaining: maxSaltOutput}
	command.Stdout = limitedWriter{limit: limit, output: &stdout}
	command.Stderr = limitedWriter{limit: limit}
	err := command.Run()
	if limit.wasExceeded() {
		return nil, errors.New("Salt output exceeds safety limit")
	}
	return stdout.Bytes(), err
}

var errOutputLimit = errors.New("output limit exceeded")

type outputLimit struct {
	mu        sync.Mutex
	remaining int
	exceeded  bool
}

type limitedWriter struct {
	limit  *outputLimit
	output *bytes.Buffer
}

func (w limitedWriter) Write(value []byte) (int, error) {
	w.limit.mu.Lock()
	defer w.limit.mu.Unlock()
	allowed := min(len(value), w.limit.remaining)
	if allowed > 0 && w.output != nil {
		_, _ = w.output.Write(value[:allowed])
	}
	w.limit.remaining -= allowed
	if allowed < len(value) {
		w.limit.exceeded = true
		return len(value), errOutputLimit
	}
	return len(value), nil
}

func (l *outputLimit) wasExceeded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.exceeded
}

type Driver struct {
	Runner      CommandRunner
	Salt        string
	SaltRun     string
	SaltKey     string
	SaltCP      string
	StateRoot   string
	StagingRoot string
	PollEvery   time.Duration
	PollTimeout time.Duration
	now         func() time.Time
}

func New(runner CommandRunner) *Driver {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Driver{
		Runner: runner, Salt: "salt", SaltRun: "salt-run", SaltKey: "salt-key", SaltCP: "salt-cp",
		StateRoot: DefaultStateRoot, StagingRoot: "/var/lib/toolhub-bridge/staging",
		PollEvery: time.Second, PollTimeout: 10 * time.Minute, now: time.Now,
	}
}

type AcceptedNode struct {
	MinionID string
	Version  string
	Online   bool
}

func (d *Driver) Health(ctx context.Context) error {
	for _, binary := range []string{d.Salt, d.SaltRun, d.SaltKey, d.SaltCP} {
		if _, err := exec.LookPath(binary); err != nil {
			return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "Required Salt CLI is not installed"}
		}
	}
	return nil
}

func (d *Driver) AcceptedNodes(ctx context.Context) ([]AcceptedNode, error) {
	output, err := d.Runner.CombinedOutput(ctx, d.SaltKey, "--out=json", "--list=acc")
	if err != nil {
		return nil, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "Could not list accepted Salt keys", Retryable: true}
	}
	ids, err := parseAcceptedKeys(output)
	if err != nil {
		return nil, err
	}
	result := make([]AcceptedNode, 0, len(ids))
	for _, id := range ids {
		version, online := d.version(ctx, id)
		result = append(result, AcceptedNode{MinionID: id, Version: version, Online: online})
	}
	return result, nil
}

func parseAcceptedKeys(body []byte) ([]string, error) {
	objects, err := DecodeStreamingJSON(body)
	if err != nil {
		return nil, fmt.Errorf("parse accepted Salt keys: %w", err)
	}
	seen := map[string]bool{}
	for _, object := range objects {
		for key, raw := range object {
			if key == "minions" || key == "accepted" {
				var ids []string
				if json.Unmarshal(raw, &ids) == nil {
					for _, id := range ids {
						if minionPattern.MatchString(id) {
							seen[id] = true
						}
					}
				}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func (d *Driver) version(ctx context.Context, minionID string) (string, bool) {
	if !minionPattern.MatchString(minionID) {
		return "", false
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := d.Runner.CombinedOutput(callCtx, d.Salt, "--out=json", "--static", "--timeout=10", "--", minionID, "test.version")
	if err != nil {
		return "", false
	}
	value, found, err := MinionResult(output, minionID)
	if err != nil || !found {
		return "", false
	}
	var version string
	if json.Unmarshal(value, &version) != nil || !strings.HasPrefix(version, "3008.") {
		return version, false
	}
	return version, true
}

func (d *Driver) EnsureCapabilities(ctx context.Context, minionID string) error {
	version, online := d.version(ctx, minionID)
	if !online {
		if version != "" && !strings.HasPrefix(version, "3008.") {
			return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrSaltVersion, Message: "Salt minion must run version 3008.x"}
		}
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "Salt minion is unavailable", Retryable: true}
	}
	for _, function := range []string{"saltutil.sync_modules", "saltutil.sync_states"} {
		output, err := d.Runner.CombinedOutput(ctx, d.Salt, "--out=json", "--static", "--timeout=30", "--", minionID, function)
		if err != nil {
			return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "Could not synchronize ToolHub Salt extensions", Retryable: true}
		}
		if _, found, parseErr := MinionResult(output, minionID); parseErr != nil || !found {
			return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "Salt extension synchronization returned no minion result", Retryable: true}
		}
	}
	return nil
}

func (d *Driver) ResolveManagedHome(ctx context.Context, minionID, username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", &bridgeprotocol.APIError{Code: bridgeprotocol.ErrManagedUserMissing, Message: "Managed username is required"}
	}
	raw, err := d.Call(ctx, minionID, "user.info", username)
	if err != nil {
		return "", err
	}
	var info struct {
		Name string `json:"name"`
		Home string `json:"home"`
	}
	if json.Unmarshal(raw, &info) != nil || info.Name != username {
		return "", &bridgeprotocol.APIError{Code: bridgeprotocol.ErrManagedUserMissing, Message: "Managed user does not exist on the Salt minion"}
	}
	info.Home = strings.TrimSpace(info.Home)
	if !path.IsAbs(info.Home) || info.Home == "/" || path.Clean(info.Home) != info.Home || strings.ContainsRune(info.Home, '\x00') {
		return "", &bridgeprotocol.APIError{Code: bridgeprotocol.ErrManagedUserMissing, Message: "Managed user home is not a canonical safe path"}
	}
	return info.Home, nil
}

var allowedFunctions = map[string]bool{
	"toolhub.scan": true, "toolhub.preflight": true, "toolhub.apply": true,
	"toolhub.reconcile": true, "toolhub.restore": true, "toolhub.remove_backup": true, "toolhub.cleanup_bundle": true,
	"user.info": true,
}

func (d *Driver) Call(ctx context.Context, minionID, function string, args ...string) (json.RawMessage, error) {
	if !minionPattern.MatchString(minionID) || !allowedFunctions[function] {
		return nil, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrUnsupportedOperation, Message: "Salt target or function is not allowed"}
	}
	argv := []string{"--out=json", "--static", "--timeout=60", "--", minionID, function}
	argv = append(argv, args...)
	output, err := d.Runner.CombinedOutput(ctx, d.Salt, argv...)
	if err != nil {
		return nil, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "Salt call failed", Retryable: true}
	}
	value, found, err := MinionResult(output, minionID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "Salt minion returned no result", Retryable: true}
	}
	return value, nil
}

func (d *Driver) Dispatch(ctx context.Context, minionID, function string, args ...string) (string, error) {
	if !minionPattern.MatchString(minionID) || !allowedFunctions[function] || function == "user.info" {
		return "", &bridgeprotocol.APIError{Code: bridgeprotocol.ErrUnsupportedOperation, Message: "Salt async target or function is not allowed"}
	}
	argv := []string{"--out=json", "--async", "--", minionID, function}
	argv = append(argv, args...)
	output, err := d.Runner.CombinedOutput(ctx, d.Salt, argv...)
	if err != nil {
		return "", &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "Salt async dispatch failed", Retryable: true}
	}
	jid, err := ParseAsyncJID(output, minionID)
	if err != nil {
		return "", err
	}
	return jid, nil
}

func (d *Driver) Poll(ctx context.Context, jid, minionID string) (json.RawMessage, error) {
	if !jidPattern.MatchString(jid) || !minionPattern.MatchString(minionID) {
		return nil, errors.New("invalid Salt JID or minion id")
	}
	deadline := d.now().Add(d.PollTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		output, err := d.Runner.CombinedOutput(ctx, d.SaltRun, "--out=json", "jobs.lookup_jid", jid)
		if err == nil {
			if result, found, parseErr := MinionResult(output, minionID); parseErr == nil && found && string(result) != "null" {
				return result, nil
			}
		}
		if !d.now().Before(deadline) {
			listOutput, listErr := d.Runner.CombinedOutput(ctx, d.SaltRun, "--out=json", "jobs.list_job", jid)
			if listErr != nil || !bytes.Contains(listOutput, []byte(jid)) {
				return nil, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrSaltJobMissing, Message: "Salt job cache no longer contains the target JID", Retryable: true}
			}
			return nil, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "Salt target operation timed out", Retryable: true}
		}
		timer := time.NewTimer(d.PollEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type StagedBundle struct {
	LocalPath  string
	RemotePath string
}

func (d *Driver) Stage(ctx context.Context, minionID string, payload any) (StagedBundle, error) {
	if !minionPattern.MatchString(minionID) {
		return StagedBundle{}, errors.New("invalid Salt minion id")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return StagedBundle{}, err
	}
	if len(body) > bridgeprotocol.MaxRequestBytes {
		return StagedBundle{}, errors.New("Salt staging bundle exceeds safety limit")
	}
	if err := os.MkdirAll(d.StagingRoot, 0700); err != nil {
		return StagedBundle{}, err
	}
	token := uuid.NewString()
	local := filepath.Join(d.StagingRoot, token+".json")
	remote := filepath.Join(remoteStagingRoot, token+".json")
	if err := ensureStagingPath(d.StagingRoot, local); err != nil {
		return StagedBundle{}, err
	}
	if err := os.WriteFile(local, body, 0600); err != nil {
		return StagedBundle{}, err
	}
	output, err := d.Runner.CombinedOutput(ctx, d.SaltCP, "--chunked", "--out=json", "--", minionID, local, remote)
	if err != nil {
		_ = os.Remove(local)
		return StagedBundle{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "Could not stage Salt target bundle", Retryable: true}
	}
	if _, found, parseErr := MinionResult(output, minionID); parseErr != nil || !found {
		_ = os.Remove(local)
		return StagedBundle{}, errors.New("salt-cp returned no target result")
	}
	return StagedBundle{LocalPath: local, RemotePath: remote}, nil
}

func (d *Driver) Cleanup(bundle StagedBundle) {
	if ensureStagingPath(d.StagingRoot, bundle.LocalPath) == nil {
		_ = os.Remove(bundle.LocalPath)
	}
}

func (d *Driver) CleanupRemote(minionID string, bundle StagedBundle) {
	d.Cleanup(bundle)
	if !minionPattern.MatchString(minionID) || !strings.HasPrefix(filepath.Clean(bundle.RemotePath), remoteStagingRoot+string(filepath.Separator)) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = d.Call(ctx, minionID, "toolhub.cleanup_bundle", bundle.RemotePath)
}

func ensureStagingPath(root, candidate string) error {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("staging path escapes Bridge staging root")
	}
	return nil
}

func DecodeStreamingJSON(body []byte) ([]map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(body)))
	var result []map[string]json.RawMessage
	for {
		var object map[string]json.RawMessage
		err := decoder.Decode(&object)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if object == nil {
			return nil, errors.New("Salt JSON output contains a non-object value")
		}
		result = append(result, object)
	}
	if len(result) == 0 {
		return nil, errors.New("Salt JSON output is empty")
	}
	return result, nil
}

func MinionResult(body []byte, minionID string) (json.RawMessage, bool, error) {
	objects, err := DecodeStreamingJSON(body)
	if err != nil {
		return nil, false, err
	}
	for _, object := range objects {
		if value, ok := object[minionID]; ok {
			return append(json.RawMessage(nil), value...), true, nil
		}
	}
	return nil, false, nil
}

func ParseAsyncJID(body []byte, minionID string) (string, error) {
	value, found, err := MinionResult(body, minionID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("Salt async response contains no target JID")
	}
	var jid string
	if json.Unmarshal(value, &jid) != nil {
		var response struct {
			JID string `json:"jid"`
		}
		if json.Unmarshal(value, &response) != nil {
			return "", errors.New("Salt async JID response is invalid")
		}
		jid = response.JID
	}
	if !jidPattern.MatchString(jid) {
		return "", errors.New("Salt async JID is invalid")
	}
	return jid, nil
}
