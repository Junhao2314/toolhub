package domain

import (
	"encoding/json"
	"time"
)

const (
	RuntimeClaude      = "claude"
	RuntimeCodex       = "codex"
	RuntimeHermes      = "hermes"
	RuntimeSharedRelay = "shared-relay"
)

type Account struct {
	Username                  string    `json:"username"`
	PasswordHash              string    `json:"-"`
	PasswordChangeRecommended bool      `json:"passwordChangeRecommended"`
	PasswordChangedAt         time.Time `json:"passwordChangedAt"`
	CreatedAt                 time.Time `json:"createdAt"`
	UpdatedAt                 time.Time `json:"updatedAt"`
}

type Principal struct {
	Username                  string    `json:"username"`
	PasswordChangeRecommended bool      `json:"passwordChangeRecommended"`
	CSRFHash                  []byte    `json:"-"`
	ExpiresAt                 time.Time `json:"expiresAt"`
}

type Node struct {
	ID                      string     `json:"id"`
	Name                    string     `json:"name"`
	Kind                    string     `json:"kind"`
	SaltMinionID            string     `json:"saltMinionId,omitempty"`
	ManagedUsernameOverride string     `json:"managedUsernameOverride,omitempty"`
	Status                  string     `json:"status"`
	SaltVersion             string     `json:"saltVersion,omitempty"`
	LastSeenAt              *time.Time `json:"lastSeenAt,omitempty"`
	ArchivedAt              *time.Time `json:"archivedAt,omitempty"`
	CreatedAt               time.Time  `json:"createdAt"`
	UpdatedAt               time.Time  `json:"updatedAt"`
}

type Target struct {
	ID                     string          `json:"id"`
	TargetKey              string          `json:"targetKey"`
	NodeID                 string          `json:"nodeId"`
	NodeName               string          `json:"nodeName"`
	NodeKind               string          `json:"nodeKind"`
	SaltMinionID           string          `json:"saltMinionId,omitempty"`
	Runtime                string          `json:"runtime"`
	ManagedUsername        string          `json:"managedUsername"`
	Writable               bool            `json:"writable"`
	Health                 string          `json:"health"`
	DesiredRevision        int64           `json:"desiredRevision"`
	TargetRevision         string          `json:"targetRevision,omitempty"`
	DriftSummary           json.RawMessage `json:"driftSummary,omitempty"`
	LastScannedAt          *time.Time      `json:"lastScannedAt,omitempty"`
	LastReconciledAt       *time.Time      `json:"lastReconciledAt,omitempty"`
	ErrorCode              string          `json:"errorCode,omitempty"`
	ErrorReason            string          `json:"errorReason,omitempty"`
	RelayFailureCount      int             `json:"relayFailureCount"`
	RelayNextRetryAt       *time.Time      `json:"relayNextRetryAt,omitempty"`
	RelaySuspended         bool            `json:"relaySuspended"`
	RelayLastMemberCheckAt *time.Time      `json:"relayLastMemberCheckAt,omitempty"`
	RelayMemberStatuses    json.RawMessage `json:"relayMemberStatuses"`
}

