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
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
	"github.com/Junhao2314/toolhub/internal/security"
)

func TestExecutorSyncSharedIsSignedAndIdempotent(t *testing.T) {
	root := t.TempDir()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	source := executorSharedSource(t, root)
	config := Config{TaskKey: base64.StdEncoding.EncodeToString(key), DataDir: filepath.Join(root, "data"), Paths: runtimeadapter.DefaultPaths(filepath.Join(root, "home")), SharedSources: []runtimeadapter.SharedSourceConfig{source}}
	executor := NewExecutor(config)
	payload, err := json.Marshal(protocol.SyncSharedPayload{SourceID: "source-1", SourceName: source.Name, Scopes: []string{"skills"}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	task := signedAgentTask(t, key, "task-1", "sync_shared", payload)
	firstStatus, firstResult := executor.Execute(context.Background(), task)
	secondStatus, secondResult := executor.Execute(context.Background(), task)
	if firstStatus != "succeeded" || secondStatus != firstStatus || string(secondResult) != string(firstResult) {
		t.Fatalf("idempotent shared task mismatch: %s %s %s %s", firstStatus, secondStatus, firstResult, secondResult)
	}
	invalid := task
	invalid.ID = "task-2"
	invalid.Signature = "invalid"
	status, result := executor.Execute(context.Background(), invalid)
	if status != "failed" || !json.Valid(result) {
		t.Fatalf("invalid signature result: status=%s result=%s", status, result)
	}
}

func TestRunnerReportsFreshInventoryAfterSharedSync(t *testing.T) {
	root := t.TempDir()
	key := make([]byte, 32)
	source := executorSharedSource(t, root)
	payload, err := json.Marshal(protocol.SyncSharedPayload{SourceID: "source-1", SourceName: source.Name, Scopes: []string{"skills"}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	task := signedAgentTask(t, key, "task-sync", "sync_shared", payload)
	upgrader := websocket.Upgrader{}
	completed := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/v1/discoveries/descriptors", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"captureRequests":[]}`))
	})
	mux.HandleFunc("/agent/v1/connect", func(w http.ResponseWriter, r *http.Request) {
		socket, err := upgrader.Upgrade(w, r, nil)
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
			completed <- errors.New("fresh inventory was not reported after shared sync")
			return
		}
		var inventory domain.AgentInventory
		if err := json.Unmarshal(refreshed.Payload, &inventory); err != nil || len(inventory.Runtimes) != 5 || len(inventory.SharedSources) != 1 {
			completed <- errors.New("shared sync result was sent instead of Agent inventory")
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
	runner := NewRunner(Config{ServerURL: server.URL, NodeID: "node-1", AgentToken: "agent-token", TaskKey: base64.StdEncoding.EncodeToString(key), DataDir: filepath.Join(root, "data"), Paths: runtimeadapter.DefaultPaths(filepath.Join(root, "home")), SharedSources: []runtimeadapter.SharedSourceConfig{source}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.runConnection(ctx) }()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shared sync inventory exchange timed out")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		server.CloseClientConnections()
		t.Fatal("runner did not stop")
	}
}

func TestRetrySharedReconcileBacksOffAndRecovers(t *testing.T) {
	var attempts atomic.Int32
	started := time.Now()
	err := retrySharedReconcile(context.Background(), 10*time.Millisecond, 4, func() error {
		if attempts.Add(1) < 3 {
			return errors.New("temporary manifest parse failure")
		}
		return nil
	})
	if err != nil || attempts.Load() != 3 {
		t.Fatalf("retry result: attempts=%d err=%v", attempts.Load(), err)
	}
	if time.Since(started) < 25*time.Millisecond {
		t.Fatal("reconcile retry did not back off")
	}
}

func executorSharedSource(t *testing.T, root string) runtimeadapter.SharedSourceConfig {
	t.Helper()
	skillsRoot := filepath.Join(root, "shared", "skills")
	manifest := filepath.Join(root, "shared", "mcp", "servers.json")
	if err := os.MkdirAll(skillsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(manifest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"servers":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return runtimeadapter.SharedSourceConfig{Name: "test-shared", Mode: runtimeadapter.SharedModeManaged, SkillsRoot: skillsRoot, MCPManifest: manifest, AllowedSkillRoots: []string{skillsRoot}, Consumers: map[string]runtimeadapter.SharedConsumerConfig{}}
}

func signedAgentTask(t *testing.T, key []byte, id, kind string, payload json.RawMessage) domain.AgentTask {
	t.Helper()
	signingBytes, err := protocol.TaskSigningBytes(id, kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return domain.AgentTask{ID: id, Kind: kind, Payload: payload, Signature: security.SignPayload(key, signingBytes), CreatedAt: time.Now().UTC()}
}
