package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/domain"
	"gopkg.in/yaml.v3"
)

func TestSharedSkillReconcileLinksAllConsumersAndRemovesOwnedStaleLinks(t *testing.T) {
	root := t.TempDir()
	source := sharedSkillsTestSource(t, root, []string{domain.RuntimeCodex, domain.RuntimeClaude, domain.RuntimeHermes, domain.RuntimeGrok, domain.RuntimeOpenClaw})
	writeSharedSkill(t, filepath.Join(source.SkillsRoot, "example"))
	reconciler := SharedReconciler{DataDir: filepath.Join(root, "data"), Sources: []SharedSourceConfig{source}, FingerprintKey: bytes.Repeat([]byte{3}, 32)}

	first, err := reconciler.Reconcile(context.Background(), SharedSyncRequest{SourceName: source.Name, Scopes: []string{"skills"}})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || len(first.Conflicts) != 0 {
		t.Fatalf("unexpected first reconcile result: %+v", first)
	}
	for kind, consumer := range source.Consumers {
		target := filepath.Join(consumer.SkillsPath, "example")
		info, err := os.Lstat(target)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s link was not created: info=%v err=%v", kind, info, err)
		}
		link, err := os.Readlink(target)
		if err != nil || !samePath(cleanLinkTarget(target, link), filepath.Join(source.SkillsRoot, "example")) {
			t.Fatalf("%s link target = %q err=%v", kind, link, err)
		}
	}

	second, err := reconciler.Reconcile(context.Background(), SharedSyncRequest{SourceName: source.Name, Scopes: []string{"skills"}})
	if err != nil || second.Changed || len(second.Actions) != 0 || len(second.Conflicts) != 0 {
		t.Fatalf("reconcile was not idempotent: %+v err=%v", second, err)
	}

	if err := os.RemoveAll(filepath.Join(source.SkillsRoot, "example")); err != nil {
		t.Fatal(err)
	}
	stale, err := reconciler.Reconcile(context.Background(), SharedSyncRequest{SourceName: source.Name, Scopes: []string{"skills"}})
	if err != nil || !stale.Changed || len(stale.Conflicts) != 0 {
		t.Fatalf("stale link reconcile failed: %+v err=%v", stale, err)
	}
	for kind, consumer := range source.Consumers {
		if _, err := os.Lstat(filepath.Join(consumer.SkillsPath, "example")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s stale owned link remains: %v", kind, err)
		}
	}
}

