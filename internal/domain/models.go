package domain

import (
	"encoding/json"
	"time"
)

type Principal struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	Roles       []string  `json:"roles"`
	CSRFHash    []byte    `json:"-"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

func (p Principal) HasRole(roles ...string) bool {
	for _, want := range roles {
		for _, actual := range p.Roles {
			if actual == want {
				return true
			}
		}
	}
	return false
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName"`
	PasswordHash string    `json:"-"`
	Roles        []string  `json:"roles"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"createdAt"`
}

type AuditEvent struct {
	ActorUserID  string
	Action       string
	ResourceType string
	ResourceID   string
	Outcome      string
	IPAddress    string
	Metadata     map[string]any
}

type EnrollmentResult struct {
	NodeID      string `json:"nodeId"`
	AgentToken  string `json:"agentToken"`
	TaskKey     string `json:"taskKey"`
	ConnectPath string `json:"connectPath"`
}

type InventoryRuntime struct {
	Kind      string         `json:"kind"`
	RootPath  string         `json:"rootPath"`
	Version   string         `json:"version"`
	Config    map[string]any `json:"config"`
	Inventory map[string]any `json:"inventory"`
}

type AgentMessage struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type AgentTask struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
	Attempt   int             `json:"attempt"`
	CreatedAt time.Time       `json:"createdAt"`
}

type Job struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Status      string          `json:"status"`
	Payload     json.RawMessage `json:"payload"`
	DryRun      bool            `json:"dryRun"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"maxAttempts"`
	RunAfter    time.Time       `json:"runAfter"`
	CreatedBy   string          `json:"createdBy,omitempty"`
}
