package runtime

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Junhao2314/toolhub/internal/skills"
)

func TestDeployRefusesUnmanagedTarget(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	skillRoot := filepath.Join(home, ".codex", "skills")
	target := filepath.Join(skillRoot, "example")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	archive := testSkillZIP(t)
	pkg, err := skills.ScanZIP(archive, skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	deployer := Deployer{DataDir: filepath.Join(root, "data"), Paths: DefaultPaths(home)}
	_, err = deployer.Deploy(DeployRequest{Runtime: "codex", SkillSlug: "example", VersionID: "v1", SHA256: pkg.SHA256, Enabled: true, Artifact: pkg.CanonicalZIP})
	if err == nil {
		t.Fatal("unmanaged target was overwritten")
	}
}

func TestDeployIsIdempotent(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	archive := testSkillZIP(t)
	pkg, err := skills.ScanZIP(archive, skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	deployer := Deployer{DataDir: filepath.Join(root, "data"), Paths: DefaultPaths(home)}
	request := DeployRequest{Runtime: "codex", SkillSlug: "example", VersionID: "v1", SHA256: pkg.SHA256, Enabled: true, Artifact: pkg.CanonicalZIP}
	first, err := deployer.Deploy(request)
	if err != nil || !first.Changed {
		t.Fatalf("first deploy failed: %+v %v", first, err)
	}
	second, err := deployer.Deploy(request)
	if err != nil || second.Changed {
		t.Fatalf("idempotent deploy failed: %+v %v", second, err)
	}
}

func TestDeployReplacesOwnedLegacySharedLink(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dataDir := filepath.Join(root, "data")
	paths := DefaultPaths(home)
	source := DefaultSharedSource(paths)
	source.Name = "legacy-shared"
	sourceTarget := filepath.Join(source.SkillsRoot, "example")
	target := filepath.Join(paths.RuntimeRoots["codex"], "example")
	if err := os.MkdirAll(sourceTarget, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sourceTarget, target); err != nil {
		t.Fatal(err)
	}
	writeSharedOwnershipState(t, dataDir, source.Name, "codex", "example", target, sourceTarget)

	archive := testSkillZIP(t)
	pkg, err := skills.ScanZIP(archive, skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	deployer := Deployer{DataDir: dataDir, Paths: paths, SharedSources: []SharedSourceConfig{source}}
	result, err := deployer.Deploy(DeployRequest{Runtime: "codex", SkillSlug: "example", VersionID: "v1", SHA256: pkg.SHA256, Enabled: true, Artifact: pkg.CanonicalZIP})
	if err != nil || !result.Changed || result.BackupPath == "" {
		t.Fatalf("owned link takeover failed: result=%+v err=%v", result, err)
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("target was not materialized: info=%v err=%v", info, err)
	}
	backupInfo, err := os.Lstat(result.BackupPath)
	if err != nil || backupInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("legacy link was not preserved as backup: info=%v err=%v", backupInfo, err)
	}
	if !fileExists(filepath.Join(target, ".toolhub-managed.json")) {
		t.Fatal("materialized target is missing the management marker")
	}
}

func TestDeployRefusesUnownedOrRetargetedLegacyLink(t *testing.T) {
	for _, test := range []struct {
		name       string
		writeState bool
		retarget   bool
	}{
		{name: "unowned"},
		{name: "retargeted", writeState: true, retarget: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			dataDir := filepath.Join(root, "data")
			paths := DefaultPaths(home)
			source := DefaultSharedSource(paths)
			source.Name = "legacy-shared"
			sourceTarget := filepath.Join(source.SkillsRoot, "example")
			actualTarget := sourceTarget
			if test.retarget {
				actualTarget = filepath.Join(root, "other", "example")
			}
			target := filepath.Join(paths.RuntimeRoots["codex"], "example")
			if err := os.MkdirAll(actualTarget, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(actualTarget, target); err != nil {
				t.Fatal(err)
			}
			if test.writeState {
				writeSharedOwnershipState(t, dataDir, source.Name, "codex", "example", target, sourceTarget)
			}
			archive := testSkillZIP(t)
			pkg, err := skills.ScanZIP(archive, skills.DefaultLimits)
			if err != nil {
				t.Fatal(err)
			}
			deployer := Deployer{DataDir: dataDir, Paths: paths, SharedSources: []SharedSourceConfig{source}}
			if _, err := deployer.Deploy(DeployRequest{Runtime: "codex", SkillSlug: "example", VersionID: "v1", SHA256: pkg.SHA256, Enabled: true, Artifact: pkg.CanonicalZIP}); err == nil {
				t.Fatal("unsafe legacy symlink was replaced")
			}
			if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("refused target was modified: info=%v err=%v", info, err)
			}
		})
	}
}

func writeSharedOwnershipState(t *testing.T, dataDir, sourceName, runtimeKind, skillSlug, target, expected string) {
	t.Helper()
	state := newSharedState(sourceName)
	state.Links[runtimeKind] = map[string]managedLink{skillSlug: {TargetPath: target, ExpectedTarget: expected}}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := sharedStatePath(dataDir, sourceName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func testSkillZIP(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, _ := writer.Create("SKILL.md")
	_, _ = file.Write([]byte("---\nname: Example\nlicense: MIT\n---\nBody\n"))
	_ = writer.Close()
	return buffer.Bytes()
}
