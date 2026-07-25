package agentclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type taskRecord struct {
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result"`
	CompletedAt time.Time       `json:"completedAt"`
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
		if value.CompletedAt.Before(cutoff) {
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
	temporary := h.path + ".new"
	if err := os.WriteFile(temporary, body, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, h.path)
}
