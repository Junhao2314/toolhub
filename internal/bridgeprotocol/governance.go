package bridgeprotocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/google/uuid"
)

const (
	GovernanceMaxBodyBytes    = 1 << 20
	GovernanceMaxDepth        = 5
	GovernanceMaxRoutingDepth = 64
	GovernanceMaxItems        = 10000
)

type RoutingBundle struct {
	SchemaVersion                int                   `json:"schemaVersion"`
	Mode                         string                `json:"mode"`
	RelayConfigurationRevisionID string                `json:"relayConfigurationRevisionId"`
	RelayConfigurationHash       string                `json:"relayConfigurationHash"`
	GlobalPolicyRevisionID       string                `json:"globalPolicyRevisionId"`
	GlobalPolicyHash             string                `json:"globalPolicyHash"`
	DefaultProfileID             *string               `json:"defaultProfileId"`
	Servers                      []ServerContractDTO   `json:"servers"`
	Profiles                     []PublishedProfileDTO `json:"profiles"`
}

type ServerContractDTO struct {
	ServerID                   string           `json:"serverId"`
	ServerName                 string           `json:"serverName"`
	MCPConfigRevisionID        string           `json:"mcpConfigRevisionId"`
	AcceptedContractRevisionID *string          `json:"acceptedContractRevisionId"`
	AcceptedContractHash       *string          `json:"acceptedContractHash"`
	Tools                      []RoutingToolDTO `json:"tools"`
}

type RoutingToolDTO struct {
	ToolID         string         `json:"toolId"`
	Name           string         `json:"name"`
	InputSchema    map[string]any `json:"inputSchema"`
	OutputSchema   map[string]any `json:"outputSchema"`
	Annotations    map[string]any `json:"annotations"`
	GlobalDecision string         `json:"globalDecision"`
	ReasonCodes    []string       `json:"reasonCodes"`
	Paused         bool           `json:"paused"`
}

type ToolVisibilityOverrideDTO struct {
	ToolID  string `json:"toolId"`
	Visible bool   `json:"visible"`
}

type ToolPolicyRuleDTO struct {
	ToolID   string `json:"toolId"`
	Decision string `json:"decision"`
}

type ProfileServerRoutingDTO struct {
	ServerID                   string                      `json:"serverId"`
	MCPConfigRevisionID        string                      `json:"mcpConfigRevisionId"`
	AcceptedContractRevisionID *string                     `json:"acceptedContractRevisionId"`
	VisibilityMode             string                      `json:"visibilityMode"`
	ToolOverrides              []ToolVisibilityOverrideDTO `json:"toolOverrides"`
	ToolRules                  []ToolPolicyRuleDTO         `json:"toolRules"`
}

type PublishedProfileDTO struct {
	ProfileID           string                    `json:"profileId"`
	ProfileRevisionID   string                    `json:"profileRevisionId"`
	ProfileRevisionHash string                    `json:"profileRevisionHash"`
	ProfileName         string                    `json:"profileName"`
	ClientKind          string                    `json:"clientKind"`
	Servers             []ProfileServerRoutingDTO `json:"servers"`
}

type RelayGovernanceManifest struct {
	RelayConfigurationRevisionID string          `json:"relayConfigurationRevisionId"`
	RelayConfigurationHash       string          `json:"relayConfigurationHash"`
	RoutingBundle                json.RawMessage `json:"routingBundle"`
	RoutingHash                  string          `json:"routingHash"`
}

func (r RelayGovernanceManifest) Validate() error {
	if uuid.Validate(r.RelayConfigurationRevisionID) != nil || !IsSHA256(r.RelayConfigurationHash) || !IsSHA256(r.RoutingHash) || len(r.RoutingBundle) == 0 || len(r.RoutingBundle) > GovernanceMaxBodyBytes {
		return errors.New("relay governance manifest contains invalid identifiers or hashes")
	}
	var bundle RoutingBundle
	if err := decodeStrictGovernance(r.RoutingBundle, &bundle); err != nil {
		return fmt.Errorf("routing bundle: %w", err)
	}
	_, hash, err := bundle.Canonical()
	if err != nil {
		return err
	}
	if hash != r.RoutingHash {
		return errors.New("routing bundle hash does not match governance manifest")
	}
	if bundle.RelayConfigurationRevisionID != r.RelayConfigurationRevisionID || bundle.RelayConfigurationHash != r.RelayConfigurationHash {
		return errors.New("routing bundle relay revision does not match governance manifest")
	}
	return nil
}

