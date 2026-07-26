package runtime

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/skills"
)

func sharedSkillsTestSource(t *testing.T, root string, kinds []string) SharedSourceConfig {
	t.Helper()
	skillsRoot := filepath.Join(root, "shared", "skills")
	manifest := filepath.Join(root, "shared", "mcp", "servers.json")
	if err := os.MkdirAll(skillsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(manifest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"servers":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	consumers := make(map[string]SharedConsumerConfig, len(kinds))
	for _, kind := range kinds {
		consumers[kind] = SharedConsumerConfig{SkillsPath: filepath.Join(root, "consumers", kind, "skills")}
	}
	return SharedSourceConfig{Name: "test-shared", Mode: SharedModeObserved, SkillsRoot: skillsRoot, MCPManifest: manifest, AllowedSkillRoots: []string{skillsRoot}, Consumers: consumers}
}

func writeSharedSkill(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("---\nname: Example\nlicense: MIT\n---\nBody\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// A Skill that fails package validation must stay blocked on its own row with the real
// reason, while the source status is capped at drift instead of escalating to blocked.
func TestScanSharedSourceIsolatesUnscannableSkill(t *testing.T) {
	root := t.TempDir()
	source := sharedSkillsTestSource(t, root, []string{domain.RuntimeCodex})
	writeSharedSkill(t, filepath.Join(source.SkillsRoot, "healthy"))
	oversized := filepath.Join(source.SkillsRoot, "toolarge")
	if err := os.MkdirAll(oversized, 0755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= skills.DefaultLimits.MaxFiles; index++ {
		if err := os.WriteFile(filepath.Join(oversized, fmt.Sprintf("file-%04d.txt", index)), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	inventory, err := ScanSharedSource(source, filepath.Join(root, "data"), bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]domain.SharedSkillInventory{}
	for _, skill := range inventory.Skills {
		byName[skill.Name] = skill
	}
	blocked := byName["toolarge"]
	if blocked.State != "blocked" || !strings.Contains(blocked.LastError, fmt.Sprintf("more than %d files", skills.DefaultLimits.MaxFiles)) {
		t.Fatalf("oversized skill state=%q lastError=%q", blocked.State, blocked.LastError)
	}
	if healthy := byName["healthy"]; healthy.State != "observed" || healthy.SHA256 == "" {
		t.Fatalf("healthy skill state=%q sha=%q", healthy.State, healthy.SHA256)
	}
	if inventory.Status == "blocked" || inventory.Status == "failed" {
		t.Fatalf("one unscannable skill escalated the source status to %q", inventory.Status)
	}
	if !strings.Contains(inventory.LastError, "toolarge: package validation failed:") {
		t.Fatalf("source error does not name the blocked skill: %q", inventory.LastError)
	}
}