func TestSharedSkillReconcilePreservesUnownedAndModifiedTargets(t *testing.T) {
	root := t.TempDir()
	source := sharedSkillsTestSource(t, root, []string{domain.RuntimeCodex, domain.RuntimeClaude})
	writeSharedSkill(t, filepath.Join(source.SkillsRoot, "example"))
	unowned := filepath.Join(source.Consumers[domain.RuntimeCodex].SkillsPath, "example")
	if err := os.MkdirAll(unowned, 0755); err != nil {
		t.Fatal(err)
	}
	reconciler := SharedReconciler{DataDir: filepath.Join(root, "data"), Sources: []SharedSourceConfig{source}, FingerprintKey: bytes.Repeat([]byte{4}, 32)}
	first, err := reconciler.Reconcile(context.Background(), SharedSyncRequest{SourceName: source.Name, Scopes: []string{"skills"}})
	if err != nil || len(first.Conflicts) != 1 {
		t.Fatalf("unowned target was not reported as one conflict: %+v err=%v", first, err)
	}
	if info, err := os.Stat(unowned); err != nil || !info.IsDir() {
		t.Fatalf("unowned directory was modified: info=%v err=%v", info, err)
	}

	claudeTarget := filepath.Join(source.Consumers[domain.RuntimeClaude].SkillsPath, "example")
	if err := os.RemoveAll(filepath.Join(source.SkillsRoot, "example")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(claudeTarget); err != nil {
		t.Fatal(err)
	}
	alternate := filepath.Join(root, "local-skill")
	if err := os.MkdirAll(alternate, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alternate, claudeTarget); err != nil {
		t.Fatal(err)
	}
	second, err := reconciler.Reconcile(context.Background(), SharedSyncRequest{SourceName: source.Name, Scopes: []string{"skills"}})
	if err != nil || len(second.Conflicts) == 0 {
		t.Fatalf("modified stale link was not preserved as conflict: %+v err=%v", second, err)
	}
	actual, err := os.Readlink(claudeTarget)
	if err != nil || !samePath(cleanLinkTarget(claudeTarget, actual), alternate) {
		t.Fatalf("modified stale link was overwritten: target=%q err=%v", actual, err)
	}
}

func TestSharedSkillReconcileDoesNotAdoptBlockedSource(t *testing.T) {
	root := t.TempDir()
	source := sharedSkillsTestSource(t, root, []string{domain.RuntimeCodex})
	blockedSource := filepath.Join(source.SkillsRoot, "invalid")
	if err := os.MkdirAll(blockedSource, 0755); err != nil {
		t.Fatal(err)
	}
	consumerRoot := source.Consumers[domain.RuntimeCodex].SkillsPath
	if err := os.MkdirAll(consumerRoot, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(consumerRoot, "invalid")
	if err := os.Symlink(blockedSource, target); err != nil {
		t.Fatal(err)
	}
	reconciler := SharedReconciler{DataDir: filepath.Join(root, "data"), Sources: []SharedSourceConfig{source}, FingerprintKey: bytes.Repeat([]byte{9}, 32)}

	result, err := reconciler.Reconcile(context.Background(), SharedSyncRequest{SourceName: source.Name, Scopes: []string{"skills"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range result.Actions {
		if strings.Contains(action, "invalid") {
			t.Fatalf("blocked source produced reconciliation action %q", action)
		}
	}
	state, err := loadSharedState(reconciler.DataDir, source.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, managed := state.Links[domain.RuntimeCodex]["invalid"]; managed {
		t.Fatal("blocked source link was adopted")
	}
	actual, err := os.Readlink(target)
	if err != nil || !samePath(cleanLinkTarget(target, actual), blockedSource) {
		t.Fatalf("unowned blocked source link changed: target=%q err=%v", actual, err)
	}
	if len(result.Source.Skills) != 1 || result.Source.Skills[0].State != "blocked" {
		t.Fatalf("blocked source was not reported: %+v", result.Source.Skills)
	}
}

func TestSharedMCPReconcileRendersAllFormatsAndPreservesLocalEntries(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	source := DefaultSharedSource(DefaultPaths(home))
	source.Name = "test-shared"
	source.Mode = SharedModeManaged
	source.AutoSync = false
	source.Consumers[domain.RuntimeHermes] = SharedConsumerConfig{
		SkillsPath: filepath.Join(home, ".hermes", "skills"),
		MCPPath:    filepath.Join(home, ".hermes", "config.yaml"),
		MCPFormat:  MCPFormatHermesYAML,
	}
	source.Consumers[domain.RuntimeGrok] = SharedConsumerConfig{
		SkillsPath:  filepath.Join(home, ".grok", "skills"),
		MCPInherits: domain.RuntimeClaude,
	}
	source.Consumers[domain.RuntimeOpenClaw] = SharedConsumerConfig{
		SkillsPath: filepath.Join(home, ".openclaw", "workspace", "skills"),
		MCPPath:    filepath.Join(home, ".openclaw", "workspace", "config", "mcporter.json"),
		MCPFormat:  MCPFormatOpenClawJSON,
	}
	writeTestFile(t, source.MCPManifest, `{
  "version": 1,
  "servers": {
    "stdio": {"enabled": true, "transport": "stdio", "command": "npx", "args": ["-y", "example"], "env": {"TOKEN": "sensitive-env-value"}},
    "remote": {"enabled": true, "transport": "streamable-http", "url": "https://example.test/mcp", "headers": {"authorization": "Bearer sensitive-header-value"}},
    "disabled": {"enabled": false, "transport": "stdio", "command": "disabled"}
  }
}`, 0600)
	if err := os.MkdirAll(source.SkillsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	claude := source.Consumers[domain.RuntimeClaude].MCPPath
	hermes := source.Consumers[domain.RuntimeHermes].MCPPath
	openclaw := source.Consumers[domain.RuntimeOpenClaw].MCPPath
	writeTestFile(t, claude, `{"theme":"dark","mcpServers":{"local":{"command":"local-claude"}}}`, 0600)
	writeTestFile(t, hermes, "theme: dark\nmcp_servers:\n  task-trellis:\n    command: local-task\n  acemcp:\n    command: local-acemcp\n", 0600)
	writeTestFile(t, openclaw, `{"other":{"keep":true},"mcpServers":{"local":{"type":"stdio","command":"local-openclaw"}}}`, 0600)

	reconciler := SharedReconciler{DataDir: filepath.Join(root, "data"), Sources: []SharedSourceConfig{source}, FingerprintKey: bytes.Repeat([]byte{5}, 32)}
	result, err := reconciler.Reconcile(context.Background(), SharedSyncRequest{SourceName: source.Name, Scopes: []string{"mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(result.Conflicts) != 0 {
		t.Fatalf("unexpected MCP reconcile result: %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sensitive-env-value", "sensitive-header-value"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("shared sync result leaked %q: %s", secret, encoded)
		}
	}

	for kind, consumer := range source.Consumers {
		if consumer.MCPPath == "" {
			continue
		}
		info, err := os.Stat(consumer.MCPPath)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s MCP file mode = %v err=%v", kind, info, err)
		}
	}
	claudeRoot := readJSONMap(t, claude)
	if claudeRoot["theme"] != "dark" {
		t.Fatalf("Claude top-level field was lost: %+v", claudeRoot)
	}
	assertRenderedServerNames(t, claudeRoot, "local", "remote", "stdio")
	codexPath := source.Consumers[domain.RuntimeCodex].MCPPath
	assertRenderedServerNames(t, readJSONMap(t, codexPath), "remote", "stdio")
	if info, err := os.Stat(filepath.Join(filepath.Dir(codexPath), "plugin.json")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("Codex plugin metadata missing or insecure: info=%v err=%v", info, err)
	}
	openclawRoot := readJSONMap(t, openclaw)
	if _, ok := openclawRoot["other"]; !ok {
		t.Fatalf("OpenClaw top-level field was lost: %+v", openclawRoot)
	}
	assertRenderedServerNames(t, openclawRoot, "local", "remote", "stdio")
	var hermesRoot map[string]any
	hermesBody, err := os.ReadFile(hermes)
	if err != nil || yaml.Unmarshal(hermesBody, &hermesRoot) != nil {
		t.Fatalf("read Hermes output: %v", err)
	}
	hermesServers, ok := hermesRoot["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("Hermes mcp_servers missing: %+v", hermesRoot)
	}
	for _, name := range []string{"task-trellis", "acemcp", "remote", "stdio"} {
		if _, ok := hermesServers[name]; !ok {
			t.Fatalf("Hermes entry %q was not preserved/rendered: %+v", name, hermesServers)
		}
	}
	if _, ok := hermesServers["disabled"]; ok {
		t.Fatal("disabled shared server was rendered")
	}

	second, err := reconciler.Reconcile(context.Background(), SharedSyncRequest{SourceName: source.Name, Scopes: []string{"mcp"}})
	if err != nil || second.Changed || len(second.Actions) != 0 || len(second.Conflicts) != 0 {
		t.Fatalf("MCP reconcile was not idempotent: %+v err=%v", second, err)
	}
}

func TestSharedMCPWriteDetectsConcurrentDocumentChange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "settings.json")
	writeTestFile(t, path, `{"mcpServers":{}}`, 0600)
	document, err := loadSharedMCPDocument(path, MCPFormatClaudeSettingsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.setEntry("managed", map[string]any{"command": "managed"}); err != nil {
		t.Fatal(err)
	}
	external := `{"localEdit":true,"mcpServers":{}}`
	writeTestFile(t, path, external, 0600)
	if err := writeSharedMCPDocument(document, filepath.Join(root, "data"), "source", domain.RuntimeClaude); !errors.Is(err, errSharedMCPConcurrentChange) {
		t.Fatalf("concurrent edit error = %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != external {
		t.Fatalf("concurrent local edit was overwritten: %q err=%v", body, err)
	}
}

func TestSharedMCPWriteRequiresPrivateManifestPermissions(t *testing.T) {
	root := t.TempDir()
	source := sharedSkillsTestSource(t, root, nil)
	source.Consumers = map[string]SharedConsumerConfig{
		domain.RuntimeClaude: {MCPPath: filepath.Join(root, "claude.json"), MCPFormat: MCPFormatClaudeSettingsJSON},
	}
	writeTestFile(t, source.MCPManifest, `{"servers":{"example":{"enabled":true,"transport":"stdio","command":"example"}}}`, 0644)
	reconciler := SharedReconciler{DataDir: filepath.Join(root, "data"), Sources: []SharedSourceConfig{source}, FingerprintKey: bytes.Repeat([]byte{8}, 32)}
	dryRun, err := reconciler.Reconcile(context.Background(), SharedSyncRequest{SourceName: source.Name, Scopes: []string{"mcp"}, DryRun: true})
	if err != nil || dryRun.Source.Status != "blocked" {
		t.Fatalf("dry-run did not report insecure manifest: %+v err=%v", dryRun, err)
	}
	if _, err := reconciler.Reconcile(context.Background(), SharedSyncRequest{SourceName: source.Name, Scopes: []string{"mcp"}}); err == nil || !strings.Contains(err.Error(), "requires 0600") {
		t.Fatalf("managed write with insecure manifest error = %v", err)
	}
	if _, err := os.Stat(source.Consumers[domain.RuntimeClaude].MCPPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumer file was written despite insecure manifest: %v", err)
	}
}

func TestSharedLockSerializesReconciliation(t *testing.T) {
	dataDir := t.TempDir()
	release, err := acquireSharedLock(context.Background(), dataDir, "source")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := acquireSharedLock(ctx, dataDir, "source"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock acquisition error = %v", err)
	}
}

func sharedSkillsTestSource(t *testing.T, root string, kinds []string) SharedSourceConfig {
	t.Helper()
	skillsRoot := filepath.Join(root, "shared", "skills")
	manifest := filepath.Join(root, "shared", "mcp", "servers.json")
	if err := os.MkdirAll(skillsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, manifest, `{"servers":{}}`, 0600)
	consumers := make(map[string]SharedConsumerConfig, len(kinds))
	for _, kind := range kinds {
		consumers[kind] = SharedConsumerConfig{SkillsPath: filepath.Join(root, "consumers", kind, "skills")}
	}
	return SharedSourceConfig{Name: "test-shared", Mode: SharedModeManaged, SkillsRoot: skillsRoot, MCPManifest: manifest, AllowedSkillRoots: []string{skillsRoot}, Consumers: consumers}
}

func writeSharedSkill(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(path, "SKILL.md"), "---\nname: Example\nlicense: MIT\n---\nBody\n", 0644)
}

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

func assertRenderedServerNames(t *testing.T, root map[string]any, names ...string) {
	t.Helper()
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing: %+v", root)
	}
	for _, name := range names {
		if _, ok := servers[name]; !ok {
			t.Fatalf("server %q missing: %+v", name, servers)
		}
	}
	if _, ok := servers["disabled"]; ok {
		t.Fatal("disabled shared server was rendered")
	}
}
