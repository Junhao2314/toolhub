package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type AnchorApplyResult struct {
	ConfigPath string
	BackupPath string
	Restore    func() error
}

// ApplyRuntimeMCPAnchor writes the one ToolHub-owned native anchor. It keeps all
// unrelated JSON/TOML content intact and removes the legacy all-mcp Codex table.
func ApplyRuntimeMCPAnchor(home, dataDir, runtimeKind, profile string, enabled bool) (AnchorApplyResult, error) {
	switch runtimeKind {
	case "claude":
		return applyClaudeAnchor(filepath.Join(home, ".claude.json"), dataDir, profile, enabled)
	case "codex":
		return applyCodexAnchor(filepath.Join(home, ".codex", "config.toml"), dataDir, profile, enabled)
	default:
		return AnchorApplyResult{}, errors.New("MCP anchors are managed only for Claude and Codex")
	}
}

func InspectRuntimeMCPAnchor(home, runtimeKind string) map[string]any {
	profile := MCPMProfileForRuntime(runtimeKind)
	path := ""
	switch runtimeKind {
	case "claude":
		path = filepath.Join(home, ".claude.json")
	case "codex":
		path = filepath.Join(home, ".codex", "config.toml")
	default:
		return map[string]any{"present": false, "valid": false}
	}
	servers := readMCPConfig(path, runtimeKind)
	entry, present := servers[profile].(map[string]any)
	valid := present && stringValue(entry["command"]) == "mcpm" && equalStringSlice(entry["args"], []string{"profile", "run", profile})
	legacy := false
	if old, ok := servers["all-mcp"].(map[string]any); ok {
		legacy = stringValue(old["command"]) == "mcpm" && equalStringSlice(old["args"], []string{"profile", "run", "all-mcp"})
	}
	return map[string]any{"path": path, "profile": profile, "present": present, "valid": valid, "legacyAllMCP": legacy}
}

func applyClaudeAnchor(path, dataDir, profile string, enabled bool) (AnchorApplyResult, error) {
	root, before, mode, exists, err := loadJSONObject(path)
	if err != nil {
		return AnchorApplyResult{}, err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	const anchor = "toolhub-claude"
	if !enabled {
		if !exists || !ok {
			return AnchorApplyResult{ConfigPath: path, Restore: func() error { return nil }}, nil
		}
		if _, found := servers[anchor]; !found {
			return AnchorApplyResult{ConfigPath: path, Restore: func() error { return nil }}, nil
		}
	}
	if !ok {
		if root["mcpServers"] != nil {
			return AnchorApplyResult{}, errors.New("Claude mcpServers value is not an object")
		}
		servers = map[string]any{}
		root["mcpServers"] = servers
	}
	if enabled {
		expected := map[string]any{"command": "mcpm", "args": []any{"profile", "run", profile}}
		servers[anchor] = expected
	} else {
		delete(servers, anchor)
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return AnchorApplyResult{}, err
	}
	encoded = append(encoded, '\n')
	return writeAnchor(path, dataDir, before, mode, exists, encoded, func() error {
		return restoreAnchor(path, before, mode, exists)
	})
}

func applyCodexAnchor(path, dataDir, profile string, enabled bool) (AnchorApplyResult, error) {
	before, mode, exists, err := readOptionalFile(path)
	if err != nil {
		return AnchorApplyResult{}, err
	}
	if !exists && !enabled {
		return AnchorApplyResult{ConfigPath: path, Restore: func() error { return nil }}, nil
	}
	sections := map[string]string{}
	removals := map[string]bool{}
	if enabled {
		sections["mcp_servers."+profile] = "[mcp_servers." + profile + "]\ncommand = \"mcpm\"\nargs = [\"profile\", \"run\", " + strconv.Quote(profile) + "]\n"
		if old, ok := readMCPConfig(path, "codex")["all-mcp"].(map[string]any); ok &&
			stringValue(old["command"]) == "mcpm" && equalStringSlice(old["args"], []string{"profile", "run", "all-mcp"}) {
			removals["mcp_servers.all-mcp"] = true
		}
	} else {
		removals["mcp_servers."+profile] = true
	}
	encoded := replaceTOMLSections(before, sections, removals)
	return writeAnchor(path, dataDir, before, mode, exists, encoded, func() error {
		return restoreAnchor(path, before, mode, exists)
	})
}

func replaceTOMLSections(body []byte, replacements map[string]string, removals map[string]bool) []byte {
	text := string(body)
	lines := strings.SplitAfter(text, "\n")
	var output strings.Builder
	seen := map[string]bool{}
	inSection := false
	current := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inSection && replacements[current] != "" && !seen[current] {
				output.WriteString(replacements[current])
				seen[current] = true
			}
			current = strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			inSection = true
			if matchesTOMLSection(current, removals) {
				continue
			}
			if replacement := replacements[current]; replacement != "" {
				if !seen[current] {
					output.WriteString(replacement)
					seen[current] = true
				}
				continue
			}
			if replacementForTOMLSection(current, replacements) != "" {
				continue
			}
			output.WriteString(line)
			continue
		}
		if inSection && matchesTOMLSection(current, removals) {
			continue
		}
		if inSection && replacementForTOMLSection(current, replacements) != "" {
			continue
		}
		output.WriteString(line)
	}
	if inSection && replacements[current] != "" && !seen[current] {
		output.WriteString(replacements[current])
		seen[current] = true
	}
	for name, replacement := range replacements {
		if !seen[name] {
			if output.Len() > 0 && !strings.HasSuffix(output.String(), "\n") {
				output.WriteByte('\n')
			}
			output.WriteString(replacement)
			seen[name] = true
		}
	}
	return []byte(output.String())
}

