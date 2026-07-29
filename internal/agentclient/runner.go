package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Junhao2314/toolhub/internal/domain"
)

type Runner struct {
	config            Config
	executor          *Executor
	inventoryInterval time.Duration
	mu                sync.Mutex
	activeMu          sync.Mutex
	activeTasks       map[string]int
}

const DefaultInventoryInterval = 6 * time.Hour

func NewRunner(config Config) *Runner {
	return &Runner{config: config, executor: NewExecutor(config), inventoryInterval: DefaultInventoryInterval, activeTasks: map[string]int{}}
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
	stopCloser := make(chan struct{})
	defer close(stopCloser)
	go func() {
		select {
		case <-connectionCtx.Done():
			_ = socket.Close()
		case <-stopCloser:
		}
	}()
	go r.heartbeatLoop(connectionCtx, socket)
	if err := r.sendInventory(connectionCtx, socket); err != nil {
		return err
	}
	tasks := make(chan domain.AgentTask, 8)
	defer close(tasks)
	go r.executeTasks(ctx, socket, tasks)
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
		if !r.acceptTask(envelope.Task) {
			_ = r.sendTaskResult(socket, envelope.Task.ID, r.activeAttempt(envelope.Task.ID, envelope.Task.Attempt), "running", map[string]any{"active": true})
			continue
		}
		if err := r.sendTaskResult(socket, envelope.Task.ID, envelope.Task.Attempt, "running", map[string]any{}); err != nil {
			r.clearActive(envelope.Task.ID)
			return err
		}
		select {
		case tasks <- envelope.Task:
		default:
			r.clearActive(envelope.Task.ID)
			if err := r.sendTaskResult(socket, envelope.Task.ID, envelope.Task.Attempt, "failed", map[string]any{"error": "Agent task queue is full", "code": "queue_full"}); err != nil {
				return err
			}
		}
	}
}

func (r *Runner) executeTasks(ctx context.Context, socket *websocket.Conn, tasks <-chan domain.AgentTask) {
	for task := range tasks {
		deadline := taskDeadline(task.Kind)
		taskCtx := ctx
		cancel := func() {}
		if deadline > 0 {
			taskCtx, cancel = context.WithTimeout(ctx, deadline)
		}
		done := make(chan struct{})
		go r.runningHeartbeat(taskCtx, socket, task.ID, task.Attempt, done)
		status, result := r.executor.Execute(taskCtx, task)
		cancel()
		<-done
		if task.Kind == "scan_inventory" && status == "succeeded" {
			var inventory domain.AgentInventory
			if json.Unmarshal(result, &inventory) == nil {
				_ = r.send(socket, "inventory", inventory)
			}
		} else if (task.Kind == "apply_mcp" || task.Kind == "deploy_skill") && status == "succeeded" {
			if err := r.sendInventory(ctx, socket); err != nil {
				log.Printf("refresh inventory after %s: %v", task.Kind, err)
			}
		}
		attempt := r.activeAttempt(task.ID, task.Attempt)
		if err := r.sendTaskResult(socket, task.ID, attempt, status, json.RawMessage(result)); err != nil {
			log.Printf("send task result %s attempt %d: %v", task.ID, attempt, err)
		}
		r.clearActive(task.ID)
	}
}

func (r *Runner) runningHeartbeat(ctx context.Context, socket *websocket.Conn, taskID string, attempt int, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.sendTaskResult(socket, taskID, r.activeAttempt(taskID, attempt), "running", map[string]any{})
		}
	}
}

func taskDeadline(kind string) time.Duration {
	switch kind {
	case "scan_inventory":
		return 2 * time.Minute
	case "deploy_skill", "apply_mcp", "adopt_skill", "import_skill_snapshot":
		return 10 * time.Minute
	default:
		return time.Minute
	}
}

func (r *Runner) acceptTask(task domain.AgentTask) bool {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if current, exists := r.activeTasks[task.ID]; exists {
		if task.Attempt > current {
			r.activeTasks[task.ID] = task.Attempt
		}
		return false
	}
	r.activeTasks[task.ID] = task.Attempt
	return true
}

func (r *Runner) activeAttempt(taskID string, fallback int) int {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if attempt, ok := r.activeTasks[taskID]; ok && attempt > 0 {
		return attempt
	}
	return fallback
}

func (r *Runner) clearActive(taskID string) {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	delete(r.activeTasks, taskID)
}

func (r *Runner) heartbeatLoop(ctx context.Context, socket *websocket.Conn) {
	heartbeat := time.NewTicker(30 * time.Second)
	inventory := time.NewTicker(r.inventoryInterval)
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
	inventory, err := r.executor.discoverInventory(ctx)
	if err != nil {
		return err
	}
	return r.send(socket, "inventory", inventory)
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

func (r *Runner) sendTaskResult(socket *websocket.Conn, taskID string, attempt int, status string, result any) error {
	return r.send(socket, "task_result", map[string]any{"id": taskID, "attempt": attempt, "status": status, "result": result})
}
