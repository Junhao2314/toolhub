package agentclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
)

func TestLoadConfigAutoProbesOnlyWhenSharedSourcesAreOmitted(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".shared", "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"serverUrl":  "https://toolhub.example.test",
		"nodeId":     "node-1",
		"agentToken": "agent-token",
		"taskKey":    "task-key",
		"privateKey": "private-key",
		"dataDir":    filepath.Join(home, ".toolhub"),
		"paths":      runtimeadapter.DefaultPaths(home),
	}
	omittedPath := writeAgentConfig(t, base)
	omitted, err := LoadConfig(omittedPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(omitted.SharedSources) != 1 || omitted.SharedSources[0].Mode != runtimeadapter.SharedModeObserved {
		t.Fatalf("omitted sharedSources did not auto-probe observed state: %+v", omitted.SharedSources)
	}

	explicit := make(map[string]any, len(base)+1)
	for key, value := range base {
		explicit[key] = value
	}
	explicit["sharedSources"] = []any{}
	explicitPath := writeAgentConfig(t, explicit)
	configured, err := LoadConfig(explicitPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(configured.SharedSources) != 0 {
		t.Fatalf("explicit empty sharedSources was ignored: %+v", configured.SharedSources)
	}
}

func TestLoadConfigRejectsRelativeSharedPaths(t *testing.T) {
	home := t.TempDir()
	config := map[string]any{
		"serverUrl":  "https://toolhub.example.test",
		"nodeId":     "node-1",
		"agentToken": "agent-token",
		"taskKey":    "task-key",
		"privateKey": "private-key",
		"dataDir":    filepath.Join(home, ".toolhub"),
		"paths":      runtimeadapter.DefaultPaths(home),
		"sharedSources": []any{map[string]any{
			"name":        "shared",
			"mode":        "observed",
			"skillsRoot":  "relative/skills",
			"mcpManifest": filepath.Join(home, ".shared", "mcp", "servers.json"),
		}},
	}
	if _, err := LoadConfig(writeAgentConfig(t, config)); err == nil {
		t.Fatal("relative shared source path was accepted")
	}
}

func writeAgentConfig(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