func matchesTOMLSection(section string, candidates map[string]bool) bool {
	for candidate, enabled := range candidates {
		if enabled && (section == candidate || strings.HasPrefix(section, candidate+".")) {
			return true
		}
	}
	return false
}

func replacementForTOMLSection(section string, replacements map[string]string) string {
	if replacement := replacements[section]; replacement != "" {
		return replacement
	}
	for candidate, replacement := range replacements {
		if replacement != "" && strings.HasPrefix(section, candidate+".") {
			return replacement
		}
	}
	return ""
}

func loadJSONObject(path string) (map[string]any, []byte, os.FileMode, bool, error) {
	body, mode, exists, err := readOptionalFile(path)
	if err != nil {
		return nil, nil, 0, false, err
	}
	root := map[string]any{}
	if exists && len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &root); err != nil {
			return nil, nil, 0, false, errors.New("existing JSON config cannot be parsed")
		}
		if root == nil {
			return nil, nil, 0, false, errors.New("existing JSON config root is not an object")
		}
	}
	return root, body, mode, exists, nil
}

func readOptionalFile(path string) ([]byte, os.FileMode, bool, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0600, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, errors.New("MCP config must be a regular file")
	}
	return body, info.Mode(), true, nil
}

func writeAnchor(path, dataDir string, before []byte, mode os.FileMode, exists bool, encoded []byte, restore func() error) (AnchorApplyResult, error) {
	if bytes.Equal(before, encoded) && exists {
		return AnchorApplyResult{ConfigPath: path, Restore: func() error { return nil }}, nil
	}
	backup := ""
	if exists {
		root := filepath.Join(dataDir, "backups", "mcp-anchors")
		if err := os.MkdirAll(root, 0700); err != nil {
			return AnchorApplyResult{}, err
		}
		backup = filepath.Join(root, time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+filepath.Base(path))
		if err := os.WriteFile(backup, before, 0600); err != nil {
			return AnchorApplyResult{}, err
		}
	}
	if err := atomicWriteMCPM(path, encoded, 0600); err != nil {
		return AnchorApplyResult{}, err
	}
	return AnchorApplyResult{ConfigPath: path, BackupPath: backup, Restore: restore}, nil
}

func restoreAnchor(path string, before []byte, mode os.FileMode, exists bool) error {
	if !exists {
		if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else {
			return err
		}
	}
	return atomicWriteMCPM(path, before, mode.Perm())
}

// CleanupClaudeLegacy removes only the dead settings mcpServers blocks. It is
// intentionally separate from anchor delivery so phase 3 can gate it.
func CleanupClaudeLegacy(home, dataDir string) ([]string, error) {
	paths := []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".claude", "settings.local.json")}
	removed := make([]string, 0, len(paths))
	for index, path := range paths {
		root, before, mode, exists, err := loadJSONObject(path)
		if err != nil {
			return removed, err
		}
		if !exists {
			continue
		}
		key := "mcpServers"
		if index == 1 {
			key = "mcp_servers"
		}
		if _, ok := root[key]; !ok {
			continue
		}
		delete(root, key)
		encoded, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			return removed, err
		}
		encoded = append(encoded, '\n')
		if _, err := writeAnchor(path, dataDir, before, mode, exists, encoded, func() error { return restoreAnchor(path, before, mode, exists) }); err != nil {
			return removed, err
		}
		removed = append(removed, path)
	}
	return removed, nil
}

// RemoveToolHubSharedPlugin moves the old Codex plugin aside after verifying it
// carries the ToolHub author marker. The caller can archive the returned path.
func RemoveToolHubSharedPlugin(home, backupRoot string) (string, error) {
	path := filepath.Join(home, ".codex", ".tmp", "plugins", "plugins", "shared-mcp")
	metadataPath := filepath.Join(path, "plugin.json")
	body, err := os.ReadFile(metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		return "", errors.New("ToolHub shared MCP plugin metadata is invalid")
	}
	author := ""
	switch typed := value["author"].(type) {
	case string:
		author = typed
	case map[string]any:
		author = fmt.Sprint(typed["name"])
	}
	if strings.TrimSpace(fmt.Sprint(value["name"])) != "shared-mcp" || !strings.EqualFold(strings.TrimSpace(author), "ToolHub") {
		return "", errors.New("refusing to remove a non-ToolHub Codex plugin")
	}
	if backupRoot == "" {
		backupRoot = filepath.Join(filepath.Dir(path), "toolhub-archive-"+uuid.NewString())
	}
	if err := os.MkdirAll(filepath.Dir(backupRoot), 0700); err != nil {
		return "", err
	}
	if err := os.Rename(path, backupRoot); err != nil {
		return "", err
	}
	return backupRoot, nil
}
