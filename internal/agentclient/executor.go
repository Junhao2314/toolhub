package agentclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/skills"
)

type Executor struct {
	config  Config
	http    *http.Client
	history *taskHistory
}

func NewExecutor(config Config) *Executor {
	return &Executor{config: config, http: &http.Client{Timeout: 45 * time.Second}, history: loadHistory(config.DataDir)}
}

func (e *Executor) Execute(ctx context.Context, task domain.AgentTask) (string, json.RawMessage) {
	if err := e.verify(task); err != nil {
		return "failed", marshalResult(map[string]any{"error": err.Error(), "code": "signature_invalid"})
	}
	digest, err := taskPayloadDigest(task.Kind, task.Payload)
	if err != nil {
		return "failed", marshalResult(map[string]any{"error": err.Error(), "code": "invalid_payload"})
	}
	if record, ok := e.history.get(task.ID); ok && record.Status != "running" {
		if record.Kind != task.Kind || record.PayloadDigest != digest {
			return "failed", marshalResult(map[string]any{"error": "task ID was replayed with different semantics", "code": "task_replay_mismatch"})
		}
		return record.Status, record.Result
	}
	lock, err := acquireExecutionLock(ctx, e.config.DataDir)
	if err != nil {
		return "failed", marshalResult(map[string]any{"error": err.Error(), "code": "execution_lock_unavailable"})
	}
	defer lock.release()
	if err := e.history.put(task.ID, taskRecord{Kind: task.Kind, PayloadDigest: digest, Status: "running", Result: json.RawMessage(`{}`), StartedAt: time.Now().UTC()}); err != nil {
		return "failed", marshalResult(map[string]any{"error": err.Error(), "code": "history_write_failed"})
	}
	var result any
	switch task.Kind {
	case "scan_inventory":
		result, err = e.discoverInventory(ctx)
	case "deploy_skill":
		result, err = e.deploySkill(ctx, task.Payload)
	case "apply_mcp":
		result, err = e.applyMCP(ctx, task.Payload)
	case "adopt_skill":
		result, err = e.adoptSkill(ctx, task.ID, task.Payload)
	default:
		err = fmt.Errorf("unsupported task kind %q", task.Kind)
	}
	status := "succeeded"
	if err != nil {
		status = "failed"
		result = map[string]any{"error": err.Error()}
	}
	encoded := marshalResult(result)
	if err := e.history.put(task.ID, taskRecord{Kind: task.Kind, PayloadDigest: digest, Status: status, Result: encoded, CompletedAt: time.Now().UTC()}); err != nil {
		return "failed", marshalResult(map[string]any{"error": err.Error(), "code": "history_write_failed"})
	}
	return status, encoded
}

func (e *Executor) verify(task domain.AgentTask) error {
	key, err := base64.StdEncoding.DecodeString(e.config.TaskKey)
	if err != nil || len(key) != 32 {
		return errors.New("agent task key is invalid")
	}
	signingBytes, err := protocol.TaskSigningBytes(task.ID, task.Kind, task.Payload)
	if err != nil {
		return err
	}
	if !security.VerifyPayload(key, signingBytes, task.Signature) {
		return errors.New("task signature verification failed")
	}
	return nil
}

func (e *Executor) deploySkill(ctx context.Context, raw json.RawMessage) (any, error) {
	var request protocol.DeploySkillPayload
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	var artifact []byte
	var err error
	if request.Enabled {
		artifact, err = e.fetchBytes(ctx, "/agent/v1/artifacts/"+request.VersionID, 20<<20)
		if err != nil {
			return nil, err
		}
	}
	deployer := runtimeadapter.Deployer{DataDir: e.config.DataDir, Paths: e.config.Paths}
	result, err := deployer.Deploy(runtimeadapter.DeployRequest{Runtime: request.Runtime, SkillSlug: request.SkillSlug, VersionID: request.VersionID, SHA256: request.SHA256, Enabled: request.Enabled, Artifact: artifact})
	return protocol.DeploySkillResult{ActualHash: result.ActualHash, ActualEnabled: request.Enabled, BackupPath: result.BackupPath, Changed: result.Changed}, err
}

