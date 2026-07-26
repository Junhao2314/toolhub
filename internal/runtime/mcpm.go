package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/security"
)

const (
	mcpmStoreRelativePath = ".config/mcpm/servers.json"
	mcpmMaxBytes          = 4 << 20
	managedCodexProfile   = "toolhub-codex"
	managedClaudeProfile  = "toolhub-claude"
)

// MCPMServer is the normalized subset of the mcpm global server document that
// ToolHub owns. Unknown fields in the on-disk document are preserved by the
// structured patcher.
type MCPMServer struct {
	Name        string            `json:"name"`
	Type        string            `json:"type,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	URL         string            `json:"url,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	ProfileTags []string          `json:"profile_tags,omitempty"`
}

// MCPMScan contains only non-secret descriptors plus values held on the Agent
// until the control plane issues a one-time capture request.
type MCPMScan struct {
	Servers []domain.MCPDescriptor
	Secrets map[string]MCPSecretValues
}

func MCPMProfileForRuntime(runtimeKind string) string {
	switch runtimeKind {
	case domain.RuntimeCodex:
		return managedCodexProfile
	case domain.RuntimeClaude:
		return managedClaudeProfile
	default:
		return ""
	}
}

func MCPMStorePath(home string) string {
	return filepath.Join(home, mcpmStoreRelativePath)
}

// ScanMCPM reads the global mcpm registry without writing it. A server carrying
// the legacy all-mcp tag is seeded into both managed runtime profiles until a
// ToolHub profile tag exists; this makes the first cutover behaviour-preserving.
func ScanMCPM(home string, fingerprintKey []byte) (MCPMScan, error) {
	path := MCPMStorePath(home)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return MCPMScan{Servers: []domain.MCPDescriptor{}, Secrets: map[string]MCPSecretValues{}}, nil
	}
	if err != nil {
		return MCPMScan{}, err
	}
	if len(body) > mcpmMaxBytes {
		return MCPMScan{}, errors.New("mcpm server registry exceeds 4 MiB")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return MCPMScan{}, fmt.Errorf("parse mcpm server registry: %w", err)
	}
	hasManagedTag := map[string]bool{}
	for _, raw := range document {
		var server MCPMServer
		if json.Unmarshal(raw, &server) == nil {
			for _, tag := range server.ProfileTags {
				switch tag {
				case managedCodexProfile:
					hasManagedTag[domain.RuntimeCodex] = true
				case managedClaudeProfile:
					hasManagedTag[domain.RuntimeClaude] = true
				}
			}
		}
	}
	result := MCPMScan{Servers: []domain.MCPDescriptor{}, Secrets: map[string]MCPSecretValues{}}
	names := make([]string, 0, len(document))
	for name := range document {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var server MCPMServer
		if err := json.Unmarshal(document[name], &server); err != nil {
			continue
		}
		server.Name = strings.TrimSpace(server.Name)
		if server.Name == "" {
			server.Name = name
		}
		targets := mcpmTargetRuntimes(server.ProfileTags, hasManagedTag)
		if len(targets) == 0 {
			continue
		}
		transport := ""
		if server.Type == "remote" || server.URL != "" {
			transport = "streamable-http"
		} else if server.Command != "" {
			transport = "stdio"
		}
		descriptor, err := protocol.NormalizeMCPDescriptor(domain.RuntimeCodex, domain.MCPDescriptor{
			Name: server.Name, Transport: transport, Command: server.Command, Args: server.Args,
			URL: server.URL, EnvKeys: sortedStringMapKeys(server.Env), HeaderKeys: sortedStringMapKeys(server.Headers),
		})
		if err != nil {
			continue
		}
		if len(fingerprintKey) > 0 && (len(server.Env) > 0 || len(server.Headers) > 0) {
			descriptor.SecretFingerprint = fingerprintSecretValues(fingerprintKey, server.Env, server.Headers)
		}
		descriptor.ImportSource = "mcpm"
		descriptor.ImportSourceName = "mcpm"
		descriptor.ImportRuntime = domain.RuntimeCodex
		descriptor.ImportEnabled = server.Enabled == nil || *server.Enabled
		descriptor.TargetRuntimes = targets
		descriptor.ProfileTags = append([]string(nil), server.ProfileTags...)
		result.Servers = append(result.Servers, descriptor)
		result.Secrets[descriptor.Identity] = MCPSecretValues{Env: cloneStringMap(server.Env), Headers: cloneStringMap(server.Headers)}
	}
	return result, nil
}