type Skill struct {
	ID                 string          `json:"id"`
	Slug               string          `json:"slug"`
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	SourceKind         string          `json:"sourceKind"`
	SourceURL          string          `json:"sourceUrl,omitempty"`
	SourceCommit       string          `json:"sourceCommit,omitempty"`
	CurrentVersionID   string          `json:"currentVersionId"`
	CurrentSHA256      string          `json:"currentSha256"`
	CurrentContentHash string          `json:"currentContentHash"`
	Manifest           json.RawMessage `json:"manifest"`
	ScanReport         json.RawMessage `json:"scanReport"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

type SkillVersion struct {
	ID           string          `json:"id"`
	SkillID      string          `json:"skillId"`
	ArtifactID   string          `json:"artifactId"`
	SourceCommit string          `json:"sourceCommit,omitempty"`
	SHA256       string          `json:"sha256"`
	Provenance   json.RawMessage `json:"provenance"`
	Manifest     json.RawMessage `json:"manifest"`
	ScanReport   json.RawMessage `json:"scanReport"`
	CreatedAt    time.Time       `json:"createdAt"`
}

type MCPServer struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Revision    int64     `json:"revision"`
	Transport   string    `json:"transport"`
	Command     string    `json:"command,omitempty"`
	Args        []string  `json:"args,omitempty"`
	URL         string    `json:"url,omitempty"`
	EnvKeys     []string  `json:"envKeys"`
	HeaderKeys  []string  `json:"headerKeys"`
	ContentHash string    `json:"contentHash"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Profile struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	Revision     int64     `json:"revision"`
	SkillIDs     []string  `json:"skillIds"`
	MCPServerIDs []string  `json:"mcpServerIds"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Operation struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	SourceID        string          `json:"sourceId,omitempty"`
	IdempotencyKey  string          `json:"idempotencyKey,omitempty"`
	Metadata        json.RawMessage `json:"metadata"`
	ErrorCode       string          `json:"errorCode,omitempty"`
	ErrorReason     string          `json:"errorReason,omitempty"`
	CancelRequested bool            `json:"cancelRequested"`
	CreatedAt       time.Time       `json:"createdAt"`
	StartedAt       *time.Time      `json:"startedAt,omitempty"`
	FinishedAt      *time.Time      `json:"finishedAt,omitempty"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

type OperationTarget struct {
	ID                string          `json:"id"`
	OperationID       string          `json:"operationId"`
	TargetID          string          `json:"targetId"`
	TargetKey         string          `json:"targetKey"`
	Status            string          `json:"status"`
	Attempt           int             `json:"attempt"`
	PendingRerun      bool            `json:"pendingRerun"`
	BridgeOperationID string          `json:"bridgeOperationId,omitempty"`
	SaltJID           string          `json:"saltJid,omitempty"`
	Request           json.RawMessage `json:"-"`
	Result            json.RawMessage `json:"result,omitempty"`
	ErrorCode         string          `json:"errorCode,omitempty"`
	ErrorReason       string          `json:"errorReason,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
	StartedAt         *time.Time      `json:"startedAt,omitempty"`
	FinishedAt        *time.Time      `json:"finishedAt,omitempty"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

type Settings struct {
	ManagedUsername        string    `json:"managedUsername"`
	UpdateCron             string    `json:"updateCron"`
	Timezone               string    `json:"timezone"`
	RelayPort              int       `json:"relayPort"`
	RelayIntentionalPaused bool      `json:"relayIntentionalPaused"`
	UpdatedAt              time.Time `json:"updatedAt"`
}

type DesiredSnapshot struct {
	ID                    string          `json:"id"`
	TargetID              string          `json:"targetId"`
	Revision              int64           `json:"revision"`
	SourceKind            string          `json:"sourceKind"`
	SourceID              string          `json:"sourceId,omitempty"`
	ProfileRevision       int64           `json:"profileRevision,omitempty"`
	ManifestSchemaVersion int             `json:"manifestSchemaVersion"`
	ManifestHash          string          `json:"manifestHash"`
	Manifest              json.RawMessage `json:"manifest"`
	CreatedAt             time.Time       `json:"createdAt"`
}

type Backup struct {
	ID                string          `json:"id"`
	BridgeBackupID    string          `json:"bridgeBackupId"`
	TargetID          string          `json:"targetId"`
	SourceOperationID string          `json:"sourceOperationId,omitempty"`
	TargetRevision    string          `json:"targetRevision"`
	ManifestHash      string          `json:"manifestHash,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
	ExpiresAt         time.Time       `json:"expiresAt"`
	Metadata          json.RawMessage `json:"metadata"`
}

type AuditEvent struct {
	Action       string         `json:"action"`
	ResourceType string         `json:"resourceType"`
	ResourceID   string         `json:"resourceId,omitempty"`
	Outcome      string         `json:"outcome"`
	IPAddress    string         `json:"ipAddress,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type AIProvider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	APIKey  string `json:"-"`
}

func IsWritableRuntime(runtime string) bool {
	return runtime == RuntimeClaude || runtime == RuntimeCodex || runtime == RuntimeSharedRelay
}
