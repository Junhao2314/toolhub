package agenthub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/store"
)

var ErrOffline = errors.New("node is offline")

type connection struct {
	socket *websocket.Conn
	mu     sync.Mutex
}

func (c *connection) write(value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.socket.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return c.socket.WriteJSON(value)
}

type Hub struct {
	store       *store.Store
	logger      *slog.Logger
	publicHost  string
	mu          sync.RWMutex
	connections map[string]*connection
	upgrader    websocket.Upgrader
}

func New(st *store.Store, logger *slog.Logger, publicHost string) *Hub {
	return &Hub{
		store: st, logger: logger, publicHost: publicHost, connections: map[string]*connection{},
		upgrader: websocket.Upgrader{HandshakeTimeout: 10 * time.Second, ReadBufferSize: 16 << 10, WriteBufferSize: 16 << 10},
	}
}

func (h *Hub) ServeConnect(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.Header.Get("X-ToolHub-Node-ID"))
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if nodeID == "" || token == "" || h.store.VerifyAgent(r.Context(), nodeID, token) != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	upgrader := h.upgrader
	upgrader.CheckOrigin = func(request *http.Request) bool {
		origin := request.Header.Get("Origin")
		return origin == "" || h.publicHost == "" || strings.Contains(origin, h.publicHost)
	}
	socket, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn := &connection{socket: socket}
	h.mu.Lock()
	if previous := h.connections[nodeID]; previous != nil {
		_ = previous.socket.Close()
	}
	h.connections[nodeID] = conn
	h.mu.Unlock()
	_ = h.store.SetNodeStatus(r.Context(), nodeID, "online")
	h.logger.Info("agent connected", "nodeId", nodeID)
	defer func() {
		h.mu.Lock()
		if h.connections[nodeID] == conn {
			delete(h.connections, nodeID)
		}
		h.mu.Unlock()
		_ = h.store.SetNodeStatus(context.Background(), nodeID, "offline")
		_ = socket.Close()
		h.logger.Info("agent disconnected", "nodeId", nodeID)
	}()

	socket.SetReadLimit(2 << 20)
	_ = socket.SetReadDeadline(time.Now().Add(90 * time.Second))
	socket.SetPongHandler(func(string) error {
		return socket.SetReadDeadline(time.Now().Add(90 * time.Second))
	})
	go h.pingUntilClosed(conn)
	h.deliverPending(r.Context(), nodeID, conn)
	for {
		var message domain.AgentMessage
		if err := socket.ReadJSON(&message); err != nil {
			return
		}
		if err := h.handleMessage(r.Context(), nodeID, message); err != nil {
			h.logger.Warn("agent message rejected", "nodeId", nodeID, "type", message.Type, "error", err)
			_ = conn.write(map[string]any{"type": "error", "message": "message rejected"})
		}
	}
}

func (h *Hub) SendTask(nodeID string, task domain.AgentTask) error {
	h.mu.RLock()
	conn := h.connections[nodeID]
	h.mu.RUnlock()
	if conn == nil {
		return ErrOffline
	}
	if err := conn.write(map[string]any{"type": "task", "task": task}); err != nil {
		return err
	}
	return h.store.MarkTaskDelivered(context.Background(), task.ID)
}

func (h *Hub) IsOnline(nodeID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.connections[nodeID] != nil
}

func (h *Hub) deliverPending(ctx context.Context, nodeID string, conn *connection) {
	tasks, err := h.store.PendingNodeTasks(ctx, nodeID)
	if err != nil {
		h.logger.Error("load pending node tasks", "nodeId", nodeID, "error", err)
		return
	}
	for _, task := range tasks {
		if err := conn.write(map[string]any{"type": "task", "task": task}); err != nil {
			return
		}
		_ = h.store.MarkTaskDelivered(ctx, task.ID)
	}
}

func (h *Hub) handleMessage(ctx context.Context, nodeID string, message domain.AgentMessage) error {
	switch message.Type {
	case "heartbeat":
		var payload struct{ Hostname, Platform, Architecture string }
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return err
		}
		return h.store.UpdateHeartbeat(ctx, nodeID, payload.Hostname, payload.Platform, payload.Architecture)
	case "inventory":
		var payload domain.AgentInventory
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return err
		}
		return h.store.ReplaceInventory(ctx, nodeID, payload)
	case "task_result":
		var payload struct {
			ID     string          `json:"id"`
			Status string          `json:"status"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return err
		}
		return h.store.CompleteTask(ctx, nodeID, payload.ID, payload.Status, payload.Result)
	default:
		return errors.New("unsupported message type")
	}
}

func (h *Hub) pingUntilClosed(conn *connection) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		conn.mu.Lock()
		_ = conn.socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
		err := conn.socket.WriteMessage(websocket.PingMessage, nil)
		conn.mu.Unlock()
		if err != nil {
			return
		}
	}
}
