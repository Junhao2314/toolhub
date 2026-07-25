package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/toolhub-dev/toolhub/internal/domain"
	runtimeadapter "github.com/toolhub-dev/toolhub/internal/runtime"
)

type Runner struct {
	config   Config
	executor *Executor
	mu       sync.Mutex
}

func NewRunner(config Config) *Runner {
	return &Runner{config: config, executor: NewExecutor(config)}
}

func (r *Runner) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := r.runConnection(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff + time.Duration(rand.IntN(1000))*time.Millisecond):
			}
			backoff *= 2
			if backoff > time.Minute {
				backoff = time.Minute
			}
			continue
		}
		backoff = time.Second
	}
}

func (r *Runner) runConnection(ctx context.Context) error {
	endpoint, err := url.Parse(strings.TrimRight(r.config.ServerURL, "/") + "/agent/v1/connect")
	if err != nil {
		return err
	}
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	headers := http.Header{"Authorization": []string{"Bearer " + r.config.AgentToken}, "X-ToolHub-Node-ID": []string{r.config.NodeID}}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, EnableCompression: true}
	socket, response, err := dialer.DialContext(ctx, endpoint.String(), headers)
	if err != nil {
		if response != nil {
			return fmt.Errorf("agent connect HTTP %d: %w", response.StatusCode, err)
		}
		return err
	}
	defer socket.Close()
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go r.heartbeatLoop(connectionCtx, socket)
	if err := r.sendInventory(connectionCtx, socket); err != nil {
		return err
	}
	socket.SetReadLimit(2 << 20)
	for {
		var envelope struct {
			Type string           `json:"type"`
			Task domain.AgentTask `json:"task"`
		}
		if err := socket.ReadJSON(&envelope); err != nil {
			return err
		}
		if envelope.Type != "task" {
			continue
		}
		_ = r.send(socket, "task_result", map[string]any{"id": envelope.Task.ID, "status": "running", "result": map[string]any{}})
		status, result := r.executor.Execute(connectionCtx, envelope.Task)
		if envelope.Task.Kind == "scan_inventory" && status == "succeeded" {
			var inventory map[string]any
			if json.Unmarshal(result, &inventory) == nil {
				_ = r.send(socket, "inventory", inventory)
			}
		}
		if err := r.send(socket, "task_result", map[string]any{"id": envelope.Task.ID, "status": status, "result": json.RawMessage(result)}); err != nil {
			return err
		}
	}
}

func (r *Runner) heartbeatLoop(ctx context.Context, socket *websocket.Conn) {
	heartbeat := time.NewTicker(30 * time.Second)
	inventory := time.NewTicker(10 * time.Minute)
	defer heartbeat.Stop()
	defer inventory.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			hostname, _ := os.Hostname()
			if r.send(socket, "heartbeat", map[string]any{"hostname": hostname, "platform": runtime.GOOS, "architecture": runtime.GOARCH}) != nil {
				return
			}
		case <-inventory.C:
			if r.sendInventory(ctx, socket) != nil {
				return
			}
		}
	}
}

func (r *Runner) sendInventory(ctx context.Context, socket *websocket.Conn) error {
	runtimes, err := runtimeadapter.ScanAll(r.config.Paths)
	if err != nil {
		return err
	}
	return r.send(socket, "inventory", map[string]any{"runtimes": runtimes})
}

func (r *Runner) send(socket *websocket.Conn, messageType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = socket.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return socket.WriteJSON(domain.AgentMessage{Type: messageType, Timestamp: time.Now().UTC(), Payload: encoded})
}
