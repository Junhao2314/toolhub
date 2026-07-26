package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSharedManifestRedactsValuesAndCanonicalizesHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	writeTestFile(t, path, `{
  "version": 1,
  "servers": {
    "remote": {
      "enabled": true,
      "transport": "streamable-http",
      "url": "https://example.test/mcp",
      "env": {"TOKEN": "env-secret-value"},
      "headers": {"authorization": "Bearer header-secret-value"}
    }
  }
}`, 0600)
	key := bytes.Repeat([]byte{7}, 32)
	manifest, err := ReadSharedManifest(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Servers) != 1 {
		t.Fatalf("server count = %d", len(manifest.Servers))
	}
	descriptor := manifest.Servers[0].Descriptor
	if len(descriptor.EnvKeys) != 1 || descriptor.EnvKeys[0] != "TOKEN" || len(descriptor.HeaderKeys) != 1 || descriptor.HeaderKeys[0] != "Authorization" {
		t.Fatalf("unexpected redacted descriptor: %+v", descriptor)
	}
	if descriptor.SecretFingerprint == "" || manifest.SourceFingerprint == "" {
		t.Fatalf("missing keyed fingerprints: %+v", descriptor)
	}
	encoded, err := json.Marshal(struct {
		Descriptor any    `json:"descriptor"`
		Source     string `json:"source"`
	}{descriptor, manifest.SourceFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"env-secret-value", "header-secret-value"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("redacted manifest output leaked %q: %s", secret, encoded)
		}
	}

	writeTestFile(t, path, strings.ReplaceAll(`{
  "version": 1,
  "servers": {
    "remote": {
      "enabled": true,
      "transport": "streamable-http",
      "url": "https://example.test/mcp",
      "env": {"TOKEN": "changed-env-secret"},
      "headers": {"Authorization": "Bearer header-secret-value"}
    }
  }
}`, "\\n", "\n"), 0600)
	changed, err := ReadSharedManifest(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if changed.SourceFingerprint == manifest.SourceFingerprint || changed.Servers[0].Descriptor.SecretFingerprint == descriptor.SecretFingerprint {
		t.Fatal("secret-only manifest change did not update keyed fingerprints")
	}
}

func TestReadSharedManifestRejectsInvalidHeaderName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	writeTestFile(t, path, `{"servers":{"remote":{"enabled":true,"transport":"streamable-http","url":"https://example.test/mcp","headers":{"Bad Header":"secret"}}}}`, 0600)
	if _, err := ReadSharedManifest(path, bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Fatal("invalid manifest header name was accepted")
	}
}

func writeTestFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
