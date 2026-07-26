package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPackageDiscoveredSkillRejectsProtectedAndSymlinkedContent(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	protected := filepath.Join(paths.RuntimeRoots["codex"], ".system", "example")
	writeDiscoveredSkill(t, protected)
	if _, err := PackageDiscoveredSkill(paths, "codex", protected, "ignored"); err == nil {
		t.Fatal("protected skill was accepted")
	}

	target := filepath.Join(paths.RuntimeRoots["codex"], "linked")
	writeDiscoveredSkill(t, target)
	if err := os.Symlink(filepath.Join(target, "SKILL.md"), filepath.Join(target, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := PackageDiscoveredSkill(paths, "codex", target, "ignored"); err == nil {
		t.Fatal("symlinked skill was accepted")
	}
}

func TestMarkAdoptedSkillWritesMarkerOnlyAfterMatchingHash(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	target := filepath.Join(paths.RuntimeRoots["claude"], "example")
	writeDiscoveredSkill(t, target)
	pkg, err := PackageDiscoveredSkill(paths, "claude", target, scanSkillHash(t, target))
	if err != nil {
		t.Fatal(err)
	}
	if err := MarkAdoptedSkill(paths, "claude", target, "wrong", AdoptedSkillMarker{SkillID: "skill", VersionID: "version"}); err == nil {
		t.Fatal("hash mismatch was accepted")
	}
	if fileExists(filepath.Join(target, ".toolhub-managed.json")) {
		t.Fatal("marker was written after failed validation")
	}
	if err := MarkAdoptedSkill(paths, "claude", target, pkg.SHA256, AdoptedSkillMarker{SkillID: "skill", VersionID: "version"}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(target, ".toolhub-managed.json")) {
		t.Fatal("marker was not written")
	}
	discoveries, _, _, _, err := scanSkillRoot(filepath.Dir(target))
	if err != nil || len(discoveries) != 1 || discoveries[0].(map[string]any)["sha256"] != pkg.SHA256 {
		t.Fatalf("managed marker changed discovery hash: %v %+v", err, discoveries)
	}
}

func writeDiscoveredSkill(t *testing.T, target string) {
	t.Helper()
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: Example\nlicense: MIT\n---\nBody\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func scanSkillHash(t *testing.T, target string) string {
	t.Helper()
	pkg, err := PackageDiscoveredSkill(DefaultPaths(filepath.Dir(filepath.Dir(filepath.Dir(target)))), "claude", target, "missing")
	if err == nil {
		return pkg.SHA256
	}
	// The helper above intentionally cannot know the expected hash. Read it through the scanner used by inventory.
	root := filepath.Dir(target)
	skills, _, _, _, scanErr := scanSkillRoot(root)
	if scanErr != nil || len(skills) != 1 {
		t.Fatalf("scan skill root: %v %+v", scanErr, skills)
	}
	return skills[0].(map[string]any)["sha256"].(string)
}
