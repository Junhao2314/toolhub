package profilebundle

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/skills"
)

func TestEncodeParseDeterministicStandardBundle(t *testing.T) {
	pkg := bundleTestPackage(t)
	manifest := bundleTestManifest(pkg)
	first, err := Encode(manifest, map[string][]byte{pkg.SHA256: pkg.CanonicalZIP}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(manifest, map[string][]byte{pkg.SHA256: pkg.CanonicalZIP}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("identical Profile Bundles were not deterministic")
	}
	parsed, err := Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Manifest.Kind != KindStandard || len(parsed.Secrets.MCPServers) != 0 || parsed.Packages[pkg.SHA256].ContentHash != pkg.ContentHash {
		t.Fatalf("unexpected parsed Bundle: %+v", parsed.Manifest)
	}
}

func TestSecretBundleValidatesExactSlotsAndTamper(t *testing.T) {
	pkg := bundleTestPackage(t)
	manifest := bundleTestManifest(pkg)
	mcp := MCP{Name: "server", Transport: "http", URL: "https://example.invalid/mcp", EnvSlots: []string{"TOKEN"}, HeaderSlots: []string{}, Provenance: Provenance{Source: "local"}}
	var err error
	mcp.Key, err = MCPKey(mcp)
	if err != nil {
		t.Fatal(err)
	}
	manifest.MCPServers = []MCP{mcp}
	secrets := &SecretDocument{MCPServers: []SecretMCP{{Key: mcp.Key, Env: map[string]string{"TOKEN": "test-only-value"}, Headers: map[string]string{}}}}
	body, err := Encode(manifest, map[string][]byte{pkg.SHA256: pkg.CanonicalZIP}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(body); err != nil {
		t.Fatal(err)
	}
	secrets.MCPServers[0].Env = map[string]string{}
	if _, err := Encode(manifest, map[string][]byte{pkg.SHA256: pkg.CanonicalZIP}, secrets); err == nil {
		t.Fatal("missing Secret slot was accepted")
	}
	body[len(body)/2] ^= 0xff
	if _, err := Parse(body); err == nil {
		t.Fatal("tampered Profile Bundle was accepted")
	}
}

func TestParseRejectsUnsafeBundleEntry(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("../manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte(`{}`))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(buffer.Bytes()); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe entry error=%v", err)
	}
}

func bundleTestPackage(t *testing.T) skills.Package {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: Bundle Test\ndescription: deterministic\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg, err := skills.ScanDirectory(root, skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func bundleTestManifest(pkg skills.Package) Manifest {
	return Manifest{
		Origin: Origin{Label: "test", ExportedAt: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)},
		Profile: Profile{Name: "Bundle Test", SourceRevision: 1},
		Skills: []Skill{{Slug: pkg.Slug, Name: pkg.Name, Description: pkg.Description, SHA256: pkg.SHA256, ContentHash: pkg.ContentHash, Provenance: Provenance{Source: "local"}}},
		MCPServers: []MCP{},
	}
}