func mcpmTargetRuntimes(tags []string, hasManagedTag map[string]bool) []string {
	set := map[string]bool{}
	for _, tag := range tags {
		switch tag {
		case managedCodexProfile:
			set[domain.RuntimeCodex] = true
		case managedClaudeProfile:
			set[domain.RuntimeClaude] = true
		case "all-mcp":
			if !hasManagedTag[domain.RuntimeCodex] {
				set[domain.RuntimeCodex] = true
			}
			if !hasManagedTag[domain.RuntimeClaude] {
				set[domain.RuntimeClaude] = true
			}
		}
	}
	result := make([]string, 0, len(set))
	for runtimeKind := range set {
		result = append(result, runtimeKind)
	}
	sort.Strings(result)
	return result
}

// ScanSharedMCPImports turns a legacy manifest into disabled import candidates.
// The manifest remains read-only; plaintext values stay in the returned Agent
// memory map until a one-time capture request is accepted by the control plane.
func ScanSharedMCPImports(sources []SharedSourceConfig, fingerprintKey []byte) ([]domain.MCPDescriptor, map[string]MCPSecretValues) {
	var result []domain.MCPDescriptor
	secrets := map[string]MCPSecretValues{}
	for _, source := range sources {
		manifest, err := ReadSharedManifest(source.MCPManifest, fingerprintKey)
		if err != nil {
			continue
		}
		for _, server := range manifest.Servers {
			descriptor := server.Descriptor
			descriptor.ImportSource = "shared-manifest"
			descriptor.ImportSourceName = source.Name
			descriptor.ImportRuntime = domain.RuntimeClaude
			descriptor.ImportEnabled = false
			descriptor.TargetRuntimes = nil
			result = append(result, descriptor)
			secrets[descriptor.Identity] = MCPSecretValues{Env: cloneStringMap(server.Env), Headers: cloneStringMap(server.Headers)}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ImportSourceName != result[j].ImportSourceName {
			return result[i].ImportSourceName < result[j].ImportSourceName
		}
		return result[i].Name < result[j].Name
	})
	return result, secrets
}

func fingerprintSecretValues(key []byte, env, headers map[string]string) string {
	values := make(map[string]string, len(env)+len(headers))
	for name, value := range env {
		values["env:"+name] = value
	}
	for name, value := range headers {
		values["header:"+name] = value
	}
	return securityFingerprint(key, values)
}

func securityFingerprint(key []byte, values map[string]string) string {
	// Keep this helper local to the runtime package so inventory code can use the
	// same keyed fingerprint primitive without exposing plaintext values.
	// The control plane verifies it with security.FingerprintSecretMap.
	return security.FingerprintSecretMap(key, values)
}

type mcpmDocument struct {
	Path    string
	Exists  bool
	Mode    os.FileMode
	Raw     []byte
	Servers map[string]map[string]any
}

type MCPMApplyResult struct {
	Method     string
	BackupPath string
	Restore    func() error
}

// ApplyMCPM updates the global server registry and profile tags atomically from
// the Agent's point of view. It never touches a runtime-native config file.
func ApplyMCPM(ctx context.Context, home, dataDir, runtimeKind, profile string, servers []protocol.MCPServerRef, enabled bool, resolve SecretResolver) (protocol.ApplyMCPResult, MCPMApplyResult, error) {
	if runtimeKind != domain.RuntimeCodex && runtimeKind != domain.RuntimeClaude {
		return protocol.ApplyMCPResult{}, MCPMApplyResult{}, errors.New("mcpm delivery is managed only for Codex and Claude")
	}
	if profile == "" {
		profile = MCPMProfileForRuntime(runtimeKind)
	}
	if profile != MCPMProfileForRuntime(runtimeKind) {
		return protocol.ApplyMCPResult{}, MCPMApplyResult{}, errors.New("invalid mcpm profile for runtime")
	}
	desired := make([]MCPMServer, 0, len(servers))
	if enabled {
		for _, ref := range servers {
			server, err := resolveMCPMServer(ctx, ref, resolve)
			if err != nil {
				return protocol.ApplyMCPResult{}, MCPMApplyResult{}, err
			}
			desired = append(desired, server)
		}
	}
	result, err := applyMCPMDocument(ctx, home, dataDir, profile, desired)
	if err != nil {
		return protocol.ApplyMCPResult{}, MCPMApplyResult{}, err
	}
	applyResult := protocol.ApplyMCPResult{
		ActualHash:    hashMCPMDesired(desired),
		ActualEnabled: enabled,
		Method:        result.Method,
		ServerCount:   len(desired),
		ConfigPath:    MCPMStorePath(home),
	}
	if result.BackupPath != "" {
		applyResult.BackupPaths = []string{result.BackupPath}
	}
	return applyResult, result, nil
}