func (e *Executor) applyMCP(ctx context.Context, raw json.RawMessage) (any, error) {
	var request protocol.ApplyMCPPayload
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	resolver := func(ctx context.Context, id string) (string, error) {
		body, err := e.fetchBytes(ctx, "/agent/v1/secrets/"+id, 1<<20)
		if err != nil {
			return "", err
		}
		var response struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return "", err
		}
		return response.Value, nil
	}
	return runtimeadapter.ApplyMCP(ctx, e.config.Paths, e.config.DataDir, request, resolver)
}

func taskPayloadDigest(kind string, payload json.RawMessage) (string, error) {
	var semantic any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&semantic); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(semantic)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(kind+"\n"), canonical...))
	return hex.EncodeToString(sum[:]), nil
}

type executionLock struct {
	path string
	file *os.File
}

func acquireExecutionLock(ctx context.Context, dataDir string) (*executionLock, error) {
	if dataDir == "" {
		dataDir = "."
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "task-execution.lock")
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "pid=%d\ncreatedAt=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			_ = file.Sync()
			return &executionLock{path: path, file: file}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 2*time.Hour {
			_ = os.Remove(path)
			continue
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *executionLock) release() {
	if l == nil {
		return
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	_ = os.Remove(l.path)
}

func (e *Executor) fetchBytes(ctx context.Context, endpoint string, max int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.config.ServerURL, "/")+endpoint, nil)
	if err != nil {
		return nil, err
	}
	e.authorizeAgentRequest(request)
	response, err := e.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control plane returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, errors.New("control plane response exceeds size limit")
	}
	return body, nil
}

func (e *Executor) adoptSkill(ctx context.Context, taskID string, raw json.RawMessage) (any, error) {
	var request struct {
		DiscoveryID string `json:"discoveryId"`
		Runtime     string `json:"runtime"`
		Path        string `json:"path"`
		SHA256      string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	paths := e.config.Paths
	var pkg skills.Package
	var err error
	if request.Runtime == domain.RuntimeShared {
		var sharedSource *runtimeadapter.SharedSourceConfig
		for _, source := range e.config.SharedSources {
			relative, relErr := filepath.Rel(source.SkillsRoot, request.Path)
			if relErr == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				sourceCopy := source
				sharedSource = &sourceCopy
				break
			}
		}
		if sharedSource == nil {
			return nil, errors.New("shared Skill discovery is outside configured sources")
		}
		pkg, err = runtimeadapter.PackageSharedDiscoveredSkill(*sharedSource, request.Path, request.SHA256)
	} else {
		pkg, err = runtimeadapter.PackageDiscoveredSkill(paths, request.Runtime, request.Path, request.SHA256)
	}
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.config.ServerURL, "/")+"/agent/v1/discoveries/"+request.DiscoveryID+"/skill", bytes.NewReader(pkg.CanonicalZIP))
	if err != nil {
		return nil, err
	}
	e.authorizeAgentRequest(httpRequest)
	httpRequest.Header.Set("Content-Type", "application/zip")
	httpRequest.Header.Set("X-ToolHub-Task-ID", taskID)
	httpRequest.Header.Set("X-Content-SHA256", pkg.SHA256)
	response, err := e.http.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("upload discovered Skill: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control plane rejected discovered Skill with HTTP %d", response.StatusCode)
	}
	var imported struct {
		SkillID   string `json:"skillId"`
		VersionID string `json:"versionId"`
		SHA256    string `json:"sha256"`
	}
	if err := json.Unmarshal(body, &imported); err != nil {
		return nil, errors.New("control plane returned an invalid Skill adoption response")
	}
	marker := runtimeadapter.AdoptedSkillMarker{SkillID: imported.SkillID, VersionID: imported.VersionID, SHA256: imported.SHA256}
	if request.Runtime != domain.RuntimeShared {
		if err := runtimeadapter.MarkAdoptedSkill(paths, request.Runtime, request.Path, request.SHA256, marker); err != nil {
			return nil, err
		}
	}
	return map[string]any{"discoveryId": request.DiscoveryID, "skillId": imported.SkillID, "versionId": imported.VersionID, "sha256": imported.SHA256, "markerWritten": request.Runtime != domain.RuntimeShared}, nil
}

func marshalResult(value any) json.RawMessage {
	body, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"error":"encode task result"}`)
	}
	return body
}
