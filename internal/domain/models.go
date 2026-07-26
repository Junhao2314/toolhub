package domain

import (
	"encoding/json"
	"time"
)

const (
	RuntimeCodex    = "codex"
	RuntimeClaude   = "claude"
	RuntimeHermes   = "hermes"
	RuntimeGrok     = "grok"
	RuntimeOpenClaw = "openclaw"
	RuntimeShared   = "shared"
)

func IsConsumerRuntime(kind string) bool {
	switch kind {
	case RuntimeCodex, RuntimeClaude, RuntimeHermes, RuntimeGrok, RuntimeOpenClaw:
		return true
	default:
		return false
	}
}

func IsSkillRuntime(kind string) bool {
	return IsConsumerRuntime(kind) || kind == RuntimeShared
}

func IsMCPRuntime(kind string) bool {
	return IsConsumerRuntime(kind)
}

type Principal struct {
	ID                        string    `json:"id"`
	Username                  string    `json:"username"`
	Email                     string    `json:"email"`
	DisplayName               string    `json:"displayName"`
	Roles                     []string  `json:"roles"`
	PasswordChangeRecommended bool      `json:"passwordChangeRecommended"`
	CSRFHash                  []byte    `json:"-"`
	ExpiresAt                 time.Time `json:"expiresAt"`
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
	ID                        string    `json:"id"`
	Username                  string    `json:"username"`
	Email                     string    `json:"email"`
	DisplayName               string    `json:"displayName"`
	PasswordHash              string    `json:"-"`
	Roles                     []string  `json:"roles"`
	Disabled                  bool      `json:"disabled"`
	PasswordChangeRecommended bool      `json:"passwordChangeRecommended"`
	CreatedAt                 time.Time `json:"createdAt"`
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
	Kind       string          `json:"kind"`
	RootPath   string          `json:"rootPath"`
	Version    string          `json:"version"`
	Config     map[string]any  `json:"config"`
	Inventory  map[string]any  `json:"inventory"`
	MCPServers []MCPDescriptor `json:"mcpServers,omitempty"`
}

type AgentInventory struct {
	Runtimes      []InventoryRuntime      `json:"runtimes"`
	SharedSources []SharedSourceInventory `json:"sharedSources,omitempty"`
}

type MCPDescriptor struct {
	Name              string   `json:"name"`
	Identity          string   `json:"identity"`
	Transport         string   `json:"transport"`
	Command           string   `json:"command,omitempty"`
	Args              []string `json:"args,omitempty"`
	URL               string   `json:"url,omitempty"`
	EnvKeys           []string `json:"envKeys"`
	HeaderKeys        []string `json:"headerKeys,omitempty"`
	ConfigFingerprint string   `json:"configFingerprint"`
	SecretFingerprint string   `json:"secretFingerprint,omitempty"`
}

type SharedSourceInventory struct {
	Name              string                     `json:"name"`
	Mode              string                     `json:"mode"`
	AutoSync          bool                       `json:"autoSync"`
	SkillsRoot        string                     `json:"skillsRoot"`
	MCPManifestPath   string                     `json:"mcpManifestPath"`
	ConfigFingerprint string                     `json:"configFingerprint"`
	SourceFingerprint string                     `json:"sourceFingerprint"`
	Status            string                     `json:"status"`
	LastError         string                     `json:"lastError,omitempty"`
	Skills            []SharedSkillInventory     `json:"skills"`
	MCPServers        []SharedMCPServerInventory `json:"mcpServers"`
	Consumers         []SharedConsumerInventory  `json:"consumers"`
}

type SharedMCPServerInventory struct {
	Descriptor  MCPDescriptor `json:"descriptor"`
	Description string        `json:"description,omitempty"`
	Enabled     bool          `json:"enabled"`
}

type SharedSkillInventory struct {
	Name               string `json:"name"`
	SourcePath         string `json:"sourcePath"`
	ResolvedSourcePath string `json:"resolvedSourcePath"`
	SHA256             string `json:"sha256,omitempty"`
	EntryType          string `json:"entryType"`
	Managed            bool   `json:"managed"`
	State              string `json:"state"`
	LastError          string `json:"lastError,omitempty"`
}

type SharedConsumerInventory struct {
	Kind                string                      `json:"kind"`
	SkillsPath          string                      `json:"skillsPath,omitempty"`
	MCPPath             string                      `json:"mcpPath,omitempty"`
	MCPFormat           string                      `json:"mcpFormat,omitempty"`
	InheritsFrom        string                      `json:"inheritsFrom,omitempty"`
	SkillsEnabled       bool                        `json:"skillsEnabled"`
	MCPEnabled          bool                        `json:"mcpEnabled"`
	ExpectedFingerprint string                      `json:"expectedFingerprint,omitempty"`
	ActualFingerprint   string                      `json:"actualFingerprint,omitempty"`
	State               string                      `json:"state"`
	LastError           string                      `json:"lastError,omitempty"`
	SkillLinks          []SharedSkillLinkInventory  `json:"skillLinks"`
	MCPBindings         []SharedMCPBindingInventory `json:"mcpBindings"`
}

type SharedSkillLinkInventory struct {
	SkillName          string `json:"skillName"`
	SourcePath         string `json:"sourcePath"`
	ResolvedSourcePath string `json:"resolvedSourcePath"`
	TargetPath         string `json:"targetPath"`
	ExpectedTarget     string `json:"expectedTarget"`
	ActualTarget       string `json:"actualTarget,omitempty"`
	Managed            bool   `json:"managed"`
	State              string `json:"state"`
	LastError          string `json:"lastError,omitempty"`
}

type SharedMCPBindingInventory struct {
	ServerName         string   `json:"serverName"`
	DesiredFingerprint string   `json:"desiredFingerprint,omitempty"`
	ActualFingerprint  string   `json:"actualFingerprint,omitempty"`
	EnvKeys            []string `json:"envKeys,omitempty"`
	HeaderKeys         []string `json:"headerKeys,omitempty"`
	Enabled            bool     `json:"enabled"`
	Missing            bool     `json:"missing"`
	Drift              bool     `json:"drift"`
	State              string   `json:"state"`
	LastError          string   `json:"lastError,omitempty"`
}

type SharedSyncResult struct {
	Source    SharedSourceInventory `json:"source"`
	Changed   bool                  `json:"changed"`
	DryRun    bool                  `json:"dryRun"`
	Actions   []string              `json:"actions,omitempty"`
	Conflicts []string              `json:"conflicts,omitempty"`
}

type MCPCaptureRequest struct {
	Token    string `json:"token"`
	Runtime  string `json:"runtime"`
	Name     string `json:"name"`
	Identity string `json:"identity"`
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