func resolveMCPMServer(ctx context.Context, ref protocol.MCPServerRef, resolve SecretResolver) (MCPMServer, error) {
	name := strings.TrimSpace(ref.Name)
	if name == "" {
		return MCPMServer{}, errors.New("MCP server name is required")
	}
	enabled := true
	server := MCPMServer{Name: name, Command: strings.TrimSpace(ref.Command), Args: append([]string(nil), ref.Args...), URL: strings.TrimSpace(ref.URL), Env: map[string]string{}, Headers: map[string]string{}, Enabled: &enabled}
	switch ref.Transport {
	case "stdio":
		if server.Command == "" {
			return MCPMServer{}, fmt.Errorf("MCP server %q requires a command", name)
		}
		server.Type = "stdio"
	case "sse", "streamable-http":
		if server.URL == "" {
			return MCPMServer{}, fmt.Errorf("MCP server %q requires a URL", name)
		}
		server.Type = "remote"
	default:
		return MCPMServer{}, fmt.Errorf("MCP server %q has unsupported transport %q", name, ref.Transport)
	}
	for key, secretID := range ref.EnvRefs {
		if resolve == nil {
			return MCPMServer{}, errors.New("MCP secret resolver is unavailable")
		}
		value, err := resolve(ctx, secretID)
		if err != nil {
			return MCPMServer{}, fmt.Errorf("resolve MCP environment secret: %w", err)
		}
		server.Env[strings.TrimSpace(key)] = value
	}
	for key, secretID := range ref.HeaderRefs {
		if resolve == nil {
			return MCPMServer{}, errors.New("MCP secret resolver is unavailable")
		}
		value, err := resolve(ctx, secretID)
		if err != nil {
			return MCPMServer{}, fmt.Errorf("resolve MCP header secret: %w", err)
		}
		server.Headers[strings.TrimSpace(key)] = value
	}
	return server, nil
}

func hashMCPMDesired(servers []MCPMServer) string {
	copyServers := append([]MCPMServer(nil), servers...)
	sort.Slice(copyServers, func(i, j int) bool { return copyServers[i].Name < copyServers[j].Name })
	body, _ := json.Marshal(copyServers)
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum[:])
}

func applyMCPMDocument(ctx context.Context, home, dataDir, profile string, desired []MCPMServer) (MCPMApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return MCPMApplyResult{}, err
	}
	document, err := loadMCPMDocument(home)
	if err != nil {
		return MCPMApplyResult{}, err
	}
	before := append([]byte(nil), document.Raw...)
	beforeMode := document.Mode
	for _, server := range desired {
		if err := patchMCPMServer(document, profile, server); err != nil {
			return MCPMApplyResult{}, err
		}
	}
	desiredNames := map[string]bool{}
	for _, server := range desired {
		desiredNames[server.Name] = true
	}
	for name, value := range document.Servers {
		if hasString(value["profile_tags"], profile) && !desiredNames[name] {
			value["profile_tags"] = removeString(value["profile_tags"], profile)
		}
	}
	encoded, err := json.MarshalIndent(document.Servers, "", "  ")
	if err != nil {
		return MCPMApplyResult{}, err
	}
	encoded = append(encoded, '\n')
	if bytes.Equal(bytes.TrimSpace(before), bytes.TrimSpace(encoded)) && document.Exists && document.Mode.Perm() == 0600 {
		return MCPMApplyResult{Method: "mcpm-idempotent", Restore: func() error { return nil }}, nil
	}
	method := "mcpm-structured-json"
	if cliEncoded, ok := renderMCPMProfileWithCLI(ctx, home, document.Servers, profile, desired); ok {
		encoded = cliEncoded
		method = "mcpm-cli-profile"
	}
	backupPath, err := backupMCPM(document.Path, dataDir, before, document.Exists, beforeMode)
	if err != nil {
		return MCPMApplyResult{}, err
	}
	if err := atomicWriteMCPM(document.Path, encoded, 0600); err != nil {
		return MCPMApplyResult{}, err
	}
	return MCPMApplyResult{Method: method, BackupPath: backupPath, Restore: func() error {
		if !document.Exists {
			return os.Remove(document.Path)
		}
		return atomicWriteMCPM(document.Path, before, beforeMode.Perm())
	}}, nil
}