func (r RoutingBundle) Validate() error {
	if r.SchemaVersion != 1 || (r.Mode != "compatibility" && r.Mode != "enforced") || uuid.Validate(r.RelayConfigurationRevisionID) != nil || uuid.Validate(r.GlobalPolicyRevisionID) != nil || !IsSHA256(r.RelayConfigurationHash) || !IsSHA256(r.GlobalPolicyHash) {
		return errors.New("routing bundle contains invalid governance metadata")
	}
	if r.DefaultProfileID != nil && uuid.Validate(*r.DefaultProfileID) != nil {
		return errors.New("routing bundle default profile is invalid")
	}
	if len(r.Servers) > 500 || len(r.Profiles) > 100 {
		return errors.New("routing bundle profile limit exceeded")
	}
	serverIDs := map[string]struct{}{}
	serverNames := map[string]struct{}{}
	runtimeToolNames := map[string]struct{}{}
	totalTools, totalRules := 0, 0
	for _, server := range r.Servers {
		if uuid.Validate(server.ServerID) != nil || uuid.Validate(server.MCPConfigRevisionID) != nil || !validGovernanceName(server.ServerName) {
			return errors.New("routing bundle server identity is invalid")
		}
		if _, ok := serverIDs[server.ServerID]; ok {
			return errors.New("routing bundle contains duplicate server id")
		}
		if _, ok := serverNames[server.ServerName]; ok {
			return errors.New("routing bundle contains duplicate server name")
		}
		serverIDs[server.ServerID] = struct{}{}
		serverNames[server.ServerName] = struct{}{}
		if (server.AcceptedContractRevisionID == nil) != (server.AcceptedContractHash == nil) || (server.AcceptedContractRevisionID != nil && uuid.Validate(*server.AcceptedContractRevisionID) != nil) || (server.AcceptedContractHash != nil && !IsSHA256(*server.AcceptedContractHash)) {
			return errors.New("routing bundle server contract pointer is invalid")
		}
		if r.Mode == "enforced" && server.AcceptedContractRevisionID == nil {
			return errors.New("enforced routing bundle requires accepted server contracts")
		}
		toolIDs := map[string]struct{}{}
		toolNames := map[string]struct{}{}
		for _, tool := range server.Tools {
			if uuid.Validate(tool.ToolID) != nil || !validGovernanceName(tool.Name) || !validGovernanceDecision(tool.GlobalDecision) {
				return errors.New("routing bundle tool identity is invalid")
			}
			if _, ok := toolIDs[tool.ToolID]; ok {
				return errors.New("routing bundle contains duplicate tool id")
			}
			if _, ok := toolNames[tool.Name]; ok {
				return errors.New("routing bundle contains duplicate tool name")
			}
			toolIDs[tool.ToolID] = struct{}{}
			toolNames[tool.Name] = struct{}{}
			runtimeName := tool.Name
			if len(r.Servers) > 1 {
				runtimeName = server.ServerName + "_" + tool.Name
			}
			if _, ok := runtimeToolNames[runtimeName]; ok {
				return errors.New("routing bundle contains duplicate runtime tool name")
			}
			runtimeToolNames[runtimeName] = struct{}{}
			totalTools++
		}
	}
	for serverName := range serverNames {
		for otherName := range serverNames {
			if serverName != otherName && strings.HasPrefix(otherName, serverName+"_") {
				return errors.New("routing bundle contains ambiguous server name prefix")
			}
		}
	}
	profileIDs := map[string]struct{}{}
	profileNames := map[string]struct{}{}
	for _, profile := range r.Profiles {
		if uuid.Validate(profile.ProfileID) != nil || uuid.Validate(profile.ProfileRevisionID) != nil || !IsSHA256(profile.ProfileRevisionHash) || !validGovernanceName(profile.ProfileName) || (profile.ClientKind != RuntimeClaude && profile.ClientKind != RuntimeCodex) {
			return errors.New("routing bundle profile identity is invalid")
		}
		if _, ok := profileIDs[profile.ProfileID]; ok {
			return errors.New("routing bundle contains duplicate profile id")
		}
		if _, ok := profileNames[profile.ProfileName]; ok {
			return errors.New("routing bundle contains duplicate profile name")
		}
		profileIDs[profile.ProfileID] = struct{}{}
		profileNames[profile.ProfileName] = struct{}{}
		profileServers := map[string]struct{}{}
		for _, profileServer := range profile.Servers {
			server, ok := findRoutingServer(r.Servers, profileServer.ServerID)
			if !ok || profileServer.MCPConfigRevisionID != server.MCPConfigRevisionID || !sameOptional(profileServer.AcceptedContractRevisionID, server.AcceptedContractRevisionID) || (profileServer.VisibilityMode != "all_accepted" && profileServer.VisibilityMode != "selected" && profileServer.VisibilityMode != "hidden") {
				return errors.New("routing bundle profile server pin is invalid")
			}
			if r.Mode == "enforced" && profileServer.AcceptedContractRevisionID == nil {
				return errors.New("enforced routing bundle requires accepted profile contracts")
			}
			if _, ok := profileServers[profileServer.ServerID]; ok {
				return errors.New("routing bundle contains duplicate profile server")
			}
			profileServers[profileServer.ServerID] = struct{}{}
			toolIDs := map[string]struct{}{}
			for _, tool := range server.Tools {
				toolIDs[tool.ToolID] = struct{}{}
			}
			seenOverrides := map[string]struct{}{}
			for _, override := range profileServer.ToolOverrides {
				if uuid.Validate(override.ToolID) != nil {
					return errors.New("routing bundle visibility override identity is invalid")
				}
				if _, ok := toolIDs[override.ToolID]; !ok {
					return errors.New("routing bundle visibility override references an unknown tool")
				}
				if _, ok := seenOverrides[override.ToolID]; ok {
					return errors.New("routing bundle contains duplicate visibility override")
				}
				seenOverrides[override.ToolID] = struct{}{}
			}
			seenRules := map[string]struct{}{}
			for _, rule := range profileServer.ToolRules {
				if uuid.Validate(rule.ToolID) != nil || !validGovernanceDecision(rule.Decision) {
					return errors.New("routing bundle policy rule is invalid")
				}
				if _, ok := seenRules[rule.ToolID]; ok {
					return errors.New("routing bundle contains duplicate policy rule")
				}
				seenRules[rule.ToolID] = struct{}{}
				serverTool, ok := findRoutingTool(server.Tools, rule.ToolID)
				if !ok || decisionRank(rule.Decision) < decisionRank(serverTool.GlobalDecision) {
					return errors.New("routing bundle profile policy loosens the global decision")
				}
			}
			totalRules += len(profileServer.ToolRules)
		}
	}
	if r.DefaultProfileID != nil {
		if _, ok := profileIDs[*r.DefaultProfileID]; !ok {
			return errors.New("routing bundle default profile is not published")
		}
	}
	if totalTools > 10000 || totalRules > 20000 {
		return errors.New("routing bundle profile limit exceeded")
	}
	return nil
}

