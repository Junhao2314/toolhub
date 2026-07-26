package agentclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
	"github.com/Junhao2314/toolhub/internal/security"
)

func TestExecutorApplyMCPIsSignedAndIdempotent(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dataDir := filepath.Join(root, "data")
	disableMCPMCLI(t, root)
	key := testTaskKey()
	executor := NewExecutor(Config{
		TaskKey: base64.StdEncoding.EncodeToString(key),
		DataDir: dataDir,
		Paths:   runtimeadapter.DefaultPaths(home),
	})
	payload := testApplyMCPPayload(t)
	task := signedAgentTask(t, key, "task-apply-mcp", "apply_mcp", payload)

	firstStatus, firstResult := executor.Execute(context.Background(), task)
	secondStatus, secondResult := executor.Execute(context.Background(), task)
	if firstStatus != "succeeded" || secondStatus != firstStatus || string(secondResult) != string(firstResult) {
		t.Fatalf("idempotent apply_mcp mismatch: %s %s %s %s", firstStatus, secondStatus, firstResult, secondResult)
	}
	for _, path := range []string{runtimeadapter.MCPMStorePath(home), filepath.Join(home, ".codex", "config.toml")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("managed MCP output %s mode=%v err=%v", path, info, err)
		}
	}

	invalid := task
	invalid.ID = "task-invalid-signature"
	invalid.Signature = "invalid"
	status, result := executor.Execute(context.Background(), invalid)
	if status != "failed" || !json.Valid(result) {
		t.Fatalf("invalid signature result: status=%s result=%s", status, result)
	}
}

func TestRunnerReportsFreshInventoryAfterApplyMCP(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	disableMCPMCLI(t, root)
	key := testTaskKey()
	task := signedAgentTask(t, key, "task-wss-apply-mcp", "apply_mcp", testApplyMCPPayload(t))
	upgrader := websocket.Upgrader{}
	completed := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/v1/discoveries/descriptors", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"captureRequests":[]}`))
	})
	mux.HandleFunc("/agent/v1/connect", func(w http.ResponseWriter, request *http.Request) {
		socket, err := upgrader.Upgrade(w, request, nil)
		if err != nil {
			completed <- err
			return
		}
		defer socket.Close()
		var initial domain.AgentMessage
		if err := socket.ReadJSON(&initial); err != nil || initial.Type != "inventory" {
			completed <- errors.New("initial inventory was not reported")
			return
		}
		if err := socket.WriteJSON(map[string]any{"type": "task", "task": task}); err != nil {
			completed <- err
			return
		}
		var running domain.AgentMessage
		if err := socket.ReadJSON(&running); err != nil || running.Type != "task_result" {
			completed <- errors.New("running task result was not reported")
			return
		}
		var refreshed domain.AgentMessage
		if err := socket.ReadJSON(&refreshed); err != nil || refreshed.Type != "inventory" {
			completed <- errors.New("fresh inventory was not reported after apply_mcp")
			return
		}
		var inventory domain.AgentInventory
		if err := json.Unmarshal(refreshed.Payload, &inventory); err != nil || len(inventory.MCPImports) != 1 {
			completed <- errors.New("refreshed inventory did not include the mcpm server")
			return
		}
		anchorValid := false
		for _, runtime := range inventory.Runtimes {
			if runtime.Kind != domain.RuntimeCodex {
				continue
			}
			anchor, _ := runtime.Config["mcpAnchor"].(map[string]any)
			anchorValid, _ = anchor["valid"].(bool)
		}
		if !anchorValid {
			completed <- errors.New("refreshed inventory did not observe the Codex mcpm anchor")
			return
		}
		var final domain.AgentMessage
		if err := socket.ReadJSON(&final); err != nil || final.Type != "task_result" {
			completed <- errors.New("final task result was not reported")
			return
		}
		completed <- nil
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	runner := NewRunner(Config{
		ServerURL:  server.URL,
		NodeID:     "node-1",
		AgentToken: "agent-token",
		TaskKey:    base64.StdEncoding.EncodeToString(key),
		DataDir:    filepath.Join(root, "data"),
		Paths:      runtimeadapter.DefaultPaths(home),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.runConnection(ctx) }()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("apply_mcp inventory exchange timed out")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		server.CloseClientConnections()
		t.Fatal("runner did not stop")
	}
}

func testApplyMCPPayload(t *testing.T) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(protocol.ApplyMCPPayload{
		DeploymentID:      "deployment-1",
		DesiredGeneration: 1,
		DesiredHash:       "desired-hash",
		Runtime:           domain.RuntimeCodex,
		ProfileID:         "profile-1",
		ProfileName:       "toolhub-codex",
		MCPMProfile:       "toolhub-codex",
		Enabled:           true,
		Servers: []protocol.MCPServerRef{{
			ID: "server-1", Name: "memory", Transport: "stdio", Command: "memory-server", Args: []string{"--stdio"},
			EnvRefs: map[string]string{}, HeaderRefs: map[string]string{},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signedAgentTask(t *testing.T, key []byte, id, kind string, payload json.RawMessage) domain.AgentTask {
	t.Helper()
	signingBytes, err := protocol.TaskSigningBytes(id, kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return domain.AgentTask{ID: id, Kind: kind, Payload: payload, Signature: security.SignPayload(key, signingBytes), CreatedAt: time.Now().UTC()}
}

func testTaskKey() []byte {
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	return key
}

func disableMCPMCLI(t *testing.T, root string) {
	t.Helper()
	emptyPath := filepath.Join(root, "empty-path")
	if err := os.MkdirAll(emptyPath, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", emptyPath)
}
