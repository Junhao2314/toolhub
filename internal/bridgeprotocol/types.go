package bridgeprotocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ManifestSchemaVersion   = 1
	ManifestSchemaVersionV2 = 2
	MaxRequestBytes         = 64 << 20

	RuntimeClaude      = "claude"
	RuntimeCodex       = "codex"
	RuntimeHermes      = "hermes"
	RuntimeSharedRelay = "shared-relay"

	NodeKindLocal = "local"
	NodeKindSalt  = "salt"

	OperationQueued    = "queued"
	OperationRunning   = "running"
	OperationSucceeded = "succeeded"
	OperationPartial   = "partial"
	OperationFailed    = "failed"
	OperationCancelled = "cancelled"

	HealthHealthy     = "healthy"
	HealthDrifted     = "drifted"
	HealthRepairing   = "repairing"
	HealthBlocked     = "blocked"
	HealthUnavailable = "unavailable"
)

const (
	ErrAuthentication       = "authentication_failed"
	ErrReplay               = "nonce_replay"
	ErrExpiredRequest       = "request_expired"
	ErrIdempotencyConflict  = "idempotency_conflict"
	ErrInvalidRequest       = "invalid_request"
	ErrUnsupportedOperation = "unsupported_operation"
	ErrRevisionConflict     = "revision_conflict"
	ErrProtectedScope       = "protected_scope"
	ErrTargetUnavailable    = "target_unavailable"
	ErrManagedUserMissing   = "managed_user_missing"
	ErrSaltVersion          = "salt_version_incompatible"
	ErrSaltJobMissing       = "salt_job_missing"
	ErrMCPMMissing          = "mcpm_missing"
	ErrMCPMIncompatible     = "mcpm_incompatible"
	ErrRelayPortConflict    = "relay_port_conflict"
	ErrRelayUnhealthy       = "relay_unhealthy"
	ErrAtomicWrite          = "atomic_write_failed"
	ErrBackup               = "backup_failed"
	ErrHermesReadOnly       = "hermes_read_only"
)