// renderMCPMProfileWithCLI lets mcpm own profile membership semantics without
// passing plaintext secrets through command-line arguments. ToolHub stages the
// already validated server definitions in an isolated HOME, asks mcpm to set the
// profile, and accepts the result only when it is semantically identical to the
// structured patch. Any CLI incompatibility falls back to the atomic JSON writer.
func renderMCPMProfileWithCLI(ctx context.Context, home string, expected map[string]map[string]any, profile string, desired []MCPMServer) ([]byte, bool) {
	binary := mcpBinary(home)
	if binary == "" || len(desired) == 0 {
		return nil, false
	}
	names := make([]string, 0, len(desired))
	for _, server := range desired {
		if strings.Contains(server.Name, ",") {
			return nil, false
		}
		names = append(names, server.Name)
	}
	sort.Strings(names)
	stagingHome, err := os.MkdirTemp("", "toolhub-mcpm-cli-")
	if err != nil {
		return nil, false
	}
	defer os.RemoveAll(stagingHome)
	staged := cloneMCPMServers(expected)
	for _, entry := range staged {
		entry["profile_tags"] = removeString(entry["profile_tags"], profile)
	}
	body, err := json.MarshalIndent(staged, "", "  ")
	if err != nil {
		return nil, false
	}
	body = append(body, '\n')
	path := MCPMStorePath(stagingHome)
	if err := atomicWriteMCPM(path, body, 0600); err != nil {
		return nil, false
	}
	environment := mcpmCommandEnvironment(stagingHome)
	commands := [][]string{
		{"profile", "create", profile, "--force"},
		{"profile", "edit", profile, "--set-servers", strings.Join(names, ","), "--force"},
	}
	for _, arguments := range commands {
		command := exec.CommandContext(ctx, binary, arguments...)
		command.Env = environment
		if output, commandErr := command.CombinedOutput(); commandErr != nil || len(output) > 1<<20 {
			return nil, false
		}
	}
	rendered, err := readAllLimited(path, mcpmMaxBytes)
	if err != nil || len(rendered) > mcpmMaxBytes {
		return nil, false
	}
	var actual map[string]map[string]any
	if json.Unmarshal(rendered, &actual) != nil || !reflect.DeepEqual(actual, expected) {
		return nil, false
	}
	encoded, err := json.MarshalIndent(actual, "", "  ")
	if err != nil {
		return nil, false
	}
	return append(encoded, '\n'), true
}

func cloneMCPMServers(source map[string]map[string]any) map[string]map[string]any {
	body, _ := json.Marshal(source)
	result := map[string]map[string]any{}
	_ = json.Unmarshal(body, &result)
	return result
}

func mcpmCommandEnvironment(home string) []string {
	result := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "XDG_CONFIG_HOME=") || strings.HasPrefix(entry, "MCPM_NON_INTERACTIVE=") || strings.HasPrefix(entry, "PYTHONWARNINGS=") {
			continue
		}
		result = append(result, entry)
	}
	return append(result,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"MCPM_NON_INTERACTIVE=true",
		"PYTHONWARNINGS=ignore::DeprecationWarning",
	)
}

func loadMCPMDocument(home string) (*mcpmDocument, error) {
	path := MCPMStorePath(home)
	document := &mcpmDocument{Path: path, Servers: map[string]map[string]any{}}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return nil, err
	}
	if len(body) > mcpmMaxBytes {
		return nil, errors.New("mcpm server registry exceeds 4 MiB")
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse mcpm server registry: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	document.Exists, document.Raw, document.Mode, document.Servers = true, body, info.Mode(), raw
	return document, nil
}

