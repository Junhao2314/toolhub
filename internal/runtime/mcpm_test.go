package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
)

func TestScanMCPMSeedsEachRuntimeUntilItsManagedProfileExists(t *testing.T) {
	home := t.TempDir()
	path := MCPMStorePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := `{
  "legacy": {"name":"legacy","command":"npx","args":["legacy"],"env":{"TOKEN":"not-in-inventory"},"profile_tags":["all-mcp"]},
  "codex": {"name":"codex","command":"npx","args":["codex"],"profile_tags":["toolhub-codex"]},
  "unrelated": {"name":"unrelated","command":"npx","args":["other"],"profile_tags":["other"]}
}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanMCPM(home, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Servers) != 2 {
		t.Fatalf("server count = %d; want 2", len(scan.Servers))
	}
	byName := map[string]domain.MCPDescriptor{}
	for _, descriptor := range scan.Servers {
		byName[descriptor.Name] = descriptor
	}
	if got := byName["legacy"].TargetRuntimes; len(got) != 1 || got[0] != domain.RuntimeClaude {
		t.Fatalf("legacy targets = %v; want claude fallback only", got)
	}
	if got := byName["codex"].TargetRuntimes; len(got) != 1 || got[0] != domain.RuntimeCodex {
		t.Fatalf("codex targets = %v", got)
	}
	encoded, err := json.Marshal(scan.Servers)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("not-in-inventory")) {
		t.Fatalf("ordinary mcpm inventory leaked a secret: %s", encoded)
	}
	if values := scan.Secrets[byName["legacy"].Identity]; values.Env["TOKEN"] != "not-in-inventory" {
		t.Fatal("Agent-local capture values were not retained")
	}
}

func TestScanMCPMSeedsLegacyProfileOnlyIntoObservedNativeAnchor(t *testing.T) {
	home := t.TempDir()
	registry := MCPMStorePath(home)
	if err := os.MkdirAll(filepath.Dir(registry), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(`{
  "legacy": {"name":"legacy","command":"npx","args":["legacy"],"profile_tags":["all-mcp"]}
}`), 0600); err != nil {
		t.Fatal(err)
	}
	codexConfig := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(codexConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexConfig, []byte(`[mcp_servers.all-mcp]
command = "mcpm"
args = ["profile", "run", "all-mcp"]
`), 0600); err != nil {
		t.Fatal(err)
	}

	scan, err := ScanMCPM(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Servers) != 1 {
		t.Fatalf("server count = %d; want 1", len(scan.Servers))
	}
	if got := scan.Servers[0].TargetRuntimes; len(got) != 1 || got[0] != domain.RuntimeCodex {
		t.Fatalf("legacy targets = %v; want only the observed Codex anchor", got)
	}
}

func TestMCPMTargetRuntimesUsesLegacyAnchorsWhenAvailable(t *testing.T) {
	tests := []struct {
		name    string
		anchors map[string]bool
		want    string
	}{
		{name: "compatibility fallback", anchors: map[string]bool{}, want: "claude,codex"},
		{name: "Codex only", anchors: map[string]bool{domain.RuntimeCodex: true}, want: "codex"},
		{name: "Claude only", anchors: map[string]bool{domain.RuntimeClaude: true}, want: "claude"},
		{name: "both runtimes", anchors: map[string]bool{domain.RuntimeCodex: true, domain.RuntimeClaude: true}, want: "claude,codex"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mcpmTargetRuntimes([]string{"all-mcp"}, map[string]bool{}, test.anchors)
			if joined := strings.Join(got, ","); joined != test.want {
				t.Fatalf("legacy targets = %q; want %q", joined, test.want)
			}
		})
	}
}

func TestApplyMCPMPreservesUnownedStateAndSecuresBackups(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	home := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "agent-data")
	path := MCPMStorePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := `{
  "example": {"name":"example","command":"npx","args":["example"],"env":{"TOKEN":"old"},"profile_tags":["all-mcp"],"custom":{"keep":true}},
  "local": {"name":"local","command":"local","args":[],"profile_tags":["personal"]}
}`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	request := []protocol.MCPServerRef{{Name: "example", Transport: "stdio", Command: "npx", Args: []string{"example"}, EnvRefs: map[string]string{"TOKEN": "secret-1"}, HeaderRefs: map[string]string{}}}
	resolver := func(_ context.Context, id string) (string, error) {
		if id != "secret-1" {
			t.Fatalf("unexpected secret id %q", id)
		}
		return "old", nil
	}
	result, applied, err := ApplyMCPM(context.Background(), home, dataDir, domain.RuntimeCodex, managedCodexProfile, request, true, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.BackupPaths) != 1 || result.BackupPaths[0] != applied.BackupPath {
		t.Fatalf("backup paths = %v; apply backup = %q", result.BackupPaths, applied.BackupPath)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("registry mode = %v err=%v; want 0600", info, err)
	}
	if info, err := os.Stat(applied.BackupPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup mode = %v err=%v; want 0600", info, err)
	}
	root := readMCPMTestDocument(t, path)
	if !hasString(root["example"]["profile_tags"], "all-mcp") || !hasString(root["example"]["profile_tags"], managedCodexProfile) {
		t.Fatalf("profile tags were not merged: %+v", root["example"]["profile_tags"])
	}
	if custom, ok := root["example"]["custom"].(map[string]any); !ok || custom["keep"] != true {
		t.Fatalf("owned entry unknown fields were lost: %+v", root["example"])
	}
	if !hasString(root["local"]["profile_tags"], "personal") {
		t.Fatalf("unowned entry changed: %+v", root["local"])
	}
	second, secondApply, err := ApplyMCPM(context.Background(), home, dataDir, domain.RuntimeCodex, managedCodexProfile, request, true, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if second.Method != "mcpm-idempotent" || len(second.BackupPaths) != 0 || secondApply.BackupPath != "" {
		t.Fatalf("second apply was not idempotent: result=%+v apply=%+v", second, secondApply)
	}
	if err := applied.Restore(); err != nil {
		t.Fatal(err)
	}
	if restored := readMCPMTestDocument(t, path); hasString(restored["example"]["profile_tags"], managedCodexProfile) {
		t.Fatalf("restore left the managed profile tag: %+v", restored["example"])
	}
}

func TestApplyMCPMRejectsUnownedNameConflict(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	home := t.TempDir()
	path := MCPMStorePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"example":{"name":"example","command":"local","args":[],"profile_tags":["personal"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	request := []protocol.MCPServerRef{{Name: "example", Transport: "stdio", Command: "central", Args: []string{}, EnvRefs: map[string]string{}, HeaderRefs: map[string]string{}}}
	if _, _, err := ApplyMCPM(context.Background(), home, t.TempDir(), domain.RuntimeCodex, managedCodexProfile, request, true, nil); err == nil {
		t.Fatal("unowned conflicting definition was overwritten")
	}
	if got := readMCPMTestDocument(t, path)["example"]["command"]; got != "local" {
		t.Fatalf("conflicting entry changed to %v", got)
	}
}

func TestApplyMCPRestoresMCPMWhenAnchorWriteFails(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	home := t.TempDir()
	dataDir := t.TempDir()
	registry := MCPMStorePath(home)
	if err := os.MkdirAll(filepath.Dir(registry), 0700); err != nil {
		t.Fatal(err)
	}
	before := []byte("{}\n")
	if err := os.WriteFile(registry, before, 0600); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudePath, []byte(`{"mcpServers":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	request := protocol.ApplyMCPPayload{Runtime: domain.RuntimeClaude, MCPMProfile: managedClaudeProfile, DesiredHash: "desired", Enabled: true,
		Servers: []protocol.MCPServerRef{{Name: "example", Transport: "stdio", Command: "example", Args: []string{}, EnvRefs: map[string]string{}, HeaderRefs: map[string]string{}}}}
	if _, err := ApplyMCP(context.Background(), DefaultPaths(home), dataDir, request, nil); err == nil {
		t.Fatal("invalid Claude anchor unexpectedly succeeded")
	}
	after, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("mcpm registry was not restored: %q", after)
	}
}

func readMCPMTestDocument(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
