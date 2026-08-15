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

	"github.com/google/uuid"
)

const (
	GovernanceMaxBodyBytes = 1 << 20
	GovernanceMaxDepth     = 5
	GovernanceMaxItems     = 10000
)

type RoutingBundle struct {
	SchemaVersion                int                 `json:"schemaVersion"`
	Mode                         string              `json:"mode"`
	RelayConfigurationRevisionID string              `json:"relayConfigurationRevisionId"`
	RelayConfigurationHash       string              `json:"relayConfigurationHash"`
	GlobalPolicyRevisionID       string              `json:"globalPolicyRevisionId"`
	GlobalPolicyHash             string              `json:"globalPolicyHash"`
	Profiles                     []RoutingProfileDTO `json:"profiles"`
}

type RoutingProfileDTO struct {
	ProfileID         string             `json:"profileId"`
	ProfileRevisionID string             `json:"profileRevisionId"`
	ClientKind        string             `json:"clientKind"`
	Category          string             `json:"category"`
	Variant           string             `json:"variant"`
	Servers           []RoutingServerDTO `json:"servers"`
	ToolRules         []ToolRuleDTO      `json:"toolRules"`
}

type RoutingServerDTO struct {
	ServerID                   string `json:"serverId"`
	MCPRevisionID              string `json:"mcpRevisionId"`
	AcceptedContractRevisionID string `json:"acceptedContractRevisionId,omitempty"`
	VisibilityMode             string `json:"visibilityMode,omitempty"`
}

type ToolRuleDTO struct {
	ToolID      string   `json:"toolId"`
	Visible     bool     `json:"visible"`
	Decision    string   `json:"decision"`
	ReasonCodes []string `json:"reasonCodes,omitempty"`
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
	if len(r.Profiles) > 100 {
		return errors.New("routing bundle profile limit exceeded")
	}
	seen := map[string]struct{}{}
	for _, profile := range r.Profiles {
		if uuid.Validate(profile.ProfileID) != nil || uuid.Validate(profile.ProfileRevisionID) != nil || (profile.ClientKind != RuntimeClaude && profile.ClientKind != RuntimeCodex && profile.ClientKind != "shared") {
			return errors.New("routing bundle profile identity is invalid")
		}
		if _, ok := seen[profile.ProfileID]; ok {
			return errors.New("routing bundle contains duplicate Profile")
		}
		seen[profile.ProfileID] = struct{}{}
		if len(profile.Servers) > 500 || len(profile.ToolRules) > 20000 {
			return errors.New("routing bundle profile limit exceeded")
		}
	}
	return nil
}

func (r RoutingBundle) Canonical() ([]byte, string, error) {
	if err := r.Validate(); err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(r)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

type RelayCapabilityResponse struct {
	AdminProtocolVersion  int      `json:"adminProtocolVersion"`
	Features              []string `json:"features"`
	RoutingSchemaVersions []int    `json:"routingSchemaVersions"`
	Runtime               string   `json:"runtime"`
	RuntimeVersion        string   `json:"runtimeVersion"`
}

type RelayReloadRequest struct {
	RelayConfigurationRevisionID string          `json:"relayConfigurationRevisionId"`
	RoutingBundleHash            string          `json:"routingBundleHash"`
	RoutingBundle                json.RawMessage `json:"routingBundle"`
}

type ContractToolDTO struct {
	ToolID       string         `json:"toolId,omitempty"`
	Name         string         `json:"name"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Annotations  map[string]any `json:"annotations"`
	Presentation map[string]any `json:"presentation"`
}

type ContractObservationResponse struct {
	ServerID           string            `json:"serverId"`
	ContractRevisionID string            `json:"contractRevisionId"`
	CanonicalHash      string            `json:"canonicalHash"`
	Tools              []ContractToolDTO `json:"tools"`
}

type ConfirmationSummary struct {
	ChallengeID       string `json:"challengeId"`
	ProfileID         string `json:"profileId"`
	ProfileRevisionID string `json:"profileRevisionId"`
	ServerID          string `json:"serverId"`
	ToolID            string `json:"toolId"`
	Decision          string `json:"decision"`
	ArgumentHash      string `json:"argumentHash"`
	ExpiresAt         string `json:"expiresAt"`
}

type ConfirmationListResponse struct {
	Items []ConfirmationSummary `json:"items"`
}

type ConfirmationDecisionRequest struct {
	ChallengeID string `json:"challengeId"`
	ProfileName string `json:"profileName"`
}

type Observation struct {
	TimeBucket        string `json:"timeBucket"`
	ProfileID         string `json:"profileId"`
	ProfileRevisionID string `json:"profileRevisionId"`
	ServerID          string `json:"serverId"`
	ToolID            string `json:"toolId"`
	Decision          string `json:"decision"`
	Outcome           string `json:"outcome"`
	ErrorClass        string `json:"errorClass,omitempty"`
	DurationBucket    string `json:"durationBucket,omitempty"`
}

type ObservationDrainRequest struct {
	BootID string `json:"bootId"`
	Cursor int64  `json:"cursor"`
	Limit  int    `json:"limit"`
}
type ObservationDrainResponse struct {
	BootID string        `json:"bootId"`
	Cursor int64         `json:"cursor"`
	Items  []Observation `json:"items"`
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
	return validateGovernanceValue(value, 0, 0)
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

func validateGovernanceValue(value any, depth, items int) error {
	if depth > GovernanceMaxDepth || items > GovernanceMaxItems {
		return errors.New("governance body exceeds nesting or item limit")
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			forbidden := map[string]struct{}{"secretvalues": {}, "arguments": {}, "result": {}, "prompt": {}, "rawerror": {}, "sessionid": {}, "ciphertext": {}}
			if _, ok := forbidden[lower]; ok {
				return fmt.Errorf("governance body contains forbidden field %q", key)
			}
			if len(key) > 256 {
				return errors.New("governance key is too long")
			}
			if err := validateGovernanceValue(child, depth+1, items+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateGovernanceValue(child, depth+1, items+1); err != nil {
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
