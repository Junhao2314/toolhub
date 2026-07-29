package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/skills"
)

func TestHermesSnapshotPackagingIsMarkerFreeAndReadOnly(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	target := filepath.Join(paths.RuntimeRoots[domain.RuntimeHermes], "example")
	writeDiscoveredSkill(t, target)
	markerPath := filepath.Join(target, ".toolhub-managed.json")
	marker := []byte(`{"legacy":true}`)
	if err := os.WriteFile(markerPath, marker, 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := skills.ScanDirectory(target, skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := PackageHermesSkillSnapshot(paths, target, observed.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(pkg.CanonicalZIP), int64(len(pkg.CanonicalZIP)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name == ".toolhub-managed.json" {
			t.Fatal("Hermes snapshot included the ToolHub marker")
		}
	}
	current, err := os.ReadFile(markerPath)
	if err != nil || !bytes.Equal(current, marker) {
		t.Fatalf("Hermes marker changed: body=%q err=%v", current, err)
	}
}

func TestHermesRuntimeWritersAreRejected(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	target := filepath.Join(paths.RuntimeRoots[domain.RuntimeHermes], "example")
	writeDiscoveredSkill(t, target)
	if _, err := PackageDiscoveredSkill(paths, domain.RuntimeHermes, target, "hash"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Hermes adoption package error=%v", err)
	}
	if err := MarkAdoptedSkill(paths, domain.RuntimeHermes, target, "hash", AdoptedSkillMarker{}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Hermes marker error=%v", err)
	}
	deployer := Deployer{DataDir: t.TempDir(), Paths: paths}
	if _, err := deployer.Deploy(DeployRequest{Runtime: domain.RuntimeHermes, SkillSlug: "example", Enabled: true}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Hermes deployment error=%v", err)
	}
	if _, err := ApplyMCP(context.Background(), paths, t.TempDir(), protocol.ApplyMCPPayload{Runtime: domain.RuntimeHermes}, nil); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Hermes MCP error=%v", err)
	}
}
