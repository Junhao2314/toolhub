package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/skills"
)

var localMCPNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func (m *Manager) ExportLocalSkill(user ManagedUser, input bridgeprotocol.LocalSkillExportRequest) (bridgeprotocol.LocalSkillExportResponse, error) {
	if err := validateLocalIntakeTarget(input.Target); err != nil {
		return bridgeprotocol.LocalSkillExportResponse{}, err
	}
	if !bridgeprotocol.IsSHA256(input.ExpectedRevision) || !bridgeprotocol.IsSHA256(input.ContentHash) {
		return bridgeprotocol.LocalSkillExportResponse{}, errors.New("local Skill import requires scanned revision and content hash")
	}
	name := strings.TrimSpace(input.Name)
	if bridgeprotocol.IsProtectedSkillEntry(name) || filepath.Base(name) != name {
		return bridgeprotocol.LocalSkillExportResponse{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrProtectedScope, Message: "Local Skill is protected or invalid"}
	}
	current, err := m.Scan(user, input.Target.Runtime)
	if err != nil {
		return bridgeprotocol.LocalSkillExportResponse{}, err
	}
	if current.Revision != input.ExpectedRevision {
		return bridgeprotocol.LocalSkillExportResponse{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Local Skill inventory changed before import"}
	}
	found := false
	for _, member := range current.Members {
		if member.Kind == "skill" && member.Name == name && !member.Protected && member.ContentHash == input.ContentHash {
			found = true
			break
		}
	}
	if !found {
		return bridgeprotocol.LocalSkillExportResponse{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Local Skill no longer matches the scanned inventory"}
	}
	paths, err := PathsFor(user, input.Target.Runtime)
	if err != nil {
		return bridgeprotocol.LocalSkillExportResponse{}, err
	}
	source := filepath.Join(paths.SkillsRoot, name)
	if err := rejectSymlinkComponents(user.Home, source); err != nil {
		return bridgeprotocol.LocalSkillExportResponse{}, err
	}
	pkg, err := skills.ScanDirectory(source, skills.DefaultLimits)
	if err != nil {
		return bridgeprotocol.LocalSkillExportResponse{}, err
	}
	if pkg.ContentHash != input.ContentHash {
		return bridgeprotocol.LocalSkillExportResponse{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Local Skill changed during import"}
	}
	return bridgeprotocol.LocalSkillExportResponse{Name: name, SHA256: pkg.SHA256, ContentHash: pkg.ContentHash, Archive: pkg.CanonicalZIP}, nil
}

func (m *Manager) PreviewLocalMCP(user ManagedUser, target bridgeprotocol.Target) (bridgeprotocol.LocalMCPPreviewResponse, error) {
	if err := validateLocalIntakeTarget(target); err != nil {
		return bridgeprotocol.LocalMCPPreviewResponse{}, err
	}
	revision, entries, err := readLocalMCPEntries(user, target.Runtime)
	if err != nil {
		return bridgeprotocol.LocalMCPPreviewResponse{}, err
	}
	items := make([]bridgeprotocol.LocalMCPServerPreview, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entry.Preview)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return bridgeprotocol.LocalMCPPreviewResponse{TargetRevision: revision, Items: items}, nil
}

func (m *Manager) CaptureLocalMCP(user ManagedUser, input bridgeprotocol.LocalMCPCaptureRequest) (bridgeprotocol.LocalMCPCaptureResponse, error) {
	if err := validateLocalIntakeTarget(input.Target); err != nil {
		return bridgeprotocol.LocalMCPCaptureResponse{}, err
	}
	if !bridgeprotocol.IsSHA256(input.ExpectedRevision) || !bridgeprotocol.IsSHA256(input.ContentHash) {
		return bridgeprotocol.LocalMCPCaptureResponse{}, errors.New("local MCP capture requires preview revision and content hash")
	}
	revision, entries, err := readLocalMCPEntries(user, input.Target.Runtime)
	if err != nil {
		return bridgeprotocol.LocalMCPCaptureResponse{}, err
	}
	if revision != input.ExpectedRevision {
		return bridgeprotocol.LocalMCPCaptureResponse{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Local MCP configuration changed after confirmation"}
	}
	entry, ok := entries[input.Name]
	if !ok || entry.Preview.ContentHash != input.ContentHash {
		return bridgeprotocol.LocalMCPCaptureResponse{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Local MCP server changed after confirmation"}
	}
	return bridgeprotocol.LocalMCPCaptureResponse{Preview: entry.Preview, Env: cloneStringMap(entry.Env), Headers: cloneStringMap(entry.Headers)}, nil
}

func validateLocalIntakeTarget(target bridgeprotocol.Target) error {
	if err := target.Validate(false); err != nil {
		return err
	}
	if target.NodeKind != bridgeprotocol.NodeKindLocal || (target.Runtime != bridgeprotocol.RuntimeClaude && target.Runtime != bridgeprotocol.RuntimeCodex) {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrProtectedScope, Message: "Library intake is available only from local Claude or Codex"}
	}
	return nil
}

type localMCPEntry struct {
	Preview bridgeprotocol.LocalMCPServerPreview
	Env     map[string]string
	Headers map[string]string
}

func readLocalMCPEntries(user ManagedUser, runtimeKind string) (string, map[string]localMCPEntry, error) {
	paths, err := PathsFor(user, runtimeKind)
	if err != nil {
		return "", nil, err
	}
	path := paths.ClaudeConfig
	if runtimeKind == bridgeprotocol.RuntimeCodex {
		path = paths.CodexConfig
	}
	if err := rejectSymlinkComponents(user.Home, path); err != nil {
		return "", nil, err
	}
	body, err := readSafeConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		body = []byte{}
	} else if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(body)
	revision := hex.EncodeToString(sum[:])
	servers, err := decodeNativeMCPServers(runtimeKind, body)
	if err != nil {
		return "", nil, err
	}
	entries := make(map[string]localMCPEntry, len(servers))
	for name, raw := range servers {
		entry, ok := normalizeLocalMCPEntry(runtimeKind, name, raw)
		if ok {
			entries[name] = entry
		}
	}
	return revision, entries, nil
}

func decodeNativeMCPServers(runtimeKind string, body []byte) (map[string]any, error) {
	root := map[string]any{}
	if len(strings.TrimSpace(string(body))) > 0 {
		switch runtimeKind {
		case bridgeprotocol.RuntimeClaude:
			if err := json.Unmarshal(body, &root); err != nil {
				return nil, errors.New("Claude user configuration is invalid")
			}
		case bridgeprotocol.RuntimeCodex:
			if err := toml.Unmarshal(body, &root); err != nil {
				return nil, errors.New("Codex user configuration is invalid")
			}
		}
	}
	key := "mcpServers"
	if runtimeKind == bridgeprotocol.RuntimeCodex {
		key = "mcp_servers"
	}
	if root[key] == nil {
		return map[string]any{}, nil
	}
	servers, ok := root[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s user MCP configuration is not an object", runtimeKind)
	}
	return servers, nil
}

func normalizeLocalMCPEntry(runtimeKind, name string, raw any) (localMCPEntry, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !localMCPNamePattern.MatchString(name) || bridgeprotocol.IsProtectedSkillEntry(name) || name == RelayAnchor {
		return localMCPEntry{}, false
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return localMCPEntry{}, false
	}
	command, commandOK := optionalString(value, "command")
	url, urlOK := optionalString(value, "url")
	if !commandOK || !urlOK || (command == "") == (url == "") {
		return localMCPEntry{}, false
	}
	args, ok := optionalStringSlice(value, "args")
	if !ok {
		return localMCPEntry{}, false
	}
	env, ok := optionalStringMap(value, "env")
	if !ok {
		return localMCPEntry{}, false
	}
	headerKey := "headers"
	if runtimeKind == bridgeprotocol.RuntimeCodex {
		headerKey = "http_headers"
	}
	headers, ok := optionalStringMap(value, headerKey)
	if !ok {
		return localMCPEntry{}, false
	}
	transport := "stdio"
	if url != "" {
		transport = "http"
		if explicit, valid := optionalString(value, "type"); !valid {
			return localMCPEntry{}, false
		} else if explicit != "" {
			transport = strings.ToLower(explicit)
		}
		if explicit, valid := optionalString(value, "transport"); !valid {
			return localMCPEntry{}, false
		} else if explicit != "" {
			transport = strings.ToLower(explicit)
		}
		if transport != "http" && transport != "sse" {
			return localMCPEntry{}, false
		}
		if len(args) > 0 {
			return localMCPEntry{}, false
		}
	} else if len(headers) > 0 {
		return localMCPEntry{}, false
	}
	canonical := struct {
		Name, Transport, Command, URL string
		Args                          []string
		Env, Headers                  map[string]string
	}{name, transport, command, url, args, env, headers}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	preview := bridgeprotocol.LocalMCPServerPreview{Name: name, Transport: transport, Command: command, Args: args, URL: url, EnvKeys: sortedKeys(env), HeaderKeys: sortedKeys(headers), ContentHash: hex.EncodeToString(sum[:])}
	return localMCPEntry{Preview: preview, Env: env, Headers: headers}, true
}

func optionalString(value map[string]any, key string) (string, bool) {
	raw, exists := value[key]
	if !exists || raw == nil {
		return "", true
	}
	text, ok := raw.(string)
	return strings.TrimSpace(text), ok
}

func optionalStringSlice(value map[string]any, key string) ([]string, bool) {
	raw, exists := value[key]
	if !exists || raw == nil {
		return []string{}, true
	}
	items, ok := raw.([]any)
	if !ok {
		if typed, typedOK := raw.([]string); typedOK {
			return append([]string(nil), typed...), true
		}
		return nil, false
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}

func optionalStringMap(value map[string]any, key string) (map[string]string, bool) {
	raw, exists := value[key]
	if !exists || raw == nil {
		return map[string]string{}, true
	}
	items, ok := raw.(map[string]any)
	if !ok {
		if typed, typedOK := raw.(map[string]string); typedOK {
			return cloneStringMap(typed), true
		}
		return nil, false
	}
	result := make(map[string]string, len(items))
	for key, item := range items {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(key) == "" {
			return nil, false
		}
		result[key] = text
	}
	return result, true
}

func sortedKeys(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
