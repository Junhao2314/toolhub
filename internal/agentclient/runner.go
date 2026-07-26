package agentclient

import (
	"context"
	"encoding/base64"
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
	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
)

type Runner struct {
	config            Config
	executor          *Executor
	inventoryInterval time.Duration
	inventoryTrigger  chan struct{}
	mu                sync.Mutex
}

const DefaultInventoryInterval = 6 * time.Hour

const sharedReconcileRetryAttempts = 5

func NewRunner(config Config) *Runner {
	return &Runner{config: config, executor: NewExecutor(config), inventoryInterval: DefaultInventoryInterval, inventoryTrigger: make(chan struct{}, 1)}
}

func (r *Runner) Run(ctx context.Context) error {
	r.startSharedWatchers(ctx)
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
			var inventory domain.AgentInventory
			if json.Unmarshal(result, &inventory) == nil {
				_ = r.send(socket, "inventory", inventory)
			}
		} else if envelope.Task.Kind == "sync_shared" && status == "succeeded" {
			if err := r.sendInventory(connectionCtx, socket); err != nil {
				log.Printf("refresh inventory after shared sync: %v", err)
			}
		}
		if err := r.send(socket, "task_result", map[string]any{"id": envelope.Task.ID, "status": status, "result": json.RawMessage(result)}); err != nil {
			return err
		}
	}
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
		case <-r.inventoryTrigger:
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

func (r *Runner) startSharedWatchers(ctx context.Context) {
	key, err := base64.StdEncoding.DecodeString(r.config.TaskKey)
	if err != nil || len(key) != 32 {
		return
	}
	reconciler := runtimeadapter.SharedReconciler{DataDir: r.config.DataDir, Sources: r.config.SharedSources, FingerprintKey: key}
	for _, source := range r.config.SharedSources {
		source := source
		if source.Mode != runtimeadapter.SharedModeManaged || !source.AutoSync {
			continue
		}
		go func() {
			err := runtimeadapter.WatchSharedSourceUntilCancelled(ctx, source, func(reconcileCtx context.Context, scopes []string) {
				reconcileErr := retrySharedReconcile(reconcileCtx, 500*time.Millisecond, sharedReconcileRetryAttempts, func() error {
					_, err := reconciler.Reconcile(reconcileCtx, runtimeadapter.SharedSyncRequest{SourceName: source.Name, Scopes: scopes})
					return err
				})
				if reconcileErr != nil {
					log.Printf("shared source %s reconcile failed: %v", source.Name, reconcileErr)
					return
				}
				select {
				case r.inventoryTrigger <- struct{}{}:
				default:
				}
			})
			if err != nil && ctx.Err() == nil {
				log.Printf("shared source %s watcher stopped: %v", source.Name, err)
			}
		}()
	}
}

func retrySharedReconcile(ctx context.Context, minimumDelay time.Duration, attempts int, reconcile func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	delay := minimumDelay
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if lastErr = reconcile(); lastErr == nil {
			return nil
		}
		if attempt == attempts-1 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay *= 2
	}
	return lastErr
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
