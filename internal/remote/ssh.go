package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/store"
)

var ErrUnavailable = errors.New("SSH fallback is unavailable")

type Executor struct {
	store  *store.Store
	logger *slog.Logger
}

func New(st *store.Store, logger *slog.Logger) *Executor {
	return &Executor{store: st, logger: logger}
}

func (e *Executor) Dispatch(ctx context.Context, nodeID string, task domain.AgentTask, owner string) error {
	connection, err := e.store.SSHConnectionForNode(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		return ErrUnavailable
	}
	if err != nil {
		return err
	}
	if owner == "" {
		owner = "ssh-dispatch"
	}
	reserved, err := e.store.ReserveNodeTask(ctx, nodeID, task.ID, "ssh", owner, store.DefaultTaskLease)
	if err != nil {
		return err
	}
	localDir, err := os.MkdirTemp("", "toolhub-ssh-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(localDir)
	keyPath := filepath.Join(localDir, "identity")
	knownHostsPath := filepath.Join(localDir, "known_hosts")
	taskPath := filepath.Join(localDir, "task.json")
	batchPath := filepath.Join(localDir, "sftp.batch")
	remotePath := "/tmp/toolhub-task-" + reserved.ID + ".json"
	encoded, _ := json.Marshal(reserved)
	if err := os.WriteFile(keyPath, connection.PrivateKey, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(knownHostsPath, []byte(connection.KnownHosts+"\n"), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(taskPath, encoded, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(batchPath, []byte("put "+taskPath+" "+remotePath+"\n"), 0600); err != nil {
		return err
	}
	commandCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	common := []string{"-i", keyPath, "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + knownHostsPath, "-o", "ConnectTimeout=15"}
	sftpArgs := append(append([]string{}, common...), "-b", batchPath, connection.Address)
	if output, err := limitedCommand(commandCtx, "sftp", sftpArgs...); err != nil {
		return fmt.Errorf("SFTP task upload failed: %s", strings.TrimSpace(string(output)))
	}
	_, _ = e.store.CompleteTaskAttempt(ctx, nodeID, reserved.ID, reserved.Attempt, "running", json.RawMessage(`{}`))
	sshArgs := append(append([]string{}, common...), connection.Address, "toolhub-agent", "run-task", "--file", remotePath)
	output, err := limitedCommand(commandCtx, "ssh", sshArgs...)
	if err != nil {
		return fmt.Errorf("SSH fixed task failed: %s", strings.TrimSpace(string(output)))
	}
	var response struct {
		Status  string          `json:"status"`
		Attempt int             `json:"attempt"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &response); err != nil || (response.Status != "succeeded" && response.Status != "failed") {
		return errors.New("SSH agent returned an invalid task result")
	}
	if response.Attempt == 0 {
		response.Attempt = reserved.Attempt
	}
	if _, err := e.store.CompleteTaskAttempt(ctx, nodeID, reserved.ID, response.Attempt, response.Status, response.Result); err != nil {
		return err
	}
	e.logger.Info("task delivered through SSH fallback", "nodeId", nodeID, "taskId", reserved.ID, "kind", reserved.Kind, "attempt", reserved.Attempt)
	return nil
}

func limitedCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	limited := &cappedWriter{remaining: 2 << 20}
	command.Stdout = limited
	command.Stderr = limited
	err := command.Run()
	return limited.buffer.Bytes(), err
}

type cappedWriter struct {
	buffer    bytes.Buffer
	remaining int
}

func (w *cappedWriter) Write(payload []byte) (int, error) {
	original := len(payload)
	if len(payload) > w.remaining {
		payload = payload[:w.remaining]
	}
	if len(payload) > 0 {
		_, _ = w.buffer.Write(payload)
		w.remaining -= len(payload)
	}
	return original, nil
}