var (
	slugPattern            = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	shaPattern             = regexp.MustCompile(`^[a-f0-9]{64}$`)
	managedUsernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

type apiErrorClass struct {
	message   string
	retryable bool
}

var boundedAPIErrorClasses = map[string]apiErrorClass{
	ErrAuthentication:       {message: "Bridge authentication failed"},
	ErrReplay:               {message: "Bridge request nonce was already used"},
	ErrExpiredRequest:       {message: "Bridge request expired"},
	ErrIdempotencyConflict:  {message: "Idempotency key conflicts with an existing request"},
	ErrInvalidRequest:       {message: "Request is invalid"},
	ErrUnsupportedOperation: {message: "Operation is not supported"},
	ErrRevisionConflict:     {message: "Runtime revision conflicts with the request"},
	ErrProtectedScope:       {message: "Protected scope cannot be modified"},
	ErrTargetUnavailable:    {message: "Target is unavailable", retryable: true},
	ErrManagedUserMissing:   {message: "Managed user is unavailable"},
	ErrSaltVersion:          {message: "Salt version is incompatible"},
	ErrSaltJobMissing:       {message: "Salt job result is unavailable", retryable: true},
	ErrMCPMMissing:          {message: "mcpm runtime is unavailable"},
	ErrMCPMIncompatible:     {message: "mcpm runtime is incompatible"},
	ErrRelayPortConflict:    {message: "Relay port conflicts with another process"},
	ErrRelayUnhealthy:       {message: "Relay runtime is unhealthy", retryable: true},
	ErrAtomicWrite:          {message: "Atomic write failed"},
	ErrBackup:               {message: "Backup operation failed"},
	ErrHermesReadOnly:       {message: "Hermes target is read-only"},
}

// BoundedAPIError reduces an error to a public code with a fixed message and
// retry policy. It intentionally drops untrusted messages and details.
func BoundedAPIError(err error, fallbackCode string) *APIError {
	if err == nil {
		return nil
	}
	class, ok := boundedAPIErrorClasses[fallbackCode]
	if !ok {
		fallbackCode = ErrInvalidRequest
		class = boundedAPIErrorClasses[fallbackCode]
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if candidate, exists := boundedAPIErrorClasses[apiErr.Code]; exists {
			fallbackCode, class = apiErr.Code, candidate
		}
	}
	return &APIError{Code: fallbackCode, Message: class.message, Retryable: class.retryable}
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type Target struct {
	ID              string `json:"id"`
	NodeID          string `json:"nodeId"`
	NodeKind        string `json:"nodeKind"`
	SaltMinionID    string `json:"saltMinionId,omitempty"`
	Runtime         string `json:"runtime"`
	ManagedUsername string `json:"managedUsername"`
	ManagedHome     string `json:"managedHome,omitempty"`
}

func (t Target) Validate(write bool) error {
	if uuid.Validate(t.ID) != nil || uuid.Validate(t.NodeID) != nil {
		return errors.New("target id and node id must be UUIDs")
	}
	if t.NodeKind != NodeKindLocal && t.NodeKind != NodeKindSalt {
		return errors.New("target node kind must be local or salt")
	}
	if t.NodeKind == NodeKindSalt && strings.TrimSpace(t.SaltMinionID) == "" {
		return errors.New("Salt target requires a minion id")
	}
	if err := ValidateManagedUsername(t.ManagedUsername); err != nil {
		return err
	}
	if t.ManagedHome != "" && (!path.IsAbs(t.ManagedHome) || path.Clean(t.ManagedHome) != t.ManagedHome || t.ManagedHome == "/" || strings.ContainsRune(t.ManagedHome, '\x00')) {
		return errors.New("target managed home must be a canonical absolute path")
	}
	switch t.Runtime {
	case RuntimeClaude, RuntimeCodex:
	case RuntimeHermes:
		if write {
			return &APIError{Code: ErrHermesReadOnly, Message: "Hermes targets are read-only"}
		}
	case RuntimeSharedRelay:
		if t.NodeKind != NodeKindLocal {
			return errors.New("shared relay is available only on the local node")
		}
	default:
		return fmt.Errorf("unsupported runtime %q", t.Runtime)
	}
	return nil
}

type SkillMember struct {
	MemberID    string `json:"memberId"`
	SkillID     string `json:"skillId"`
	VersionID   string `json:"versionId"`
	Slug        string `json:"slug"`
	SHA256      string `json:"sha256"`
	ContentHash string `json:"contentHash"`
}

type MCPMember struct {
	MemberID    string            `json:"memberId"`
	ServerID    string            `json:"serverId"`
	Revision    int64             `json:"revision"`
	Name        string            `json:"name"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	URL         string            `json:"url,omitempty"`
	EnvRefs     map[string]string `json:"envRefs,omitempty"`
	HeaderRefs  map[string]string `json:"headerRefs,omitempty"`
	ContentHash string            `json:"contentHash"`
}

type DesiredManifest struct {
	SchemaVersion    int                      `json:"schemaVersion"`
	Target           Target                   `json:"target"`
	ProfileID        string                   `json:"profileId,omitempty"`
	ProfileRevision  int64                    `json:"profileRevision,omitempty"`
	Skills           []SkillMember            `json:"skills"`
	MCPServers       []MCPMember              `json:"mcpServers"`
	ManagedMemberIDs []string                 `json:"managedMemberIds"`
	RelayPort        int                      `json:"relayPort,omitempty"`
	RelayGovernance  *RelayGovernanceManifest `json:"relayGovernance,omitempty"`
}

func (m *DesiredManifest) Normalize() {
	if m.SchemaVersion == 0 {
		m.SchemaVersion = ManifestSchemaVersion
	}
	if m.Skills == nil {
		m.Skills = []SkillMember{}
	}
	if m.MCPServers == nil {
		m.MCPServers = []MCPMember{}
	}
	if m.ManagedMemberIDs == nil {
		m.ManagedMemberIDs = []string{}
	}
	sort.Slice(m.Skills, func(i, j int) bool { return m.Skills[i].MemberID < m.Skills[j].MemberID })
	sort.Slice(m.MCPServers, func(i, j int) bool { return m.MCPServers[i].MemberID < m.MCPServers[j].MemberID })
	sort.Strings(m.ManagedMemberIDs)
}

func (m DesiredManifest) Validate(write bool) error {
	m.Normalize()
	if m.SchemaVersion != ManifestSchemaVersion && m.SchemaVersion != ManifestSchemaVersionV2 {
		return fmt.Errorf("unsupported manifest schema version %d", m.SchemaVersion)
	}
	if m.SchemaVersion == ManifestSchemaVersionV2 {
		if m.Target.Runtime != RuntimeSharedRelay || m.RelayGovernance == nil {
			return errors.New("Manifest v2 governance is valid only for shared relay targets")
		}
		if err := m.RelayGovernance.Validate(); err != nil {
			return err
		}
	} else if m.RelayGovernance != nil {
		return errors.New("Manifest v1 cannot contain relay governance")
	}
	if err := m.Target.Validate(write); err != nil {
		return err
	}
	if m.ProfileRevision < 0 {
		return errors.New("profile revision cannot be negative")
	}
	if m.Target.Runtime == RuntimeSharedRelay && (m.RelayPort < 1 || m.RelayPort > 65535) {
		return errors.New("shared relay target requires a valid relay port")
	}
	if m.Target.Runtime != RuntimeSharedRelay && len(m.MCPServers) > 0 && m.Target.NodeKind == NodeKindLocal {
		return errors.New("local MCP servers belong to local/shared-relay")
	}
	if m.Target.Runtime == RuntimeSharedRelay && len(m.Skills) > 0 {
		return errors.New("shared relay target cannot contain Skills")
	}
	seenMembers := map[string]bool{}
	for _, skill := range m.Skills {
		if !validID(skill.MemberID) || !validID(skill.SkillID) || !validID(skill.VersionID) {
			return errors.New("Skill member contains an invalid identifier")
		}
		if !slugPattern.MatchString(skill.Slug) || IsProtectedSkillEntry(skill.Slug) {
			return fmt.Errorf("invalid or protected Skill slug %q", skill.Slug)
		}
		if !shaPattern.MatchString(skill.SHA256) || !shaPattern.MatchString(skill.ContentHash) {
			return fmt.Errorf("invalid Skill artifact or content hash for %q", skill.Slug)
		}
		if seenMembers[skill.MemberID] {
			return fmt.Errorf("duplicate managed member %q", skill.MemberID)
		}
		seenMembers[skill.MemberID] = true
	}
	for _, server := range m.MCPServers {
		if !validID(server.MemberID) || !validID(server.ServerID) || server.Revision < 1 {
			return errors.New("MCP member contains an invalid identifier or revision")
		}
		if !slugPattern.MatchString(server.Name) || !shaPattern.MatchString(server.ContentHash) {
			return fmt.Errorf("invalid MCP member %q", server.Name)
		}
		switch server.Transport {
		case "stdio":
			if strings.TrimSpace(server.Command) == "" || server.URL != "" {
				return fmt.Errorf("stdio MCP server %q requires command and forbids URL", server.Name)
			}
		case "http", "sse":
			if strings.TrimSpace(server.URL) == "" || server.Command != "" || len(server.Args) != 0 {
				return fmt.Errorf("network MCP server %q requires URL and forbids command/args", server.Name)
			}
		default:
			return fmt.Errorf("unsupported MCP transport %q", server.Transport)
		}
		for key, ref := range mergeRefs(server.EnvRefs, server.HeaderRefs) {
			if strings.TrimSpace(key) == "" || uuid.Validate(ref) != nil {
				return fmt.Errorf("MCP server %q contains an invalid secret reference", server.Name)
			}
		}
		if seenMembers[server.MemberID] {
			return fmt.Errorf("duplicate managed member %q", server.MemberID)
		}
		seenMembers[server.MemberID] = true
	}
	if len(m.ManagedMemberIDs) != len(seenMembers) {
		return errors.New("managedMemberIds must exactly match Skill and MCP member IDs")
	}
	for _, id := range m.ManagedMemberIDs {
		if !seenMembers[id] {
			return fmt.Errorf("unknown managed member id %q", id)
		}
	}
	return nil
}

func (m DesiredManifest) Canonical() ([]byte, string, error) {
	m.Normalize()
	if err := m.Validate(true); err != nil {
		return nil, "", err
	}
	if m.SchemaVersion == ManifestSchemaVersionV2 && m.RelayGovernance != nil {
		var bundle RoutingBundle
		if err := decodeStrictGovernance(m.RelayGovernance.RoutingBundle, &bundle); err != nil {
			return nil, "", fmt.Errorf("canonicalize relay routing bundle: %w", err)
		}
		canonicalBundle, _, err := bundle.Canonical()
		if err != nil {
			return nil, "", err
		}
		governance := *m.RelayGovernance
		governance.RoutingBundle = canonicalBundle
		m.RelayGovernance = &governance
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

func DecodeManifest(body []byte, write bool) (DesiredManifest, error) {
	if len(body) > MaxRequestBytes {
		return DesiredManifest{}, errors.New("manifest exceeds request limit")
	}
	var manifest DesiredManifest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return DesiredManifest{}, err
	}
	if err := ensureEOF(decoder); err != nil {
		return DesiredManifest{}, err
	}
	if err := manifest.Validate(write); err != nil {
		return DesiredManifest{}, err
	}
	return manifest, nil
}

type DiffItem struct {
	Kind     string `json:"kind"`
	MemberID string `json:"memberId,omitempty"`
	Name     string `json:"name"`
	Reason   string `json:"reason,omitempty"`
}

type Diff struct {
	Add      []DiffItem `json:"add"`
	Replace  []DiffItem `json:"replace"`
	Delete   []DiffItem `json:"delete"`
	Excluded []DiffItem `json:"excluded"`
}

type NodeInfo struct {
	NodeID       string `json:"nodeId"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	SaltMinionID string `json:"saltMinionId,omitempty"`
	Status       string `json:"status"`
	Version      string `json:"version,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
}

type RefreshNodesResponse struct {
	Nodes []NodeInfo `json:"nodes"`
}

type InventoryMember struct {
	ID                string   `json:"id"`
	Kind              string   `json:"kind"`
	Name              string   `json:"name"`
	Slug              string   `json:"slug,omitempty"`
	Description       string   `json:"description,omitempty"`
	ContentHash       string   `json:"contentHash,omitempty"`
	Protected         bool     `json:"protected"`
	Scope             string   `json:"scope,omitempty"`
	Revision          int64    `json:"revision,omitempty"`
	SecretKeys        []string `json:"secretKeys,omitempty"`
	Source            string   `json:"source,omitempty"`
	Provider          string   `json:"provider,omitempty"`
	Category          string   `json:"category,omitempty"`
	Trust             string   `json:"trust,omitempty"`
	Importable        bool     `json:"importable,omitempty"`
	EligibilityReason string   `json:"eligibilityReason,omitempty"`
	Shadowed          bool     `json:"shadowed,omitempty"`
	Builtin           bool     `json:"builtin,omitempty"`
}

type ScanRequest struct {
	Target Target `json:"target"`
}

type ScanResponse struct {
	TargetRevision string            `json:"targetRevision"`
	Members        []InventoryMember `json:"members"`
	Relay          *RelayStatus      `json:"relay,omitempty"`
}

// LocalSkillExportRequest identifies one already-scanned local Skill without
// accepting a filesystem path from the caller.
type LocalSkillExportRequest struct {
	Target           Target `json:"target"`
	ExpectedRevision string `json:"expectedRevision"`
	Name             string `json:"name"`
	ContentHash      string `json:"contentHash"`
}

type LocalSkillExportResponse struct {
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	ContentHash string `json:"contentHash"`
	Archive     []byte `json:"archive"`
}

type LocalSkillBatchItem struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

type LocalSkillBatchExportRequest struct {
	Target           Target                `json:"target"`
	ExpectedRevision string                `json:"expectedRevision"`
	Items            []LocalSkillBatchItem `json:"items"`
}

type LocalSkillBatchExportItem struct {
	ID          string    `json:"id"`
	Name        string    `json:"name,omitempty"`
	Slug        string    `json:"slug,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	ContentHash string    `json:"contentHash,omitempty"`
	Archive     []byte    `json:"archive,omitempty"`
	Source      string    `json:"source,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	Category    string    `json:"category,omitempty"`
	Status      string    `json:"status"`
	Error       *APIError `json:"error,omitempty"`
}

type LocalSkillBatchExportResponse struct {
	TargetRevision string                      `json:"targetRevision"`
	Items          []LocalSkillBatchExportItem `json:"items"`
}

// LocalMCPServerPreview contains only non-secret fields. ContentHash is bound
// to the complete native entry so capture fails if any secret changes.
type LocalMCPServerPreview struct {
	Name        string   `json:"name"`
	Transport   string   `json:"transport"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	URL         string   `json:"url,omitempty"`
	EnvKeys     []string `json:"envKeys"`
	HeaderKeys  []string `json:"headerKeys"`
	ContentHash string   `json:"contentHash"`
}

type LocalMCPPreviewRequest struct {
	Target Target `json:"target"`
}

type LocalMCPPreviewResponse struct {
	TargetRevision string                  `json:"targetRevision"`
	Items          []LocalMCPServerPreview `json:"items"`
}

type LocalMCPCaptureRequest struct {
	Target           Target `json:"target"`
	ExpectedRevision string `json:"expectedRevision"`
	Name             string `json:"name"`
	ContentHash      string `json:"contentHash"`
}

// LocalMCPCaptureResponse is an ephemeral worker-only response. The Bridge
// server must never route this response through its idempotency journal.
type LocalMCPCaptureResponse struct {
	Preview LocalMCPServerPreview `json:"preview"`
	Env     map[string]string     `json:"env,omitempty"`
	Headers map[string]string     `json:"headers,omitempty"`
}

type PreflightRequest struct {
	Target   Target          `json:"target"`
	Manifest DesiredManifest `json:"manifest"`
}

type PreflightResponse struct {
	TargetRevision string `json:"targetRevision"`
	ManifestHash   string `json:"manifestHash"`
	Diff           Diff   `json:"diff"`
}

type Artifact struct {
	VersionID string `json:"versionId"`
	SHA256    string `json:"sha256"`
	Archive   []byte `json:"archive"`
}

type CommitRequest struct {
	OperationID       string            `json:"operationId"`
	OperationKind     string            `json:"operationKind"`
	Target            Target            `json:"target"`
	ExpectedRevision  string            `json:"expectedRevision"`
	Manifest          DesiredManifest   `json:"manifest"`
	Artifacts         []Artifact        `json:"artifacts,omitempty"`
	SecretValues      map[string]string `json:"secretValues,omitempty"`
	BackupID          string            `json:"backupId,omitempty"`
	IntentionalPaused bool              `json:"intentionalPaused,omitempty"`
	FullRelayProbe    bool              `json:"fullRelayProbe,omitempty"`
}

type ReconcileRequest struct {
	OperationID       string            `json:"operationId"`
	Target            Target            `json:"target"`
	Manifest          DesiredManifest   `json:"manifest"`
	Artifacts         []Artifact        `json:"artifacts,omitempty"`
	SecretValues      map[string]string `json:"secretValues,omitempty"`
	IntentionalPaused bool              `json:"intentionalPaused,omitempty"`
	FullRelayProbe    bool              `json:"fullRelayProbe,omitempty"`
}

type TargetResult struct {
	Status         string           `json:"status"`
	Health         string           `json:"health"`
	TargetRevision string           `json:"targetRevision,omitempty"`
	BackupID       string           `json:"backupId,omitempty"`
	Repaired       bool             `json:"repaired,omitempty"`
	Manifest       *DesiredManifest `json:"manifest,omitempty"`
	Relay          *RelayStatus     `json:"relay,omitempty"`
	Details        map[string]any   `json:"details,omitempty"`
	Error          *APIError        `json:"error,omitempty"`
}

type Backup struct {
	ID                string    `json:"id"`
	TargetID          string    `json:"targetId"`
	NodeKind          string    `json:"nodeKind"`
	SaltMinionID      string    `json:"saltMinionId,omitempty"`
	Runtime           string    `json:"runtime"`
	SourceOperationID string    `json:"sourceOperationId,omitempty"`
	Revision          string    `json:"revision"`
	CreatedAt         time.Time `json:"createdAt"`
}

type BackupListResponse struct {
	Items []Backup `json:"items"`
}

type BackupGCRequest struct {
	MaxAgeDays   int `json:"maxAgeDays"`
	MaxPerTarget int `json:"maxPerTarget"`
}

type BackupGCResponse struct {
	Removed          int      `json:"removed"`
	RemovedBackupIDs []string `json:"removedBackupIds"`
}

type RelayActionRequest struct {
	Target            Target           `json:"target"`
	Port              int              `json:"port,omitempty"`
	IntentionalPaused bool             `json:"intentionalPaused,omitempty"`
	Manifest          *DesiredManifest `json:"manifest,omitempty"`
}

type RelayStatus struct {
	State             string              `json:"state"`
	Healthy           bool                `json:"healthy"`
	IntentionalPaused bool                `json:"intentionalPaused"`
	Endpoint          string              `json:"endpoint"`
	FixedPort         int                 `json:"fixedPort"`
	SystemdEnabled    bool                `json:"systemdEnabled"`
	Version           string              `json:"version,omitempty"`
	Contract          string              `json:"contract,omitempty"`
	MemberStatuses    []RelayMemberStatus `json:"memberStatuses,omitempty"`
	ErrorCode         string              `json:"errorCode,omitempty"`
	ErrorReason       string              `json:"errorReason,omitempty"`
}

type RelayCapabilityCounts struct {
	Tools             int `json:"tools"`
	Resources         int `json:"resources"`
	ResourceTemplates int `json:"resourceTemplates"`
	Prompts           int `json:"prompts"`
}

type RelayMemberStatus struct {
	MemberID        string                `json:"memberId"`
	Name            string                `json:"name"`
	Status          string                `json:"status"`
	CapabilityKinds []string              `json:"capabilityKinds"`
	Capabilities    RelayCapabilityCounts `json:"capabilities"`
	CheckedAt       time.Time             `json:"checkedAt"`
	ErrorCode       string                `json:"errorCode,omitempty"`
	ErrorReason     string                `json:"errorReason,omitempty"`
}

type Operation struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Status    string            `json:"status"`
	Targets   []OperationTarget `json:"targets"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

type OperationTarget struct {
	TargetID string        `json:"targetId"`
	Status   string        `json:"status"`
	SaltJID  string        `json:"saltJid,omitempty"`
	Result   *TargetResult `json:"result,omitempty"`
	Error    *APIError     `json:"error,omitempty"`
}

func IsTerminalOperationStatus(status string) bool {
	return status == OperationSucceeded || status == OperationPartial || status == OperationFailed || status == OperationCancelled
}

func IsProtectedSkillEntry(name string) bool {
	name = strings.TrimSpace(strings.ToLower(name))
	return name == "" || name == "." || name == ".." || name == ".system" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "toolhub-")
}

func IsManagedMCPScope(scope string) bool { return strings.EqualFold(strings.TrimSpace(scope), "user") }

func IsSHA256(value string) bool { return shaPattern.MatchString(value) }

func ValidateManagedUsername(value string) error {
	if !managedUsernamePattern.MatchString(value) {
		return errors.New("managed username must be a lowercase Linux username of at most 32 characters")
	}
	return nil
}

func validID(value string) bool {
	value = strings.TrimSpace(value)
	return uuid.Validate(value) == nil
}

func mergeRefs(maps ...map[string]string) map[string]string {
	result := map[string]string{}
	for _, values := range maps {
		for key, value := range values {
			result[key] = value
		}
	}
	return result
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("JSON body must contain one object")
	}
	return err
}