func (r RoutingBundle) Canonical() ([]byte, string, error) {
	if err := r.Validate(); err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return nil, "", err
	}
	body, err := jsoncanonicalizer.Transform(encoded)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

func validGovernanceName(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 256
}

func validGovernanceDecision(value string) bool {
	return value == "allow" || value == "confirm" || value == "deny"
}

func decisionRank(value string) int {
	switch value {
	case "deny":
		return 2
	case "confirm":
		return 1
	default:
		return 0
	}
}

func findRoutingServer(servers []ServerContractDTO, id string) (ServerContractDTO, bool) {
	for _, server := range servers {
		if server.ServerID == id {
			return server, true
		}
	}
	return ServerContractDTO{}, false
}

func findRoutingTool(tools []RoutingToolDTO, id string) (RoutingToolDTO, bool) {
	for _, tool := range tools {
		if tool.ToolID == id {
			return tool, true
		}
	}
	return RoutingToolDTO{}, false
}

func sameOptional(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type RelayCapabilityResponse struct {
	AdminProtocolVersion  int      `json:"adminProtocolVersion"`
	Features              []string `json:"features"`
	RoutingSchemaVersions []int    `json:"routingSchemaVersions"`
	Runtime               string   `json:"runtime"`
	RuntimeVersion        string   `json:"runtimeVersion"`
}

var relayEnforcementFeatures = []string{
	"profile-session-binding",
	"tool-filtering",
	"call-policy",
	"one-shot-confirmation",
	"payload-free-observations",
	"routing-hot-reload",
}

func RelayEnforcementFeatures() []string {
	return append([]string(nil), relayEnforcementFeatures...)
}

func RelayEnforcementCapabilityCompatible(capability RelayCapabilityResponse) bool {
	version := strings.TrimSpace(capability.RuntimeVersion)
	if capability.AdminProtocolVersion != 1 || capability.Runtime != "mcpm" || version == "" || len(version) > 64 || strings.IndexFunc(version, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return false
	}
	hasRoutingSchema := false
	for _, version := range capability.RoutingSchemaVersions {
		if version == 1 {
			hasRoutingSchema = true
			break
		}
	}
	if !hasRoutingSchema {
		return false
	}
	features := make(map[string]struct{}, len(capability.Features))
	for _, feature := range capability.Features {
		features[feature] = struct{}{}
	}
	for _, required := range relayEnforcementFeatures {
		if _, ok := features[required]; !ok {
			return false
		}
	}
	return true
}

func NativeClientInspectionCompatible(inspection NativeClientInspectionResponse, clientKind string) bool {
	version := strings.TrimSpace(inspection.Version)
	return inspection.ClientKind == clientKind && inspection.Supported && inspection.ErrorCode == "" && version != "" && len(version) <= 64 && strings.IndexFunc(version, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}

type RelayReloadRequest struct {
	RelayConfigurationRevisionID string          `json:"relayConfigurationRevisionId"`
	RoutingBundleHash            string          `json:"routingBundleHash"`
	RoutingBundle                json.RawMessage `json:"routingBundle"`
}

type RelayReloadResponse struct {
	Reloaded          bool   `json:"reloaded"`
	RoutingBundleHash string `json:"routingBundleHash"`
}

type RelayAdminProfileRevision struct {
	ProfileID           string `json:"profileId"`
	ProfileRevisionID   string `json:"profileRevisionId"`
	ProfileRevisionHash string `json:"profileRevisionHash"`
}

type RelayAdminStatus struct {
	Mode                         string                      `json:"mode"`
	RelayConfigurationRevisionID string                      `json:"relayConfigurationRevisionId"`
	GlobalPolicyRevisionID       string                      `json:"globalPolicyRevisionId"`
	RoutingBundleHash            string                      `json:"routingBundleHash"`
	PublishedProfileRevisions    []RelayAdminProfileRevision `json:"publishedProfileRevisions"`
}

type ContractToolDTO struct {
	Name         string         `json:"name"`
	RuntimeName  string         `json:"runtimeName"`
	Title        *string        `json:"title"`
	Description  *string        `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Annotations  map[string]any `json:"annotations"`
}

type ContractServerObservation struct {
	ServerID            string            `json:"serverId"`
	ServerName          string            `json:"serverName"`
	MCPConfigRevisionID string            `json:"mcpConfigRevisionId"`
	Tools               []ContractToolDTO `json:"tools"`
}

type ContractObservationResponse struct {
	RelayConfigurationRevisionID string                      `json:"relayConfigurationRevisionId"`
	Servers                      []ContractServerObservation `json:"servers"`
}

type ArgumentSummary struct {
	Pointer      string `json:"pointer"`
	ValueType    string `json:"valueType"`
	ArrayLength  *int   `json:"arrayLength"`
	StringLength *int   `json:"stringLength"`
	Sensitive    bool   `json:"sensitive"`
}

type ConfirmationSummary struct {
	ChallengeID            string            `json:"challengeId"`
	BindingHash            string            `json:"bindingHash"`
	ArgumentHash           string            `json:"argumentHash"`
	CreatedAt              float64           `json:"createdAt"`
	ExpiresAt              float64           `json:"expiresAt"`
	ProfileID              string            `json:"profileId"`
	ProfileRevisionID      string            `json:"profileRevisionId"`
	ProfileName            string            `json:"profileName"`
	ClientKind             string            `json:"clientKind"`
	ServerID               string            `json:"serverId"`
	ServerName             string            `json:"serverName"`
	ToolID                 string            `json:"toolId"`
	ToolName               string            `json:"toolName"`
	RuntimeName            string            `json:"runtimeName"`
	MCPConfigRevisionID    string            `json:"mcpConfigRevisionId"`
	ContractRevisionID     string            `json:"contractRevisionId"`
	GlobalPolicyRevisionID string            `json:"globalPolicyRevisionId"`
	Decision               string            `json:"decision"`
	ReasonCodes            []string          `json:"reasonCodes"`
	ArgumentSummary        []ArgumentSummary `json:"argumentSummary"`
}

type ConfirmationListResponse struct {
	Items []ConfirmationSummary `json:"items"`
}

type ConfirmationDecisionRequest struct {
	ChallengeID string `json:"challengeId"`
	BindingHash string `json:"bindingHash"`
}

type ConfirmationDecisionResponse struct {
	ChallengeID    string   `json:"challengeId"`
	BindingHash    string   `json:"bindingHash"`
	GrantExpiresAt *float64 `json:"grantExpiresAt,omitempty"`
}

type Observation struct {
	BootID            string   `json:"bootId"`
	Sequence          int64    `json:"sequence"`
	ObservedAt        float64  `json:"observedAt"`
	MinuteBucket      string   `json:"minuteBucket"`
	ProfileID         string   `json:"profileId"`
	ProfileRevisionID string   `json:"profileRevisionId"`
	ServerID          string   `json:"serverId"`
	ToolID            string   `json:"toolId"`
	Decision          string   `json:"decision"`
	ReasonCodes       []string `json:"reasonCodes"`
	Outcome           string   `json:"outcome"`
	ErrorClass        string   `json:"errorClass,omitempty"`
	DurationBucket    string   `json:"durationBucket,omitempty"`
}

type ObservationDrainRequest struct {
	AfterBootID   *string `json:"afterBootId"`
	AfterSequence int64   `json:"afterSequence"`
	Limit         int     `json:"limit"`
}
type ObservationDrainResponse struct {
	BootID       string        `json:"bootId"`
	Items        []Observation `json:"items"`
	NextSequence int64         `json:"nextSequence"`
}

type NativeClientInspectionRequest struct {
	ManagedUsername string `json:"managedUsername"`
	ClientKind      string `json:"clientKind"`
}

type NativeClientInspectionResponse struct {
	ClientKind string `json:"clientKind"`
	Version    string `json:"version"`
	Supported  bool   `json:"supported"`
	ErrorCode  string `json:"errorCode,omitempty"`
}

func ValidateGovernanceBody(body []byte) error {
	if len(body) == 0 || len(body) > GovernanceMaxBodyBytes {
		return errors.New("governance body exceeds size limit")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := ensureGovernanceEOF(decoder); err != nil {
		return err
	}
	maxDepth := GovernanceMaxDepth
	if isRoutingGovernanceValue(value) {
		maxDepth = GovernanceMaxRoutingDepth
	}
	items := 0
	return validateGovernanceValue(value, 0, &items, maxDepth, false)
}

func DecodeGovernanceBody(body []byte, target any) error {
	if err := ValidateGovernanceBody(body); err != nil {
		return err
	}
	return decodeStrictGovernance(body, target)
}

func decodeStrictGovernance(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureGovernanceEOF(decoder)
}

func ensureGovernanceEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("governance body has trailing JSON")
		}
		return err
	}
	return nil
}

func validateGovernanceValue(value any, depth int, items *int, maxDepth int, schemaData bool) error {
	if depth > maxDepth || *items >= GovernanceMaxItems {
		return errors.New("governance body exceeds nesting or item limit")
	}
	*items = *items + 1
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			forbidden := map[string]struct{}{"secretvalues": {}, "arguments": {}, "result": {}, "prompt": {}, "rawerror": {}, "sessionid": {}, "ciphertext": {}}
			if _, ok := forbidden[lower]; ok && !schemaData {
				return fmt.Errorf("governance body contains forbidden field %q", key)
			}
			if len(key) > 256 {
				return errors.New("governance key is too long")
			}
			childIsSchemaData := schemaData || lower == "inputschema" || lower == "outputschema"
			if err := validateGovernanceValue(child, depth+1, items, maxDepth, childIsSchemaData); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateGovernanceValue(child, depth+1, items, maxDepth, schemaData); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > 4096 {
			return errors.New("governance string is too long")
		}
	}
	return nil
}

func isRoutingGovernanceValue(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if _, ok := object["routingBundle"]; ok {
		return true
	}
	_, hasSchema := object["schemaVersion"]
	_, hasServers := object["servers"]
	_, hasRelayRevision := object["relayConfigurationRevisionId"]
	return hasServers && (hasSchema || hasRelayRevision)
}