func patchMCPMServer(document *mcpmDocument, profile string, desired MCPMServer) error {
	entry, exists := document.Servers[desired.Name]
	if !exists {
		entry = map[string]any{"name": desired.Name}
		document.Servers[desired.Name] = entry
	} else if !sameMCPMDefinition(entry, desired) && !hasString(entry["profile_tags"], profile) {
		return fmt.Errorf("mcpm server %q conflicts with an unowned local definition", desired.Name)
	}
	entry["name"] = desired.Name
	entry["command"] = desired.Command
	entry["args"] = append([]string(nil), desired.Args...)
	entry["enabled"] = true
	entry["profile_tags"] = addString(entry["profile_tags"], profile)
	if desired.Type == "remote" {
		entry["type"] = "remote"
		entry["url"] = desired.URL
		entry["headers"] = cloneStringMap(desired.Headers)
		delete(entry, "env")
		delete(entry, "command")
		delete(entry, "args")
	} else {
		delete(entry, "type")
		delete(entry, "url")
		delete(entry, "headers")
		entry["command"] = desired.Command
		entry["args"] = append([]string(nil), desired.Args...)
		entry["env"] = cloneStringMap(desired.Env)
	}
	return nil
}

func sameMCPMDefinition(entry map[string]any, desired MCPMServer) bool {
	if desired.Type == "remote" {
		return stringValue(entry["url"]) == desired.URL && stringValue(entry["type"]) == "remote" && equalStringMap(entry["headers"], desired.Headers)
	}
	return stringValue(entry["command"]) == desired.Command && equalStringSlice(entry["args"], desired.Args) && equalStringMap(entry["env"], desired.Env)
}

func backupMCPM(path, dataDir string, body []byte, exists bool, _ os.FileMode) (string, error) {
	if !exists {
		return "", nil
	}
	root := filepath.Join(dataDir, "backups", "mcpm")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	backup := filepath.Join(root, time.Now().UTC().Format("20060102T150405.000000000Z")+"-servers.json")
	if err := os.WriteFile(backup, body, 0600); err != nil {
		return "", err
	}
	return backup, nil
}

func atomicWriteMCPM(path string, body []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".toolhub-new-"+uuid.NewString())
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	remove = false
	return syncDirectory(filepath.Dir(path))
}

func mcpBinary(home string) string {
	if path, err := exec.LookPath("mcpm"); err == nil {
		return path
	}
	for _, candidate := range []string{filepath.Join(home, ".local", "bin", "mcpm"), "/usr/local/bin/mcpm"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return candidate
		}
	}
	return ""
}

func readAllLimited(path string, max int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, max+1))
}

func equalStringMap(raw any, want map[string]string) bool {
	got := map[string]string{}
	if typed, ok := raw.(map[string]any); ok {
		for key, value := range typed {
			if text, ok := value.(string); ok {
				got[key] = text
			}
		}
	}
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func equalStringSlice(raw any, want []string) bool {
	var got []string
	switch typed := raw.(type) {
	case []any:
		for _, value := range typed {
			text, ok := value.(string)
			if !ok {
				return false
			}
			got = append(got, text)
		}
	case []string:
		got = append(got, typed...)
	default:
		return len(want) == 0
	}
	return len(got) == len(want) && equalStringValues(got, want)
}

func equalStringValues(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func hasString(raw any, value string) bool {
	for _, item := range stringValues(raw) {
		if item == value {
			return true
		}
	}
	return false
}

func addString(raw any, value string) []string {
	items := stringValues(raw)
	if !hasString(items, value) {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func removeString(raw any, value string) []string {
	result := make([]string, 0)
	for _, item := range stringValues(raw) {
		if item != value {
			result = append(result, item)
		}
	}
	sort.Strings(result)
	return result
}

func stringValues(raw any) []string {
	var result []string
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			if value, ok := item.(string); ok && value != "" {
				result = append(result, value)
			}
		}
	case []string:
		for _, item := range typed {
			if item != "" {
				result = append(result, item)
			}
		}
	}
	return result
}
