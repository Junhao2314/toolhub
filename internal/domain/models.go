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
	ID                string    `json:"id"`
	CurrentRevisionID string    `json:"currentRevisionId"`
	Name              string    `json:"name"`
	Description       string    `json:"description,omitempty"`
	Revision          int64     `json:"revision"`
	Transport         string    `json:"transport"`
	Command           string    `json:"command,omitempty"`
	Args              []string  `json:"args,omitempty"`
	URL               string    `json:"url,omitempty"`
	EnvKeys           []string  `json:"envKeys"`
	HeaderKeys        []string  `json:"headerKeys"`
	ContentHash       string    `json:"contentHash"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type MCPRevision struct {
	ID          string          `json:"id"`
	ServerID    string          `json:"serverId"`
	Revision    int64           `json:"revision"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Transport   string          `json:"transport"`
	Command     string          `json:"command,omitempty"`
	Args        []string        `json:"args,omitempty"`
	URL         string          `json:"url,omitempty"`
	EnvKeys     []string        `json:"envKeys"`
	HeaderKeys  []string        `json:"headerKeys"`
	ContentHash string          `json:"contentHash"`
	Provenance  json.RawMessage `json:"provenance"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type ProfileSkillPin struct {
	SkillID     string `json:"skillId"`
	VersionID   string `json:"versionId"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	ContentHash string `json:"contentHash"`
	Current     bool   `json:"current"`
}

type ProfileMCPPin struct {
	ServerID    string   `json:"serverId"`
	RevisionID  string   `json:"revisionId"`
	Revision    int64    `json:"revision"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Transport   string   `json:"transport"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	URL         string   `json:"url,omitempty"`
	EnvKeys     []string `json:"envKeys"`
	HeaderKeys  []string `json:"headerKeys"`
	ContentHash string   `json:"contentHash"`
	Current     bool     `json:"current"`
}

type ProfileMCPGovernance struct {
	ServerID                   string `json:"serverId"`
	MCPRevisionID              string `json:"mcpRevisionId"`
	AcceptedContractRevisionID string `json:"acceptedContractRevisionId,omitempty"`
	VisibilityMode             string `json:"visibilityMode"`
}

type ProfileToolRule struct {
	ToolID      string   `json:"toolId"`
	Visible     bool     `json:"visible"`
	Decision    string   `json:"decision"`
	ReasonCodes []string `json:"reasonCodes,omitempty"`
}

type Profile struct {
	ID                    string                 `json:"id"`
	CurrentRevisionID     string                 `json:"currentRevisionId"`
	PublishedRevisionID   string                 `json:"publishedRevisionId,omitempty"`
	PublishedRevision     int64                  `json:"publishedRevision,omitempty"`
	PublishedAt           *time.Time             `json:"publishedAt,omitempty"`
	Name                  string                 `json:"name"`
	Description           string                 `json:"description,omitempty"`
	ClientKind            string                 `json:"clientKind,omitempty"`
	Category              string                 `json:"category,omitempty"`
	Variant               string                 `json:"variant,omitempty"`
	MigrationState        string                 `json:"migrationState,omitempty"`
	Revision              int64                  `json:"revision"`
	CanonicalHash         string                 `json:"canonicalHash"`
	PendingBindings       bool                   `json:"pendingBindings"`
	ArchivedAt            *time.Time             `json:"archivedAt,omitempty"`
	SkillIDs              []string               `json:"skillIds"`
	MCPServerIDs          []string               `json:"mcpServerIds"`
	Skills                []ProfileSkillPin      `json:"skills"`
	MCPServers            []ProfileMCPPin        `json:"mcpServers"`
	MCPGovernance         []ProfileMCPGovernance `json:"mcpGovernance"`
	ToolRules             []ProfileToolRule      `json:"toolRules"`
	EffectiveVisibleCount int                    `json:"effectiveVisibleCount"`
	CreatedAt             time.Time              `json:"createdAt"`
	UpdatedAt             time.Time              `json:"updatedAt"`
}

