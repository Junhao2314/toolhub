package runtime

import (
	"archive/zip"
	"bytes"
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

func TestSharedDeployRequiresManagedOwnedTarget(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	source := DefaultSharedSource(DefaultPaths(home))
	source.Name = "shared"
	source.Mode = SharedModeManaged
	target := filepath.Join(source.SkillsRoot, "example")
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
	deployer := Deployer{DataDir: filepath.Join(root, "data"), Paths: DefaultPaths(home), SharedSources: []SharedSourceConfig{source}}
	_, err = deployer.Deploy(DeployRequest{Runtime: "shared", SourceName: source.Name, SkillSlug: "example", VersionID: "v1", SHA256: pkg.SHA256, Enabled: true, Artifact: pkg.CanonicalZIP})
	if err == nil {
		t.Fatal("unmanaged canonical shared Skill was overwritten")
	}
	source.Mode = SharedModeObserved
	deployer.SharedSources = []SharedSourceConfig{source}
	_, err = deployer.Deploy(DeployRequest{Runtime: "shared", SourceName: source.Name, SkillSlug: "new-skill", VersionID: "v1", SHA256: pkg.SHA256, Enabled: true, Artifact: pkg.CanonicalZIP})
	if err == nil {
		t.Fatal("observed-only shared source accepted a deployment")
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
