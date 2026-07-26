package agentclient

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
)

func TestRunnerUsesSixHourInventoryInterval(t *testing.T) {
	runner := NewRunner(Config{})
	if runner.inventoryInterval != 6*time.Hour || DefaultInventoryInterval != 6*time.Hour {
		t.Fatalf("inventory interval = %s", runner.inventoryInterval)
	}
}

func TestRunConnectionStopsWhenContextIsCancelled(t *testing.T) {
	ready := make(chan struct{})
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/v1/discoveries/descriptors", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"captureRequests":[]}`))
	})
	mux.HandleFunc("/agent/v1/connect", func(w http.ResponseWriter, r *http.Request) {
		socket, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer socket.Close()
		if _, _, err := socket.ReadMessage(); err != nil {
			t.Errorf("read initial inventory: %v", err)
			return
		}
		close(ready)
		_, _, _ = socket.ReadMessage()
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	home := t.TempDir()
	runner := NewRunner(Config{
		ServerURL:  server.URL,
		NodeID:     "node-1",
		AgentToken: "agent-token",
		TaskKey:    base64.StdEncoding.EncodeToString(make([]byte, 32)),
		DataDir:    t.TempDir(),
		Paths:      runtimeadapter.DefaultPaths(home),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.runConnection(ctx) }()

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		server.CloseClientConnections()
		t.Fatal("Agent did not finish its initial inventory exchange")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		server.CloseClientConnections()
		t.Fatal("runConnection did not stop after context cancellation")
	}
}
