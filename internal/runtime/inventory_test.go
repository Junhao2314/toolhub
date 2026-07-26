package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanAllNormalizesMCPFormatsWithoutSecretValues(t *testing.T) {
	home := t.TempDir()
	files := map[string]string{
		filepath.Join(home, ".codex", "config.toml"): `[mcp_servers.codex_server]
command = "npx"
args = ["-y", "example"]
[mcp_servers.codex_server.env]
TOKEN = "codex-secret"
`,
		filepath.Join(home, ".claude.json"):           `{"mcpServers":{"claude_server":{"type":"http","url":"https://example.test/mcp","env":{"API_KEY":"claude-secret"}}}}`,
		filepath.Join(home, ".hermes", "config.yaml"): "mcp_servers:\n  hermes_server:\n    command: python\n    args: [server.py]\n    env:\n      PASSWORD: hermes-secret\n",
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	scan, err := ScanAllWithKey(DefaultPaths(home), bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Runtimes) != 3 {
		t.Fatalf("runtime count = %d", len(scan.Runtimes))
	}
	encoded, err := json.Marshal(scan.Runtimes)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"codex-secret", "claude-secret", "hermes-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("ordinary inventory leaked %q: %s", secret, encoded)
		}
	}
	for _, runtime := range scan.Runtimes {
		if len(runtime.MCPServers) != 1 || runtime.MCPServers[0].ConfigFingerprint == "" || runtime.MCPServers[0].SecretFingerprint == "" {
			t.Fatalf("unexpected %s descriptor: %+v", runtime.Kind, runtime.MCPServers)
		}
	}
}