type ProfileRevision struct {
	ID              string                 `json:"id"`
	ProfileID       string                 `json:"profileId"`
	Revision        int64                  `json:"revision"`
	Name            string                 `json:"name"`
	Description     string                 `json:"description,omitempty"`
	ClientKind      string                 `json:"clientKind,omitempty"`
	Category        string                 `json:"category,omitempty"`
	Variant         string                 `json:"variant,omitempty"`
	MigrationState  string                 `json:"migrationState,omitempty"`
	CanonicalHash   string                 `json:"canonicalHash"`
	PendingBindings bool                   `json:"pendingBindings"`
	ArchivedRestore bool                   `json:"archivedRestore"`
	Skills          []ProfileSkillPin      `json:"skills"`
	MCPServers      []ProfileMCPPin        `json:"mcpServers"`
	MCPGovernance   []ProfileMCPGovernance `json:"mcpGovernance"`
	ToolRules       []ProfileToolRule      `json:"toolRules"`
	CreatedAt       time.Time              `json:"createdAt"`
}

// RelayConfigurationRevision is an immutable ordered set of MCP revisions
// consumed by the one shared local relay. It contains pins only, never secret
// values or editable server configuration.
type RelayConfigurationRevision struct {
	ID            string                           `json:"id"`
	Revision      int64                            `json:"revision"`
	CanonicalHash string                           `json:"canonicalHash"`
	MCPServers    []RelayConfigurationMCPServerPin `json:"mcpServers"`
	Metadata      map[string]any                   `json:"metadata,omitempty"`
	CreatedAt     time.Time                        `json:"createdAt"`
}

type RelayConfigurationMCPServerPin struct {
	ServerID      string `json:"serverId"`
	MCPRevisionID string `json:"mcpRevisionId"`
	Position      int    `json:"position"`
}

// ObservedContractRevision stores normalized tool definitions, not call
// arguments, results, prompts, or raw transport errors.
type ObservedContractRevision struct {
	ID                 string          `json:"id"`
	ServerID           string          `json:"serverId"`
	Revision           int64           `json:"revision"`
	CanonicalHash      string          `json:"canonicalHash"`
	NormalizedContract json.RawMessage `json:"normalizedContract"`
	CreatedAt          time.Time       `json:"createdAt"`
}

type ContractTool struct {
	ID           string          `json:"id"`
	ServerID     string          `json:"serverId"`
	Name         string          `json:"name"`
	Position     int             `json:"position"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Annotations  json.RawMessage `json:"annotations"`
	Presentation json.RawMessage `json:"presentation"`
}

type PublishedProfile struct {
	ProfileID         string    `json:"profileId"`
	ProfileRevisionID string    `json:"profileRevisionId"`
	ClientKind        string    `json:"clientKind"`
	Category          string    `json:"category"`
	Variant           string    `json:"variant"`
	PublishedAt       time.Time `json:"publishedAt"`
}

type GlobalPolicyRevision struct {
	ID                   string            `json:"id"`
	Revision             int64             `json:"revision"`
	CanonicalHash        string            `json:"canonicalHash"`
	CatalogVersion       int               `json:"catalogVersion"`
	ExplicitOverrides    map[string]string `json:"explicitOverrides,omitempty"`
	UnclassifiedMutating string            `json:"unclassifiedMutating"`
	ReviewedReadOnly     string            `json:"reviewedReadOnly"`
	CreatedAt            time.Time         `json:"createdAt"`
}

type ToolRule struct {
	ProfileRevisionID string   `json:"profileRevisionId"`
	ToolID            string   `json:"toolId"`
	Visible           bool     `json:"visible"`
	Decision          string   `json:"decision"`
	ReasonCodes       []string `json:"reasonCodes,omitempty"`
}

type DailyToolAggregate struct {
	Day               string `json:"day"`
	ProfileID         string `json:"profileId,omitempty"`
	ProfileRevisionID string `json:"profileRevisionId,omitempty"`
	ServerID          string `json:"serverId,omitempty"`
	ToolID            string `json:"toolId,omitempty"`
	ClientKind        string `json:"clientKind"`
	Decision          string `json:"decision"`
	Outcome           string `json:"outcome"`
	ErrorClass        string `json:"errorClass,omitempty"`
	CallCount         int64  `json:"callCount"`
	ErrorCount        int64  `json:"errorCount"`
	DurationBucket    string `json:"durationBucket,omitempty"`
}

type PendingSecretBinding struct {
	ProfileRevisionID string     `json:"profileRevisionId"`
	MCPRevisionID     string     `json:"mcpRevisionId"`
	Namespace         string     `json:"namespace"`
	Key               string     `json:"key"`
	SlotHash          string     `json:"slotHash"`
	Bound             bool       `json:"bound"`
	BoundAt           *time.Time `json:"boundAt,omitempty"`
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
	DependsOnTargetID string          `json:"dependsOnTargetId,omitempty"`
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
