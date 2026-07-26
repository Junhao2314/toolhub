package agentclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type taskRecord struct {
	Kind          string          `json:"kind"`
	PayloadDigest string          `json:"payloadDigest"`
	Status        string          `json:"status"`
	Result        json.RawMessage `json:"result"`
	StartedAt     time.Time       `json:"startedAt,omitempty"`
	CompletedAt   time.Time       `json:"completedAt,omitempty"`
}

type taskHistory struct {
	mu      sync.Mutex
	path    string
	Records map[string]taskRecord `json:"records"`
}

func loadHistory(dataDir string) *taskHistory {
	history := &taskHistory{path: filepath.Join(dataDir, "task-history.json"), Records: map[string]taskRecord{}}
	body, err := os.ReadFile(history.path)
	if err == nil {
		_ = json.Unmarshal(body, history)
	}
	if history.Records == nil {
		history.Records = map[string]taskRecord{}
	}
	return history
}

func (h *taskHistory) get(id string) (taskRecord, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	record, ok := h.Records[id]
	return record, ok
}

func (h *taskHistory) put(id string, record taskRecord) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Records[id] = record
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	for key, value := range h.Records {
		if !value.CompletedAt.IsZero() && value.CompletedAt.Before(cutoff) {
			delete(h.Records, key)
		}
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(h.path)
	temporary, err := os.CreateTemp(directory, ".task-history-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporaryPath, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, h.path); err != nil {
		return err
	}
	removeTemporary = false
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}
