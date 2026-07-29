package agentclient

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
	"github.com/Junhao2314/toolhub/internal/skills"
)

func TestPublishHermesCapabilityPreservesOpenRuntimeConfig(t *testing.T) {
	runtimes := []domain.InventoryRuntime{
		{Kind: domain.RuntimeCodex, Config: map[string]any{"capabilities": []string{"codex-capability"}}},
		{Kind: domain.RuntimeHermes, Config: map[string]any{"legacy": true, "capabilities": []any{"existing", domain.CapabilityHermesReadOnlyImportV1}}},
	}
	publishHermesReadOnlyCapability(runtimes)
	if runtimes[1].Config["legacy"] != true {
		t.Fatal("Hermes runtime config was replaced")
	}
	capabilities, ok := runtimes[1].Config["capabilities"].([]string)
	if !ok || len(capabilities) != 2 || capabilities[0] != "existing" || capabilities[1] != domain.CapabilityHermesReadOnlyImportV1 {
		t.Fatalf("Hermes capabilities=%#v", runtimes[1].Config["capabilities"])
	}
	if capabilities := runtimes[0].Config["capabilities"].([]string); len(capabilities) != 1 || capabilities[0] != "codex-capability" {
		t.Fatalf("non-Hermes capabilities changed: %#v", capabilities)
	}
	if _, err := json.Marshal(domain.AgentInventory{Runtimes: runtimes}); err != nil {
		t.Fatalf("capability is not JSON compatible: %v", err)
	}
}

func TestExecutorRejectsSignedHermesWriterTasks(t *testing.T) {
	key := testTaskKey()
	executor := NewExecutor(Config{TaskKey: base64.StdEncoding.EncodeToString(key), DataDir: t.TempDir(), Paths: runtimeadapter.DefaultPaths(t.TempDir())})
	tests := []struct {
		kind    string
		payload any
	}{
		{kind: "deploy_skill", payload: protocol.DeploySkillPayload{Runtime: domain.RuntimeHermes, Enabled: true}},
		{kind: "apply_mcp", payload: protocol.ApplyMCPPayload{Runtime: domain.RuntimeHermes}},
		{kind: "adopt_skill", payload: map[string]any{"runtime": domain.RuntimeHermes}},
	}
	for index, test := range tests {
		body, err := json.Marshal(test.payload)
		if err != nil {
			t.Fatal(err)
		}
		task := signedAgentTask(t, key, "hermes-writer-"+test.kind+string(rune('0'+index)), test.kind, body)
		status, result := executor.Execute(context.Background(), task)
		if status != "failed" || !strings.Contains(string(result), "read-only") {
			t.Fatalf("%s status=%s result=%s", test.kind, status, result)
		}
	}
}

func TestExecutorImportsHermesSnapshotWithoutWritingMarker(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	paths := runtimeadapter.DefaultPaths(home)
	target := filepath.Join(paths.RuntimeRoots[domain.RuntimeHermes], "example")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: Hermes Example\nlicense: MIT\n---\nBody\n"), 0644); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(target, ".toolhub-managed.json")
	marker := []byte(`{"legacy":true}`)
	if err := os.WriteFile(markerPath, marker, 0600); err != nil {
		t.Fatal(err)
	}
	pkg, err := skills.ScanDirectory(target, skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/agent/v1/discoveries/discovery-1/skill-snapshot" || request.Header.Get("X-ToolHub-Task-ID") != "snapshot-task" || request.Header.Get("X-Content-SHA256") != pkg.SHA256 {
			t.Errorf("unexpected snapshot request: path=%s headers=%v", request.URL.Path, request.Header)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Error(readErr)
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		reader, zipErr := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if zipErr != nil {
			t.Error(zipErr)
			http.Error(w, "zip", http.StatusBadRequest)
			return
		}
		for _, file := range reader.File {
			if file.Name == ".toolhub-managed.json" {
				t.Error("snapshot upload included the management marker")
			}
		}
		uploads++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"skillId":"skill-1","versionId":"version-1","sha256":"` + pkg.SHA256 + `"}`))
	}))
	defer server.Close()
	key := testTaskKey()
	executor := NewExecutor(Config{
		ServerURL: server.URL, NodeID: "node-1", AgentToken: "agent-token",
		TaskKey: base64.StdEncoding.EncodeToString(key), DataDir: filepath.Join(root, "data"), Paths: paths,
	})
	body, err := json.Marshal(protocol.ImportSkillSnapshotPayload{DiscoveryID: "discovery-1", Runtime: domain.RuntimeHermes, Path: target, SHA256: pkg.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	task := signedAgentTask(t, key, "snapshot-task", "import_skill_snapshot", body)
	firstStatus, firstResult := executor.Execute(context.Background(), task)
	secondStatus, secondResult := executor.Execute(context.Background(), task)
	if firstStatus != "succeeded" || secondStatus != firstStatus || string(secondResult) != string(firstResult) || uploads != 1 {
		t.Fatalf("snapshot idempotency status=%s/%s uploads=%d result=%s/%s", firstStatus, secondStatus, uploads, firstResult, secondResult)
	}
	var result protocol.ImportSkillSnapshotResult
	if err := json.Unmarshal(firstResult, &result); err != nil || result.MarkerWritten {
		t.Fatalf("snapshot result=%+v err=%v", result, err)
	}
	current, err := os.ReadFile(markerPath)
	if err != nil || !bytes.Equal(current, marker) {
		t.Fatalf("Hermes marker changed: body=%q err=%v", current, err)
	}
}
