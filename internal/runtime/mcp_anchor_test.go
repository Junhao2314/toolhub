package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestClaudeAnchorPreservesUnrelatedContentAndRepairsManagedEntry(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()
	path := filepath.Join(home, ".claude.json")
	body := `{"theme":"dark","projects":{"keep":{"allowed":true}},"mcpServers":{"local":{"command":"local"},"toolhub-claude":{"command":"edited"}}}`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyRuntimeMCPAnchor(home, dataDir, "claude", managedClaudeProfile, true)
	if err != nil {
		t.Fatal(err)
	}
	root := readJSONMap(t, path)
	if root["theme"] != "dark" {
		t.Fatalf("unrelated top-level content changed: %+v", root)
	}
	servers := root["mcpServers"].(map[string]any)
	if _, ok := servers["local"]; !ok {
		t.Fatalf("unrelated MCP entry was removed: %+v", servers)
	}
	anchor := servers["toolhub-claude"].(map[string]any)
	if anchor["command"] != "mcpm" || !equalStringSlice(anchor["args"], []string{"profile", "run", managedClaudeProfile}) {
		t.Fatalf("managed anchor was not repaired: %+v", anchor)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("Claude config mode = %v err=%v", info, err)
	}
	if info, err := os.Stat(result.BackupPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("Claude backup mode = %v err=%v", info, err)
	}
	if _, err := ApplyRuntimeMCPAnchor(home, dataDir, "claude", managedClaudeProfile, false); err != nil {
		t.Fatal(err)
	}
	servers = readJSONMap(t, path)["mcpServers"].(map[string]any)
	if _, ok := servers["toolhub-claude"]; ok {
		t.Fatalf("disabled managed anchor remains: %+v", servers)
	}
	if _, ok := servers["local"]; !ok {
		t.Fatalf("disable removed an unrelated entry: %+v", servers)
	}
}

func TestDisabledAnchorDoesNotCreateMissingConfig(t *testing.T) {
	home := t.TempDir()
	if _, err := ApplyRuntimeMCPAnchor(home, t.TempDir(), "claude", managedClaudeProfile, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("disabled Claude anchor created a file: %v", err)
	}
	if _, err := ApplyRuntimeMCPAnchor(home, t.TempDir(), "codex", managedCodexProfile, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("disabled Codex anchor created a file: %v", err)
	}
}

func TestCodexAnchorReplacesOwnedSectionAndLegacySubsections(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := `model_provider = "custom"
model = "gpt-test"

[model_providers.custom]
base_url = "https://example.test"

[mcp_servers.all-mcp]
command = "mcpm"
args = ["profile", "run", "all-mcp"]
[mcp_servers.all-mcp.env]
TOKEN = "legacy"

[mcp_servers.toolhub-codex]
command = "edited"
[mcp_servers.toolhub-codex.env]
TOKEN = "drift"

[mcp_servers.local]
command = "local"
args = []
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRuntimeMCPAnchor(home, t.TempDir(), "codex", managedCodexProfile, true); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	if strings.Contains(text, "all-mcp") || strings.Contains(text, "TOKEN =") {
		t.Fatalf("legacy or managed nested section remains:\n%s", text)
	}
	var root map[string]any
	if err := toml.Unmarshal(updated, &root); err != nil {
		t.Fatalf("updated Codex config is invalid TOML: %v\n%s", err, text)
	}
	if root["model_provider"] != "custom" || root["model"] != "gpt-test" {
		t.Fatalf("model settings changed: %+v", root)
	}
	servers := root["mcp_servers"].(map[string]any)
	if _, ok := servers["local"]; !ok {
		t.Fatalf("local MCP entry was removed: %+v", servers)
	}
	anchor := servers[managedCodexProfile].(map[string]any)
	if anchor["command"] != "mcpm" || !equalStringSlice(anchor["args"], []string{"profile", "run", managedCodexProfile}) {
		t.Fatalf("Codex anchor is wrong: %+v", anchor)
	}
	if timeout, ok := integerValue(anchor["startup_timeout_sec"]); !ok || timeout != managedCodexStartupTimeoutSeconds {
		t.Fatalf("Codex startup timeout = %v; want %d", anchor["startup_timeout_sec"], managedCodexStartupTimeoutSeconds)
	}
}

func TestInspectCodexAnchorRequiresManagedStartupTimeout(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[mcp_servers.toolhub-codex]
command = "mcpm"
args = ["profile", "run", "toolhub-codex"]
startup_timeout_sec = 10
`), 0600); err != nil {
		t.Fatal(err)
	}
	before := InspectRuntimeMCPAnchor(home, "codex")
	if before["present"] != true || before["valid"] != false {
		t.Fatalf("unexpected pre-repair observation: %+v", before)
	}
	if _, err := ApplyRuntimeMCPAnchor(home, t.TempDir(), "codex", managedCodexProfile, true); err != nil {
		t.Fatal(err)
	}
	after := InspectRuntimeMCPAnchor(home, "codex")
	if after["valid"] != true {
		t.Fatalf("repaired anchor is not valid: %+v", after)
	}
}

func TestCodexAnchorPreservesUnrecognizedAllMCPSection(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := `[mcp_servers.all-mcp]
command = "custom-relay"
args = ["keep"]
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRuntimeMCPAnchor(home, t.TempDir(), "codex", managedCodexProfile, true); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "custom-relay") || !strings.Contains(string(updated), "[mcp_servers.all-mcp]") {
		t.Fatalf("unrecognized all-mcp section was removed:\n%s", updated)
	}
	if _, err := ApplyRuntimeMCPAnchor(home, t.TempDir(), "codex", managedCodexProfile, false); err != nil {
		t.Fatal(err)
	}
	updated, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "custom-relay") || strings.Contains(string(updated), "toolhub-codex") {
		t.Fatalf("disable changed an unowned relay:\n%s", updated)
	}
}

func TestCleanupClaudeLegacyAndArchiveToolHubPlugin(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	local := filepath.Join(home, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"env":{"KEEP":"yes"},"model":"keep","mcpServers":{"legacy":{"command":"legacy"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte(`{"permissions":{"allow":["Read"]},"mcp_servers":{"all-mcp":{}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	removed, err := CleanupClaudeLegacy(home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed files = %v", removed)
	}
	settingsRoot := readJSONMap(t, settings)
	if _, ok := settingsRoot["mcpServers"]; ok || settingsRoot["model"] != "keep" {
		t.Fatalf("settings cleanup changed unrelated content: %+v", settingsRoot)
	}
	localRoot := readJSONMap(t, local)
	if _, ok := localRoot["mcp_servers"]; ok {
		t.Fatalf("legacy snake_case block remains: %+v", localRoot)
	}
	permissions, _ := json.Marshal(localRoot["permissions"])
	if !strings.Contains(string(permissions), "Read") {
		t.Fatalf("permissions were lost: %+v", localRoot)
	}

	plugin := filepath.Join(home, ".codex", ".tmp", "plugins", "plugins", "shared-mcp")
	if err := os.MkdirAll(plugin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "plugin.json"), []byte(`{"name":"shared-mcp","author":{"name":"ToolHub"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "shared-mcp")
	moved, err := RemoveToolHubSharedPlugin(home, archive)
	if err != nil {
		t.Fatal(err)
	}
	if moved != archive {
		t.Fatalf("archive path = %q; want %q", moved, archive)
	}
	if _, err := os.Stat(plugin); !os.IsNotExist(err) {
		t.Fatalf("plugin was not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archive, "plugin.json")); err != nil {
		t.Fatalf("archive is incomplete: %v", err)
	}
}
