package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/policy"
)

type RelayConfigurationInput struct {
	Revision       int64
	MCPServerIDs   []string
	MCPRevisionIDs map[string]string
	Metadata       map[string]any
}

type RoutingServer struct {
	ServerID                   string        `json:"serverId"`
	ServerName                 string        `json:"serverName"`
	MCPConfigRevisionID        string        `json:"mcpConfigRevisionId"`
	AcceptedContractRevisionID *string       `json:"acceptedContractRevisionId"`
	AcceptedContractHash       *string       `json:"acceptedContractHash"`
	Tools                      []RoutingTool `json:"tools"`
}

type RoutingProfileServer struct {
	ServerID                   string                `json:"serverId"`
	MCPConfigRevisionID        string                `json:"mcpConfigRevisionId"`
	AcceptedContractRevisionID *string               `json:"acceptedContractRevisionId"`
	VisibilityMode             string                `json:"visibilityMode"`
	ToolOverrides              []RoutingToolOverride `json:"toolOverrides"`
	ToolRules                  []RoutingToolRule     `json:"toolRules"`
}

type RoutingProfile struct {
	ProfileID           string                 `json:"profileId"`
	ProfileRevisionID   string                 `json:"profileRevisionId"`
	ProfileRevisionHash string                 `json:"profileRevisionHash"`
	ProfileName         string                 `json:"profileName"`
	ClientKind          string                 `json:"clientKind"`
	Servers             []RoutingProfileServer `json:"servers"`
}

type RoutingTool struct {
	ToolID         string         `json:"toolId"`
	Name           string         `json:"name"`
	InputSchema    map[string]any `json:"inputSchema"`
	OutputSchema   map[string]any `json:"outputSchema"`
	Annotations    map[string]any `json:"annotations"`
	GlobalDecision string         `json:"globalDecision"`
	ReasonCodes    []string       `json:"reasonCodes"`
	Paused         bool           `json:"paused"`
}

type RoutingToolOverride struct {
	ToolID  string `json:"toolId"`
	Visible bool   `json:"visible"`
}

type RoutingToolRule struct {
	ToolID   string `json:"toolId"`
	Decision string `json:"decision"`
}

type RoutingBundle struct {
	SchemaVersion                int              `json:"schemaVersion"`
	Mode                         string           `json:"mode"`
	RelayConfigurationRevisionID string           `json:"relayConfigurationRevisionId"`
	RelayConfigurationHash       string           `json:"relayConfigurationHash"`
	GlobalPolicyRevisionID       string           `json:"globalPolicyRevisionId"`
	GlobalPolicyHash             string           `json:"globalPolicyHash"`
	DefaultProfileID             *string          `json:"defaultProfileId"`
	Servers                      []RoutingServer  `json:"servers"`
	Profiles                     []RoutingProfile `json:"profiles"`
}

// RoutingBundleCandidate overlays immutable candidate revisions on top of the
// currently applied routing pointers. An empty scalar keeps the applied value;
// a PublishedProfileRevisions entry with an empty revision removes that Profile.
type RoutingBundleCandidate struct {
	Mode                         string
	RelayConfigurationRevisionID string
	GlobalPolicyRevisionID       string
	DefaultProfileID             *string
	ReplacePublishedProfiles     bool
	PublishedProfileRevisions    map[string]string
}

func (b RoutingBundle) Canonical() ([]byte, string, error) {
	encoded, err := json.Marshal(b)
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

func (s *Store) SaveRelayConfiguration(ctx context.Context, input RelayConfigurationInput) (domain.RelayConfigurationRevision, error) {
	servers, err := relayConfigurationPins(input)
	if err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	metadataJSON, err := marshalJSONObject(input.Metadata)
	if err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	if err := rejectGovernanceMetadata(input.Metadata); err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	hash, err := relayConfigurationHash(servers, metadataJSON)
	if err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentID string
	if err := tx.QueryRow(ctx, `SELECT current_revision_id::text FROM relay_configuration_state WHERE singleton FOR UPDATE`).Scan(&currentID); err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	var currentRevision int64
	var currentHash string
	if err := tx.QueryRow(ctx, `SELECT revision,canonical_hash FROM relay_configuration_revisions WHERE id=$1`, currentID).Scan(&currentRevision, &currentHash); err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	if input.Revision != 0 && input.Revision != currentRevision {
		return domain.RelayConfigurationRevision{}, ErrConflict
	}
	if currentHash == hash {
		if err := tx.Commit(ctx); err != nil {
			return domain.RelayConfigurationRevision{}, err
		}
		return s.RelayConfiguration(ctx, currentID)
	}
	revision := currentRevision + 1
	id := uuid.NewString()
	if err := validateRelayConfigurationPinsTx(ctx, tx, servers); err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO relay_configuration_revisions(id,revision,canonical_hash,metadata) VALUES($1,$2,$3,$4)`, id, revision, hash, jsonText(metadataJSON)); err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	for position, pin := range servers {
		if _, err := tx.Exec(ctx, `INSERT INTO relay_configuration_revision_mcp_servers(relay_configuration_revision_id,server_id,mcp_revision_id,position) VALUES($1,$2,$3,$4)`, id, pin.ServerID, pin.MCPRevisionID, position); err != nil {
			return domain.RelayConfigurationRevision{}, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO relay_configuration_revision_seals(relay_configuration_revision_id) VALUES($1)`, id); err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE relay_configuration_state SET current_revision_id=$1,updated_at=now() WHERE singleton`, id); err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	return s.RelayConfiguration(ctx, id)
}

func rejectGovernanceMetadata(value any) error {
	forbidden := map[string]struct{}{"secretvalues": {}, "ciphertext": {}, "password": {}, "token": {}, "apikey": {}, "arguments": {}, "result": {}, "prompt": {}, "rawerror": {}, "sessionid": {}}
	var walk func(any) error
	walk = func(item any) error {
		switch typed := item.(type) {
		case map[string]any:
			for key, child := range typed {
				if _, ok := forbidden[strings.ToLower(key)]; ok {
					return fmt.Errorf("governance metadata contains forbidden field %q", key)
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

func relayConfigurationPins(input RelayConfigurationInput) ([]domain.RelayConfigurationMCPServerPin, error) {
	ids := uniqueIDs(input.MCPServerIDs)
	if len(ids) > maxProfileMembers {
		return nil, errors.New("relay configuration exceeds server limit")
	}
	result := make([]domain.RelayConfigurationMCPServerPin, 0, len(ids))
	seen := map[string]struct{}{}
	for position, serverID := range ids {
		if uuid.Validate(serverID) != nil {
			return nil, ErrNotFound
		}
		revisionID := strings.TrimSpace(input.MCPRevisionIDs[serverID])
		if uuid.Validate(revisionID) != nil {
			return nil, ErrNotFound
		}
		if _, ok := seen[serverID]; ok {
			return nil, errors.New("duplicate relay server")
		}
		seen[serverID] = struct{}{}
		result = append(result, domain.RelayConfigurationMCPServerPin{ServerID: serverID, MCPRevisionID: revisionID, Position: position})
	}
	return result, nil
}

func relayConfigurationHash(servers []domain.RelayConfigurationMCPServerPin, metadata []byte) (string, error) {
	body, err := json.Marshal(struct {
		Servers  []domain.RelayConfigurationMCPServerPin `json:"servers"`
		Metadata json.RawMessage                         `json:"metadata"`
	}{servers, metadata})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func validateRelayConfigurationPinsTx(ctx context.Context, tx pgx.Tx, pins []domain.RelayConfigurationMCPServerPin) error {
	for _, pin := range pins {
		var serverID string
		if err := tx.QueryRow(ctx, `SELECT server_id::text FROM mcp_revisions WHERE id=$1`, pin.MCPRevisionID).Scan(&serverID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if serverID != pin.ServerID {
			return ErrConflict
		}
		var missing bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_revisions mr CROSS JOIN LATERAL jsonb_each_text(mr.env_refs || mr.header_refs) refs LEFT JOIN encrypted_secrets es ON es.id=refs.value::uuid WHERE mr.id=$1 AND es.id IS NULL)`, pin.MCPRevisionID).Scan(&missing); err != nil {
			return err
		}
		if missing {
			return errors.New("relay configuration has an unbound MCP secret")
		}
	}
	return nil
}

func (s *Store) RelayConfiguration(ctx context.Context, id string) (domain.RelayConfigurationRevision, error) {
	var result domain.RelayConfigurationRevision
	var metadata []byte
	if err := s.pool.QueryRow(ctx, `SELECT id::text,revision,canonical_hash,metadata,created_at FROM relay_configuration_revisions WHERE id=$1`, id).Scan(&result.ID, &result.Revision, &result.CanonicalHash, &metadata, &result.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.RelayConfigurationRevision{}, ErrNotFound
		}
		return domain.RelayConfigurationRevision{}, err
	}
	if err := json.Unmarshal(metadata, &result.Metadata); err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	result.MCPServers = []domain.RelayConfigurationMCPServerPin{}
	rows, err := s.pool.Query(ctx, `SELECT server_id::text,mcp_revision_id::text,position FROM relay_configuration_revision_mcp_servers WHERE relay_configuration_revision_id=$1 ORDER BY position`, id)
	if err != nil {
		return domain.RelayConfigurationRevision{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var pin domain.RelayConfigurationMCPServerPin
		if err := rows.Scan(&pin.ServerID, &pin.MCPRevisionID, &pin.Position); err != nil {
			return domain.RelayConfigurationRevision{}, err
		}
		result.MCPServers = append(result.MCPServers, pin)
	}
	return result, rows.Err()
}

func (s *Store) RelayConfigurationHistory(ctx context.Context) ([]domain.RelayConfigurationRevision, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM relay_configuration_revisions ORDER BY revision DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]domain.RelayConfigurationRevision, 0, len(ids))
	for _, id := range ids {
		revision, err := s.RelayConfiguration(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, revision)
	}
	return result, nil
}

func (s *Store) ApplyRelayConfiguration(ctx context.Context, revisionID string) error {
	return ErrConflict
}

func (s *Store) FinalizeRelayConfigurationApply(ctx context.Context, operationID, revisionID, routingHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	metadata, err := validateSucceededGovernanceReloadTx(ctx, tx, operationID, "relay_config_apply", "", routingHash)
	if err != nil {
		return err
	}
	if stringMetadata(metadata, "revisionId") != revisionID || stringMetadata(metadata, "routingHash") != routingHash {
		return ErrConflict
	}
	var appliedRevisionID string
	if err := tx.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton FOR UPDATE`).Scan(&appliedRevisionID); err != nil {
		return err
	}
	if appliedRevisionID != stringMetadata(metadata, "expectedAppliedRelayConfigurationRevisionId") {
		return ErrConflict
	}
	affectedProfileRevisions, ok := stringMapMetadata(metadata, "affectedProfileRevisions")
	if !ok {
		return ErrConflict
	}
	expectedPublishedProfileRevisions, ok := stringMapMetadata(metadata, "expectedPublishedProfileRevisions")
	if !ok || len(expectedPublishedProfileRevisions) != len(affectedProfileRevisions) {
		return ErrConflict
	}
	profileIDs := make([]string, 0, len(affectedProfileRevisions))
	for profileID, candidateRevisionID := range affectedProfileRevisions {
		if uuid.Validate(profileID) != nil || uuid.Validate(candidateRevisionID) != nil || uuid.Validate(expectedPublishedProfileRevisions[profileID]) != nil {
			return ErrConflict
		}
		profileIDs = append(profileIDs, profileID)
	}
	sort.Strings(profileIDs)
	for _, profileID := range profileIDs {
		if err := validatePublishedProfilePredecessorTx(ctx, tx, profileID, expectedPublishedProfileRevisions[profileID]); err != nil {
			return err
		}
	}
	candidate := RoutingBundleCandidate{RelayConfigurationRevisionID: revisionID, PublishedProfileRevisions: affectedProfileRevisions}
	if err := s.validateCandidateRoutingHashTx(ctx, tx, candidate, routingHash); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_configuration_revisions WHERE id=$1)`, revisionID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	operationTargetID, targetID, manifest, err := relayOperationManifestTx(ctx, tx, operationID, revisionID)
	if err != nil {
		return err
	}
	if manifest.RelayGovernance == nil || manifest.RelayGovernance.RelayConfigurationRevisionID != revisionID || manifest.RelayGovernance.RoutingHash != routingHash {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE relay_configuration_state SET applied_revision_id=$1,updated_at=now() WHERE singleton`, revisionID); err != nil {
		return err
	}
	for _, profileID := range profileIDs {
		if err := publishProfileTx(ctx, tx, profileID, affectedProfileRevisions[profileID]); err != nil {
			return err
		}
	}
	if err := pinRelayDesiredSnapshotTx(ctx, tx, targetID, revisionID, operationTargetID, manifest); err != nil {
		return err
	}
	if err := markGovernanceFinalizedTx(ctx, tx, operationID, "relay_configuration_apply"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) SetRelayDefaultProfile(ctx context.Context, profileID string) error {
	return ErrConflict
}

func (s *Store) FinalizeRelayDefaultProfile(ctx context.Context, operationID, profileID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	metadata, err := validateSucceededGovernanceReloadTx(ctx, tx, operationID, "relay_config_apply", "", "")
	if err != nil {
		return err
	}
	if stringMetadata(metadata, "defaultProfileId") != profileID {
		return ErrConflict
	}
	var currentDefaultProfileID string
	if err := tx.QueryRow(ctx, `SELECT coalesce(default_profile_id::text,'') FROM relay_configuration_state WHERE singleton FOR UPDATE`).Scan(&currentDefaultProfileID); err != nil {
		return err
	}
	if currentDefaultProfileID != stringMetadata(metadata, "expectedDefaultProfileId") {
		return ErrConflict
	}
	candidateDefaultProfileID := profileID
	if err := s.validateCandidateRoutingHashTx(ctx, tx, RoutingBundleCandidate{DefaultProfileID: &candidateDefaultProfileID}, stringMetadata(metadata, "routingHash")); err != nil {
		return err
	}
	var value any
	if strings.TrimSpace(profileID) != "" {
		if uuid.Validate(profileID) != nil {
			return ErrNotFound
		}
		var published bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM published_profiles pp JOIN profiles p ON p.id=pp.profile_id WHERE pp.profile_id=$1 AND p.archived_at IS NULL)`, profileID).Scan(&published); err != nil {
			return err
		}
		if !published {
			return ErrConflict
		}
		value = profileID
	}
	if _, err := tx.Exec(ctx, `UPDATE relay_configuration_state SET default_profile_id=$1,updated_at=now() WHERE singleton`, value); err != nil {
		return err
	}
	if err := markGovernanceFinalizedTx(ctx, tx, operationID, "relay_default_profile"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) PrepareAffectedProfileUpdates(ctx context.Context, relayRevisionID string) ([]string, error) {
	if uuid.Validate(relayRevisionID) != nil {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var relayExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_configuration_revisions WHERE id=$1)`, relayRevisionID).Scan(&relayExists); err != nil {
		return nil, err
	}
	if !relayExists {
		return nil, ErrNotFound
	}
	relayPins := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT server_id::text,mcp_revision_id::text FROM relay_configuration_revision_mcp_servers WHERE relay_configuration_revision_id=$1 ORDER BY position`, relayRevisionID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var serverID, mcpRevisionID string
		if err := rows.Scan(&serverID, &mcpRevisionID); err != nil {
			rows.Close()
			return nil, err
		}
		relayPins[serverID] = mcpRevisionID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT pp.profile_id::text,pp.profile_revision_id::text FROM published_profiles pp WHERE EXISTS(SELECT 1 FROM profile_revision_mcp_servers pins WHERE pins.profile_revision_id=pp.profile_revision_id AND NOT EXISTS(SELECT 1 FROM relay_configuration_revision_mcp_servers relay WHERE relay.relay_configuration_revision_id=$1 AND relay.server_id=pins.server_id AND relay.mcp_revision_id=pins.mcp_revision_id)) ORDER BY pp.profile_id::text`, relayRevisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	publishedRevisionIDs := map[string]string{}
	for rows.Next() {
		var id, publishedRevisionID string
		if err := rows.Scan(&id, &publishedRevisionID); err != nil {
			return nil, err
		}
		result = append(result, id)
		publishedRevisionIDs[id] = publishedRevisionID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for _, profileID := range result {
		input, currentRevisionID, err := profileInputForRelayUpdateTx(ctx, tx, profileID, publishedRevisionIDs[profileID])
		if err != nil {
			return nil, err
		}
		changed := false
		for _, serverID := range input.MCPServerIDs {
			candidateRevisionID := relayPins[serverID]
			if candidateRevisionID == "" {
				return nil, ErrConflict
			}
			if input.MCPRevisionIDs[serverID] == candidateRevisionID {
				continue
			}
			input.MCPRevisionIDs[serverID] = candidateRevisionID
			for index := range input.MCPGovernance {
				if input.MCPGovernance[index].ServerID == serverID {
					input.MCPGovernance[index].MCPRevisionID = candidateRevisionID
				}
			}
			changed = true
		}
		if !changed {
			continue
		}
		pins, err := resolveProfilePinsTx(ctx, tx, input)
		if err != nil {
			return nil, err
		}
		candidateHash, err := CanonicalGovernedProfileHash(input, pins.Skills, pins.MCP)
		if err != nil {
			return nil, err
		}
		if currentRevisionID != publishedRevisionIDs[profileID] {
			var currentHash string
			var pendingBindings bool
			if err := tx.QueryRow(ctx, `SELECT canonical_hash,pending_bindings FROM profile_revisions WHERE id=$1 AND profile_id=$2`, currentRevisionID, profileID).Scan(&currentHash, &pendingBindings); err != nil {
				return nil, err
			}
			if pendingBindings || currentHash != candidateHash {
				return nil, ErrConflict
			}
			continue
		}
		_, candidateRevisionID, err := s.saveProfileTx(ctx, tx, profileID, input)
		if err != nil {
			return nil, err
		}
		if candidateRevisionID == currentRevisionID {
			return nil, ErrConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func profileInputForRelayUpdateTx(ctx context.Context, tx pgx.Tx, profileID, publishedRevisionID string) (ProfileInput, string, error) {
	var input ProfileInput
	var currentRevisionID, lockedPublishedRevisionID string
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT p.revision,p.current_revision_id::text,pp.profile_revision_id::text,p.archived_at IS NOT NULL FROM profiles p JOIN published_profiles pp ON pp.profile_id=p.id WHERE p.id=$1 FOR UPDATE OF p,pp`, profileID).Scan(&input.Revision, &currentRevisionID, &lockedPublishedRevisionID, &archived); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProfileInput{}, "", ErrNotFound
		}
		return ProfileInput{}, "", err
	}
	if archived || lockedPublishedRevisionID != publishedRevisionID {
		return ProfileInput{}, "", ErrConflict
	}
	var pendingBindings bool
	if err := tx.QueryRow(ctx, `SELECT name,description,client_kind,category,variant,migration_state,pending_bindings FROM profile_revisions WHERE id=$1 AND profile_id=$2`, publishedRevisionID, profileID).Scan(&input.Name, &input.Description, &input.ClientKind, &input.Category, &input.Variant, &input.MigrationState, &pendingBindings); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProfileInput{}, "", ErrNotFound
		}
		return ProfileInput{}, "", err
	}
	if pendingBindings {
		return ProfileInput{}, "", ErrConflict
	}
	input.SkillVersionIDs = map[string]string{}
	input.MCPRevisionIDs = map[string]string{}
	rows, err := tx.Query(ctx, `SELECT skill_id::text,skill_version_id::text FROM profile_revision_skills WHERE profile_revision_id=$1 ORDER BY position`, publishedRevisionID)
	if err != nil {
		return ProfileInput{}, "", err
	}
	for rows.Next() {
		var skillID, versionID string
		if err := rows.Scan(&skillID, &versionID); err != nil {
			rows.Close()
			return ProfileInput{}, "", err
		}
		input.SkillIDs = append(input.SkillIDs, skillID)
		input.SkillVersionIDs[skillID] = versionID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ProfileInput{}, "", err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT server_id::text,mcp_revision_id::text FROM profile_revision_mcp_servers WHERE profile_revision_id=$1 ORDER BY position`, publishedRevisionID)
	if err != nil {
		return ProfileInput{}, "", err
	}
	for rows.Next() {
		var serverID, revisionID string
		if err := rows.Scan(&serverID, &revisionID); err != nil {
			rows.Close()
			return ProfileInput{}, "", err
		}
		input.MCPServerIDs = append(input.MCPServerIDs, serverID)
		input.MCPRevisionIDs[serverID] = revisionID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ProfileInput{}, "", err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT server_id::text,mcp_revision_id::text,coalesce(accepted_contract_revision_id::text,''),visibility_mode FROM profile_revision_mcp_governance WHERE profile_revision_id=$1 ORDER BY server_id`, publishedRevisionID)
	if err != nil {
		return ProfileInput{}, "", err
	}
	for rows.Next() {
		var item ProfileMCPGovernanceInput
		if err := rows.Scan(&item.ServerID, &item.MCPRevisionID, &item.AcceptedContractRevisionID, &item.VisibilityMode); err != nil {
			rows.Close()
			return ProfileInput{}, "", err
		}
		input.MCPGovernance = append(input.MCPGovernance, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ProfileInput{}, "", err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT tool_id::text,visible,decision,reason_codes FROM profile_revision_tool_rules WHERE profile_revision_id=$1 ORDER BY tool_id`, publishedRevisionID)
	if err != nil {
		return ProfileInput{}, "", err
	}
	for rows.Next() {
		var item ProfileToolRuleInput
		if err := rows.Scan(&item.ToolID, &item.Visible, &item.Decision, &item.ReasonCodes); err != nil {
			rows.Close()
			return ProfileInput{}, "", err
		}
		input.ToolRules = append(input.ToolRules, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ProfileInput{}, "", err
	}
	rows.Close()
	return input, currentRevisionID, nil
}

func (s *Store) PublishProfile(ctx context.Context, profileID, revisionID string) error {
	return ErrConflict
}

func (s *Store) FinalizeProfilePublish(ctx context.Context, operationID, profileID, revisionID, routingHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	metadata, err := validateSucceededGovernanceReloadTx(ctx, tx, operationID, "apply", profileID, routingHash)
	if err != nil {
		return err
	}
	if stringMetadata(metadata, "profileRevisionId") != revisionID || stringMetadata(metadata, "routingHash") != routingHash {
		return ErrConflict
	}
	if err := validatePublishedProfilePredecessorTx(ctx, tx, profileID, stringMetadata(metadata, "expectedPublishedProfileRevisionId")); err != nil {
		return err
	}
	if err := s.validateCandidateRoutingHashTx(ctx, tx, RoutingBundleCandidate{PublishedProfileRevisions: map[string]string{profileID: revisionID}}, routingHash); err != nil {
		return err
	}
	if err := publishProfileTx(ctx, tx, profileID, revisionID); err != nil {
		return err
	}
	if err := markGovernanceFinalizedTx(ctx, tx, operationID, "profile_publish"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) UnpublishProfile(ctx context.Context, profileID string) error {
	return ErrConflict
}

func (s *Store) FinalizeProfileUnpublish(ctx context.Context, operationID, profileID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	metadata, err := validateSucceededGovernanceReloadTx(ctx, tx, operationID, "apply", profileID, "")
	if err != nil {
		return err
	}
	if err := validatePublishedProfilePredecessorTx(ctx, tx, profileID, stringMetadata(metadata, "expectedPublishedProfileRevisionId")); err != nil {
		return err
	}
	var currentDefaultProfileID string
	if err := tx.QueryRow(ctx, `SELECT coalesce(default_profile_id::text,'') FROM relay_configuration_state WHERE singleton FOR UPDATE`).Scan(&currentDefaultProfileID); err != nil {
		return err
	}
	if currentDefaultProfileID != stringMetadata(metadata, "expectedDefaultProfileId") {
		return ErrConflict
	}
	candidate := RoutingBundleCandidate{PublishedProfileRevisions: map[string]string{profileID: ""}}
	if currentDefaultProfileID == profileID {
		emptyDefault := ""
		candidate.DefaultProfileID = &emptyDefault
	}
	if err := s.validateCandidateRoutingHashTx(ctx, tx, candidate, stringMetadata(metadata, "routingHash")); err != nil {
		return err
	}
	if currentDefaultProfileID == profileID {
		if _, err := tx.Exec(ctx, `UPDATE relay_configuration_state SET default_profile_id=NULL,updated_at=now() WHERE singleton`); err != nil {
			return err
		}
	}
	command, err := tx.Exec(ctx, `DELETE FROM published_profiles WHERE profile_id=$1`, profileID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	if err := markGovernanceFinalizedTx(ctx, tx, operationID, "profile_unpublish"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) FinalizeProfileArchive(ctx context.Context, operationID, profileID string, expectedProfileRevision int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	metadata, err := validateSucceededGovernanceReloadTx(ctx, tx, operationID, "apply", profileID, "")
	if err != nil {
		return err
	}
	metadataRevision, ok := int64Metadata(metadata, "archiveProfileRevision")
	if !ok || metadataRevision != expectedProfileRevision {
		return ErrConflict
	}
	expectedPublishedRevisionID := stringMetadata(metadata, "expectedPublishedProfileRevisionId")
	if expectedPublishedRevisionID == "" {
		return ErrConflict
	}
	if err := validatePublishedProfilePredecessorTx(ctx, tx, profileID, expectedPublishedRevisionID); err != nil {
		return err
	}
	var currentRevision int64
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT revision,archived_at IS NOT NULL FROM profiles WHERE id=$1 FOR UPDATE`, profileID).Scan(&currentRevision, &archived); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if archived || currentRevision != expectedProfileRevision {
		return ErrConflict
	}
	var currentDefaultProfileID string
	if err := tx.QueryRow(ctx, `SELECT coalesce(default_profile_id::text,'') FROM relay_configuration_state WHERE singleton FOR UPDATE`).Scan(&currentDefaultProfileID); err != nil {
		return err
	}
	if currentDefaultProfileID != stringMetadata(metadata, "expectedDefaultProfileId") {
		return ErrConflict
	}
	candidate := RoutingBundleCandidate{PublishedProfileRevisions: map[string]string{profileID: ""}}
	if currentDefaultProfileID == profileID {
		emptyDefault := ""
		candidate.DefaultProfileID = &emptyDefault
	}
	_, candidateHash, err := s.RenderCandidateRoutingBundleTx(ctx, tx, candidate)
	if err != nil {
		return err
	}
	if candidateHash != stringMetadata(metadata, "routingHash") {
		return ErrConflict
	}
	if currentDefaultProfileID == profileID {
		if _, err := tx.Exec(ctx, `UPDATE relay_configuration_state SET default_profile_id=NULL,updated_at=now() WHERE singleton`); err != nil {
			return err
		}
	}
	command, err := tx.Exec(ctx, `DELETE FROM published_profiles WHERE profile_id=$1`, profileID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	command, err = tx.Exec(ctx, `UPDATE profiles SET archived_at=now(),updated_at=now() WHERE id=$1 AND revision=$2 AND archived_at IS NULL`, profileID, expectedProfileRevision)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	auditMetadata, _ := json.Marshal(map[string]any{"operationId": operationID, "profileRevision": expectedProfileRevision})
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata) VALUES($1,'profile_archive','profile',$2,'success',$3)`, uuid.NewString(), profileID, jsonText(auditMetadata)); err != nil {
		return err
	}
	if err := markGovernanceFinalizedTx(ctx, tx, operationID, "profile_archive"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func publishProfileTx(ctx context.Context, tx pgx.Tx, profileID, revisionID string) error {
	var owner string
	var archived bool
	var clientKind, migrationState string
	if err := tx.QueryRow(ctx, `SELECT profile_id::text,client_kind,migration_state FROM profile_revisions WHERE id=$1 FOR SHARE`, revisionID).Scan(&owner, &clientKind, &migrationState); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if owner != profileID || (clientKind != domain.RuntimeClaude && clientKind != domain.RuntimeCodex) || migrationState != "ready" {
		return ErrConflict
	}
	if err := tx.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM profiles WHERE id=$1 FOR SHARE`, profileID).Scan(&archived); err != nil {
		return err
	}
	if archived {
		return ErrConflict
	}
	var invalidPins int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM profile_revision_mcp_governance g
		LEFT JOIN mcp_contract_state cs ON cs.server_id=g.server_id
		WHERE g.profile_revision_id=$1
		  AND (
			g.accepted_contract_revision_id IS NULL
			OR cs.accepted_revision_id IS NULL
			OR g.accepted_contract_revision_id IS DISTINCT FROM cs.accepted_revision_id
			OR NOT EXISTS (
				SELECT 1
				FROM relay_configuration_state state
				JOIN relay_configuration_revision_mcp_servers relay
				  ON relay.relay_configuration_revision_id=state.applied_revision_id
				WHERE state.singleton
				  AND relay.server_id=g.server_id
				  AND relay.mcp_revision_id=g.mcp_revision_id
			)
		  )`, revisionID).Scan(&invalidPins); err != nil {
		return err
	}
	if invalidPins != 0 {
		return ErrConflict
	}
	_, err := tx.Exec(ctx, `INSERT INTO published_profiles(profile_id,profile_revision_id) VALUES($1,$2) ON CONFLICT(profile_id) DO UPDATE SET profile_revision_id=EXCLUDED.profile_revision_id,published_at=now()`, profileID, revisionID)
	return err
}

func validateSucceededGovernanceOperationTx(ctx context.Context, tx pgx.Tx, operationID, expectedKind, expectedSourceID string) (json.RawMessage, error) {
	if uuid.Validate(operationID) != nil {
		return nil, ErrNotFound
	}
	var kind, status, sourceID string
	var metadata json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT kind,status,coalesce(source_id::text,''),metadata FROM operations WHERE id=$1 FOR UPDATE`, operationID).Scan(&kind, &status, &sourceID, &metadata); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if kind != expectedKind || status != "running" || (expectedSourceID != "" && sourceID != expectedSourceID) {
		return nil, ErrConflict
	}
	if stringMetadata(metadata, "governanceFinalizedAction") != "" {
		return nil, ErrConflict
	}
	var targetCount, pendingCount int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE governance_finalization_pending) FROM operation_targets WHERE operation_id=$1`, operationID).Scan(&targetCount, &pendingCount); err != nil {
		return nil, err
	}
	if targetCount == 0 || pendingCount != targetCount {
		return nil, ErrConflict
	}
	return metadata, nil
}

func validateSucceededGovernanceReloadTx(ctx context.Context, tx pgx.Tx, operationID, expectedKind, expectedSourceID, expectedRoutingHash string) (json.RawMessage, error) {
	metadata, err := validateSucceededGovernanceOperationTx(ctx, tx, operationID, expectedKind, expectedSourceID)
	if err != nil {
		return nil, err
	}
	metadataRoutingHash := stringMetadata(metadata, "routingHash")
	if expectedRoutingHash == "" {
		expectedRoutingHash = metadataRoutingHash
	}
	if !bridgeprotocol.IsSHA256(expectedRoutingHash) || metadataRoutingHash != expectedRoutingHash {
		return nil, ErrConflict
	}
	var relayTargets, reloadedRelayTargets int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE ot.status='succeeded' AND ot.result->>'routingReloaded'='true' AND ot.result->>'routingHash'=$2) FROM operation_targets ot JOIN targets t ON t.id=ot.target_id WHERE ot.operation_id=$1 AND t.runtime='shared-relay'`, operationID, expectedRoutingHash).Scan(&relayTargets, &reloadedRelayTargets); err != nil {
		return nil, err
	}
	if relayTargets != 1 || reloadedRelayTargets != 1 {
		return nil, ErrConflict
	}
	var manifestJSON []byte
	if err := tx.QueryRow(ctx, `SELECT ot.request->'manifest' FROM operation_targets ot JOIN targets t ON t.id=ot.target_id WHERE ot.operation_id=$1 AND t.runtime='shared-relay'`, operationID).Scan(&manifestJSON); err != nil {
		return nil, err
	}
	manifest, err := bridgeprotocol.DecodeManifest(manifestJSON, true)
	if err != nil || manifest.SchemaVersion != bridgeprotocol.ManifestSchemaVersionV2 || manifest.RelayGovernance == nil || manifest.RelayGovernance.RoutingHash != expectedRoutingHash {
		return nil, ErrConflict
	}
	return metadata, nil
}

func (s *Store) validateCandidateRoutingHashTx(ctx context.Context, tx pgx.Tx, candidate RoutingBundleCandidate, expectedHash string) error {
	_, actualHash, err := s.RenderCandidateRoutingBundleTx(ctx, tx, candidate)
	if err != nil {
		return err
	}
	if !bridgeprotocol.IsSHA256(expectedHash) || actualHash != expectedHash {
		return ErrConflict
	}
	return nil
}

func stringMetadata(metadata json.RawMessage, key string) string {
	var values map[string]any
	if json.Unmarshal(metadata, &values) != nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
}

func int64Metadata(metadata json.RawMessage, key string) (int64, bool) {
	var values map[string]json.RawMessage
	if json.Unmarshal(metadata, &values) != nil {
		return 0, false
	}
	value, found := values[key]
	if !found {
		return 0, false
	}
	var result int64
	if json.Unmarshal(value, &result) != nil {
		return 0, false
	}
	return result, true
}

func stringMapMetadata(metadata json.RawMessage, key string) (map[string]string, bool) {
	var values map[string]json.RawMessage
	if json.Unmarshal(metadata, &values) != nil {
		return nil, false
	}
	raw, found := values[key]
	if !found {
		return nil, false
	}
	var result map[string]string
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, false
	}
	return result, true
}

func relayOperationManifestTx(ctx context.Context, tx pgx.Tx, operationID, relayRevisionID string) (string, string, bridgeprotocol.DesiredManifest, error) {
	var operationTargetID, targetID, runtime, sourceKind, sourceID string
	var manifestJSON []byte
	if err := tx.QueryRow(ctx, `SELECT ot.id::text,ot.target_id::text,t.runtime,coalesce(ot.request->>'sourceKind',''),coalesce(ot.request->>'sourceId',''),ot.request->'manifest' FROM operation_targets ot JOIN targets t ON t.id=ot.target_id WHERE ot.operation_id=$1 AND t.runtime='shared-relay'`, operationID).Scan(&operationTargetID, &targetID, &runtime, &sourceKind, &sourceID, &manifestJSON); err != nil {
		return "", "", bridgeprotocol.DesiredManifest{}, err
	}
	manifest, err := bridgeprotocol.DecodeManifest(manifestJSON, true)
	if err != nil || runtime != domain.RuntimeSharedRelay || sourceKind != "relay_config_apply" || sourceID != relayRevisionID || manifest.Target.ID != targetID || manifest.Target.Runtime != domain.RuntimeSharedRelay {
		return "", "", bridgeprotocol.DesiredManifest{}, ErrConflict
	}
	return operationTargetID, targetID, manifest, nil
}

func pinRelayDesiredSnapshotTx(ctx context.Context, tx pgx.Tx, targetID, relayRevisionID, operationTargetID string, manifest bridgeprotocol.DesiredManifest) error {
	body, manifestHash, err := manifest.Canonical()
	if err != nil || manifest.SchemaVersion != bridgeprotocol.ManifestSchemaVersionV2 || manifest.RelayGovernance == nil || manifest.Target.ID != targetID {
		return ErrConflict
	}
	var routingBundle bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(manifest.RelayGovernance.RoutingBundle, &routingBundle); err != nil {
		return err
	}
	routingBundleCanonical, routingHash, err := routingBundle.Canonical()
	if err != nil || routingHash != manifest.RelayGovernance.RoutingHash {
		return ErrConflict
	}
	var existing bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM desired_snapshots WHERE source_operation_target_id=$1)`, operationTargetID).Scan(&existing); err != nil {
		return err
	}
	if existing {
		return ErrConflict
	}
	var lockedTargetID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM targets WHERE id=$1 FOR UPDATE`, targetID).Scan(&lockedTargetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(revision),0)+1 FROM desired_snapshots WHERE target_id=$1`, targetID).Scan(&revision); err != nil {
		return err
	}
	snapshotID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,source_id,source_operation_target_id,profile_revision,manifest_schema_version,manifest_hash,manifest,routing_bundle_canonical) VALUES($1,$2,$3,'relay_config_apply',$4,$5,$6,$7,$8,$9,$10)`, snapshotID, targetID, revision, relayRevisionID, operationTargetID, manifest.ProfileRevision, manifest.SchemaVersion, manifestHash, jsonText(body), routingBundleCanonical); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO target_desired_snapshots(target_id,snapshot_id,desired_revision,health,drift_summary,error_code,error_reason,updated_at) VALUES($1,$2,$3,'healthy','{}','','',now()) ON CONFLICT(target_id) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id,desired_revision=EXCLUDED.desired_revision,health='healthy',drift_summary='{}',error_code='',error_reason='',updated_at=now()`, targetID, snapshotID, revision)
	return err
}

func validatePublishedProfilePredecessorTx(ctx context.Context, tx pgx.Tx, profileID, expectedRevisionID string) error {
	var currentRevisionID string
	if err := tx.QueryRow(ctx, `SELECT coalesce(pp.profile_revision_id::text,'') FROM profiles p LEFT JOIN published_profiles pp ON pp.profile_id=p.id WHERE p.id=$1 FOR UPDATE OF p`, profileID).Scan(&currentRevisionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if currentRevisionID != expectedRevisionID {
		return ErrConflict
	}
	return nil
}

func markGovernanceFinalizedTx(ctx context.Context, tx pgx.Tx, operationID, action string) error {
	command, err := tx.Exec(ctx, `UPDATE operations SET metadata=jsonb_set(metadata,'{governanceFinalizedAction}',to_jsonb($2::text),true),updated_at=now() WHERE id=$1 AND NOT (metadata ? 'governanceFinalizedAction')`, operationID, action)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	command, err = tx.Exec(ctx, `UPDATE operation_targets SET governance_finalization_pending=false,updated_at=now() WHERE operation_id=$1 AND governance_finalization_pending`, operationID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrConflict
	}
	return recalculateOperation(ctx, tx, operationID)
}

func (s *Store) ListPublishedProfiles(ctx context.Context) ([]domain.PublishedProfile, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.profile_id::text,p.profile_revision_id::text,pr.client_kind,pr.category,pr.variant,p.published_at FROM published_profiles p JOIN profile_revisions pr ON pr.id=p.profile_revision_id ORDER BY p.profile_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.PublishedProfile{}
	for rows.Next() {
		var item domain.PublishedProfile
		if err := rows.Scan(&item.ProfileID, &item.ProfileRevisionID, &item.ClientKind, &item.Category, &item.Variant, &item.PublishedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) RenderRoutingBundle(ctx context.Context) (RoutingBundle, string, error) {
	return s.RenderCandidateRoutingBundle(ctx, RoutingBundleCandidate{})
}

func (s *Store) RenderCandidateRoutingBundle(ctx context.Context, candidate RoutingBundleCandidate) (RoutingBundle, string, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return RoutingBundle{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	bundle, hash, err := s.RenderCandidateRoutingBundleTx(ctx, tx, candidate)
	if err != nil {
		return RoutingBundle{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return RoutingBundle{}, "", err
	}
	return bundle, hash, nil
}

// RenderRoutingBundleTx renders one consistent database snapshot. All pointers,
// accepted contracts, policy decisions, and published profile rows are read
// through the same transaction so the hash cannot describe a torn state.
func (s *Store) RenderRoutingBundleTx(ctx context.Context, tx pgx.Tx) (RoutingBundle, string, error) {
	return s.RenderCandidateRoutingBundleTx(ctx, tx, RoutingBundleCandidate{})
}

func (s *Store) RenderCandidateRoutingBundleTx(ctx context.Context, tx pgx.Tx, candidate RoutingBundleCandidate) (RoutingBundle, string, error) {
	var bundle RoutingBundle
	var relayID, relayHash, mode, globalID, globalHash string
	var defaultProfile string
	if err := tx.QueryRow(ctx, `SELECT s.mode,s.applied_revision_id::text,r.canonical_hash,coalesce(s.default_profile_id::text,''),g.applied_revision_id::text,gr.canonical_hash FROM relay_configuration_state s JOIN relay_configuration_revisions r ON r.id=s.applied_revision_id CROSS JOIN global_policy_state g JOIN global_policy_revisions gr ON gr.id=g.applied_revision_id WHERE s.singleton AND g.singleton`).Scan(&mode, &relayID, &relayHash, &defaultProfile, &globalID, &globalHash); err != nil {
		return bundle, "", err
	}
	if candidate.Mode != "" {
		mode = candidate.Mode
	}
	if candidate.RelayConfigurationRevisionID != "" {
		relayID = candidate.RelayConfigurationRevisionID
		if err := tx.QueryRow(ctx, `SELECT canonical_hash FROM relay_configuration_revisions WHERE id=$1`, relayID).Scan(&relayHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return bundle, "", ErrNotFound
			}
			return bundle, "", err
		}
	}
	if candidate.GlobalPolicyRevisionID != "" {
		globalID = candidate.GlobalPolicyRevisionID
		if err := tx.QueryRow(ctx, `SELECT canonical_hash FROM global_policy_revisions WHERE id=$1`, globalID).Scan(&globalHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return bundle, "", ErrNotFound
			}
			return bundle, "", err
		}
	}
	if candidate.DefaultProfileID != nil {
		defaultProfile = strings.TrimSpace(*candidate.DefaultProfileID)
	}
	bundle = RoutingBundle{SchemaVersion: 1, Mode: mode, RelayConfigurationRevisionID: relayID, RelayConfigurationHash: relayHash, GlobalPolicyRevisionID: globalID, GlobalPolicyHash: globalHash, Servers: []RoutingServer{}, Profiles: []RoutingProfile{}}
	if defaultProfile != "" {
		bundle.DefaultProfileID = &defaultProfile
	}
	var global domain.GlobalPolicyRevision
	var overrides []byte
	if err := tx.QueryRow(ctx, `SELECT id::text,revision,canonical_hash,catalog_version,explicit_overrides,unclassified_mutating,reviewed_read_only,created_at FROM global_policy_revisions WHERE id=$1`, globalID).Scan(&global.ID, &global.Revision, &global.CanonicalHash, &global.CatalogVersion, &overrides, &global.UnclassifiedMutating, &global.ReviewedReadOnly, &global.CreatedAt); err != nil {
		return bundle, "", err
	}
	if err := json.Unmarshal(overrides, &global.ExplicitOverrides); err != nil {
		return bundle, "", err
	}
	rows, err := tx.Query(ctx, `SELECT r.server_id::text,ms.name,r.mcp_revision_id::text,cs.accepted_revision_id::text,acr.canonical_hash,coalesce(cs.review_state,'unreviewed') FROM relay_configuration_revision_mcp_servers r JOIN mcp_servers ms ON ms.id=r.server_id LEFT JOIN mcp_contract_state cs ON cs.server_id=r.server_id LEFT JOIN mcp_contract_revisions acr ON acr.id=cs.accepted_revision_id WHERE r.relay_configuration_revision_id=$1 ORDER BY r.position`, relayID)
	if err != nil {
		return bundle, "", err
	}
	type serverEntry struct {
		server                     RoutingServer
		acceptedContractRevisionID *string
		acceptedContractHash       *string
		reviewState                *string
	}
	serverEntries := []serverEntry{}
	for rows.Next() {
		var server RoutingServer
		var acceptedID, acceptedHash, reviewState *string
		if err := rows.Scan(&server.ServerID, &server.ServerName, &server.MCPConfigRevisionID, &acceptedID, &acceptedHash, &reviewState); err != nil {
			rows.Close()
			return bundle, "", err
		}
		serverEntries = append(serverEntries, serverEntry{server: server, acceptedContractRevisionID: acceptedID, acceptedContractHash: acceptedHash, reviewState: reviewState})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return bundle, "", err
	}
	rows.Close()
	for _, entry := range serverEntries {
		server := entry.server
		server.Tools = []RoutingTool{}
		server.AcceptedContractRevisionID, server.AcceptedContractHash = entry.acceptedContractRevisionID, entry.acceptedContractHash
		if entry.acceptedContractRevisionID != nil {
			toolRows, err := tx.Query(ctx, `SELECT t.id::text,t.name,crt.input_schema,crt.output_schema,crt.annotations,latest.tool_id IS NOT NULL,latest.input_schema,latest.output_schema,latest.annotations FROM mcp_contract_revision_tools crt JOIN mcp_tools t ON t.id=crt.tool_id LEFT JOIN mcp_contract_state cs ON cs.server_id=t.server_id LEFT JOIN mcp_contract_revision_tools latest ON latest.contract_revision_id=cs.latest_revision_id AND latest.tool_id=crt.tool_id WHERE crt.contract_revision_id=$1 ORDER BY crt.position`, *entry.acceptedContractRevisionID)
			if err != nil {
				return bundle, "", err
			}
			for toolRows.Next() {
				var tool RoutingTool
				var inputJSON, outputJSON, annotationsJSON []byte
				var latestInputJSON, latestOutputJSON, latestAnnotationsJSON []byte
				var latestExists bool
				if err := toolRows.Scan(&tool.ToolID, &tool.Name, &inputJSON, &outputJSON, &annotationsJSON, &latestExists, &latestInputJSON, &latestOutputJSON, &latestAnnotationsJSON); err != nil {
					toolRows.Close()
					return bundle, "", err
				}
				if err := json.Unmarshal(inputJSON, &tool.InputSchema); err != nil || tool.InputSchema == nil {
					toolRows.Close()
					return bundle, "", errors.New("contract input schema is not an object")
				}
				if string(outputJSON) == "null" {
					tool.OutputSchema = nil
				} else if err := json.Unmarshal(outputJSON, &tool.OutputSchema); err != nil {
					toolRows.Close()
					return bundle, "", errors.New("contract output schema is not an object")
				}
				if err := json.Unmarshal(annotationsJSON, &tool.Annotations); err != nil || tool.Annotations == nil {
					toolRows.Close()
					return bundle, "", errors.New("contract annotations are not an object")
				}
				tool.GlobalDecision, tool.ReasonCodes = classifyRoutingTool(global, tool)
				tool.Paused = !latestExists
				if latestExists {
					var latestInput, latestOutput, latestAnnotations map[string]any
					if err := json.Unmarshal(latestInputJSON, &latestInput); err != nil || latestInput == nil {
						toolRows.Close()
						return bundle, "", errors.New("latest contract input schema is not an object")
					}
					if string(latestOutputJSON) != "null" {
						if err := json.Unmarshal(latestOutputJSON, &latestOutput); err != nil {
							toolRows.Close()
							return bundle, "", errors.New("latest contract output schema is not an object")
						}
					}
					if err := json.Unmarshal(latestAnnotationsJSON, &latestAnnotations); err != nil || latestAnnotations == nil {
						toolRows.Close()
						return bundle, "", errors.New("latest contract annotations are not an object")
					}
					tool.Paused = !equalJSONMap(tool.InputSchema, latestInput) || !equalJSONMap(tool.OutputSchema, latestOutput) || !equalJSONMap(tool.Annotations, latestAnnotations)
				}
				server.Tools = append(server.Tools, tool)
			}
			if err := toolRows.Err(); err != nil {
				toolRows.Close()
				return bundle, "", err
			}
			toolRows.Close()
		}
		bundle.Servers = append(bundle.Servers, server)
	}
	profileRows, err := tx.Query(ctx, `SELECT pp.profile_id::text,pp.profile_revision_id::text FROM published_profiles pp JOIN profiles p ON p.id=pp.profile_id WHERE p.archived_at IS NULL ORDER BY pp.profile_id`)
	if err != nil {
		return bundle, "", err
	}
	profileRevisionIDs := map[string]string{}
	for profileRows.Next() {
		var profileID, revisionID string
		if err := profileRows.Scan(&profileID, &revisionID); err != nil {
			profileRows.Close()
			return bundle, "", err
		}
		profileRevisionIDs[profileID] = revisionID
	}
	if err := profileRows.Err(); err != nil {
		profileRows.Close()
		return bundle, "", err
	}
	profileRows.Close()
	if candidate.ReplacePublishedProfiles {
		profileRevisionIDs = map[string]string{}
	}
	for profileID, revisionID := range candidate.PublishedProfileRevisions {
		if uuid.Validate(profileID) != nil {
			return bundle, "", ErrNotFound
		}
		if strings.TrimSpace(revisionID) == "" {
			delete(profileRevisionIDs, profileID)
			continue
		}
		profileRevisionIDs[profileID] = revisionID
	}
	profileIDs := make([]string, 0, len(profileRevisionIDs))
	for profileID := range profileRevisionIDs {
		profileIDs = append(profileIDs, profileID)
	}
	sort.Strings(profileIDs)
	for _, profileID := range profileIDs {
		profile := RoutingProfile{ProfileID: profileID, ProfileRevisionID: profileRevisionIDs[profileID], Servers: []RoutingProfileServer{}}
		var owner string
		var archived bool
		if err := tx.QueryRow(ctx, `SELECT pr.profile_id::text,pr.canonical_hash,pr.name,pr.client_kind,p.archived_at IS NOT NULL FROM profile_revisions pr JOIN profiles p ON p.id=pr.profile_id WHERE pr.id=$1`, profile.ProfileRevisionID).Scan(&owner, &profile.ProfileRevisionHash, &profile.ProfileName, &profile.ClientKind, &archived); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return bundle, "", ErrNotFound
			}
			return bundle, "", err
		}
		if owner != profile.ProfileID || archived || (profile.ClientKind != domain.RuntimeClaude && profile.ClientKind != domain.RuntimeCodex) {
			return bundle, "", ErrConflict
		}
		serverRows, err := tx.Query(ctx, `SELECT g.server_id::text,g.mcp_revision_id::text,g.accepted_contract_revision_id::text,g.visibility_mode FROM profile_revision_mcp_governance g WHERE g.profile_revision_id=$1 ORDER BY g.server_id`, profile.ProfileRevisionID)
		if err != nil {
			return bundle, "", err
		}
		profileServers := []RoutingProfileServer{}
		for serverRows.Next() {
			var profileServer RoutingProfileServer
			if err := serverRows.Scan(&profileServer.ServerID, &profileServer.MCPConfigRevisionID, &profileServer.AcceptedContractRevisionID, &profileServer.VisibilityMode); err != nil {
				serverRows.Close()
				return bundle, "", err
			}
			profileServers = append(profileServers, profileServer)
		}
		if err := serverRows.Err(); err != nil {
			serverRows.Close()
			return bundle, "", err
		}
		serverRows.Close()
		for _, profileServer := range profileServers {
			server, ok := findStoreRoutingServer(bundle.Servers, profileServer.ServerID)
			if !ok || server.MCPConfigRevisionID != profileServer.MCPConfigRevisionID || !sameStoreOptional(server.AcceptedContractRevisionID, profileServer.AcceptedContractRevisionID) {
				return bundle, "", ErrConflict
			}
			profileServer.ToolOverrides, profileServer.ToolRules, err = s.routingProfileRulesTx(ctx, tx, profile.ProfileRevisionID, server.ServerID)
			if err != nil {
				return bundle, "", err
			}
			profile.Servers = append(profile.Servers, profileServer)
		}
		bundle.Profiles = append(bundle.Profiles, profile)
	}
	_, hash, err := bundle.Canonical()
	if err != nil {
		return bundle, "", err
	}
	return bundle, hash, nil
}

func (s *Store) routingProfileRulesTx(ctx context.Context, tx pgx.Tx, profileRevisionID, serverID string) ([]RoutingToolOverride, []RoutingToolRule, error) {
	rows, err := tx.Query(ctx, `SELECT r.tool_id::text,r.visible,r.decision FROM profile_revision_tool_rules r JOIN mcp_tools t ON t.id=r.tool_id WHERE r.profile_revision_id=$1 AND t.server_id=$2 ORDER BY r.tool_id`, profileRevisionID, serverID)
	if err != nil {
		return nil, nil, err
	}
	overrides, rules := []RoutingToolOverride{}, []RoutingToolRule{}
	for rows.Next() {
		var toolID, decision string
		var visible bool
		if err := rows.Scan(&toolID, &visible, &decision); err != nil {
			return nil, nil, err
		}
		overrides = append(overrides, RoutingToolOverride{ToolID: toolID, Visible: visible})
		rules = append(rules, RoutingToolRule{ToolID: toolID, Decision: decision})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	hiddenRows, err := tx.Query(ctx, `
		SELECT crt.tool_id::text
		FROM profile_revision_mcp_governance g
		JOIN mcp_contract_revision_tools crt ON crt.contract_revision_id=g.accepted_contract_revision_id
		WHERE g.profile_revision_id=$1
		  AND g.server_id=$2
		  AND g.visibility_mode='all_accepted'
		  AND crt.status='new_hidden'
		  AND NOT EXISTS (
			SELECT 1 FROM mcp_tool_renames renamed
			WHERE renamed.new_tool_id=crt.tool_id
			  AND renamed.confirmed_added_contract_revision_id=crt.contract_revision_id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM profile_revision_tool_rules r
			WHERE r.profile_revision_id=g.profile_revision_id AND r.tool_id=crt.tool_id
		  )
		ORDER BY crt.tool_id`, profileRevisionID, serverID)
	if err != nil {
		return nil, nil, err
	}
	defer hiddenRows.Close()
	for hiddenRows.Next() {
		var toolID string
		if err := hiddenRows.Scan(&toolID); err != nil {
			return nil, nil, err
		}
		overrides = append(overrides, RoutingToolOverride{ToolID: toolID, Visible: false})
	}
	return overrides, rules, hiddenRows.Err()
}

func classifyRoutingTool(global domain.GlobalPolicyRevision, tool RoutingTool) (string, []string) {
	return classifyGovernanceTool(global, tool.ToolID, tool.Name, mustJSON(tool.InputSchema), mustJSON(tool.OutputSchema), tool.Annotations)
}

func classifyGovernanceTool(global domain.GlobalPolicyRevision, toolID, name string, inputSchema, outputSchema json.RawMessage, annotations map[string]any) (string, []string) {
	var mutating, readOnly bool
	if value, ok := annotations["mutatingHint"].(bool); ok {
		mutating = value
	}
	if value, ok := annotations["readOnlyHint"].(bool); ok {
		readOnly = value
	}
	explicit := global.ExplicitOverrides[toolID]
	if explicit == "" {
		explicit = global.ExplicitOverrides[name]
	}
	classification := policy.Classify(policy.ToolDescriptor{Name: name, InputSchema: inputSchema, OutputSchema: outputSchema, Annotations: annotations, ExplicitOverride: explicit, ReadOnlyHint: readOnly, Mutating: mutating})
	decision := classification.Decision
	hasExplicitOverride := false
	for _, reason := range classification.Reasons {
		if reason.Code == policy.ReasonExplicitOverride {
			hasExplicitOverride = true
			break
		}
	}
	genericMutating := false
	classifiedRisk := false
	for _, reason := range classification.Reasons {
		switch reason.Code {
		case policy.ReasonUnclassifiedMutating:
			if !hasExplicitOverride {
				decision = global.UnclassifiedMutating
			}
		case policy.ReasonReviewedReadOnly:
			if !hasExplicitOverride {
				decision = global.ReviewedReadOnly
			}
		case policy.ReasonAnnotationMutating:
			genericMutating = true
		case policy.ReasonExplicitOverride, policy.ReasonAnnotationDeny,
			policy.ReasonAnnotationDestructive, policy.ReasonCatalogDestructive,
			policy.ReasonCatalogCredential, policy.ReasonCatalogExternalPublish,
			policy.ReasonCatalogFinancial, policy.ReasonCatalogCommand,
			policy.ReasonSchemaMutating:
			classifiedRisk = true
		}
	}
	if genericMutating && !classifiedRisk && !hasExplicitOverride {
		decision = global.UnclassifiedMutating
	}
	reasons := make([]string, 0, len(classification.Reasons))
	for _, reason := range classification.Reasons {
		reasons = append(reasons, reason.Code)
	}
	return decision, reasons
}

func mustJSON(value any) json.RawMessage {
	body, _ := json.Marshal(value)
	return body
}

func findStoreRoutingServer(servers []RoutingServer, id string) (RoutingServer, bool) {
	for _, server := range servers {
		if server.ServerID == id {
			return server, true
		}
	}
	return RoutingServer{}, false
}

func sameStoreOptional(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Store) FinalizeProfileApply(ctx context.Context, operationID, profileID, profileRevisionID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var operationKind, operationStatus, operationSource string
	var operationMetadata json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT kind,status,coalesce(source_id::text,''),metadata FROM operations WHERE id=$1 FOR UPDATE`, operationID).Scan(&operationKind, &operationStatus, &operationSource, &operationMetadata); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if operationKind != "apply" || operationStatus != "running" || operationSource != profileID || stringMetadata(operationMetadata, "profileRevisionId") != profileRevisionID {
		return ErrConflict
	}
	var total, succeeded, pending int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='succeeded'),count(*) FILTER (WHERE governance_finalization_pending) FROM operation_targets WHERE operation_id=$1`, operationID).Scan(&total, &succeeded, &pending); err != nil {
		return err
	}
	if total == 0 || total != succeeded || total != pending {
		return ErrConflict
	}
	routingHash := stringMetadata(operationMetadata, "routingHash")
	var invalidRequests, relayTargets, reloadedRelayTargets int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE ot.request->>'sourceKind' <> 'profile_apply' OR ot.request->>'sourceId' <> $2), count(*) FILTER (WHERE t.runtime='shared-relay'), count(*) FILTER (WHERE t.runtime='shared-relay' AND ot.status='succeeded' AND ot.result->>'routingReloaded'='true' AND ot.result->>'routingHash'=$3) FROM operation_targets ot JOIN targets t ON t.id=ot.target_id WHERE ot.operation_id=$1`, operationID, profileID, routingHash).Scan(&invalidRequests, &relayTargets, &reloadedRelayTargets); err != nil {
		return err
	}
	if invalidRequests != 0 || relayTargets != 1 || reloadedRelayTargets != 1 || !bridgeprotocol.IsSHA256(routingHash) {
		return ErrConflict
	}
	var owner, clientKind string
	var profileRevision int64
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT profile_id::text,revision,client_kind FROM profile_revisions WHERE id=$1 FOR SHARE`, profileRevisionID).Scan(&owner, &profileRevision, &clientKind); err != nil {
		return err
	}
	if owner != profileID {
		return ErrConflict
	}
	if err := tx.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM profiles WHERE id=$1 FOR SHARE`, profileID).Scan(&archived); err != nil {
		return err
	}
	if archived {
		return ErrConflict
	}
	if err := validatePublishedProfilePredecessorTx(ctx, tx, profileID, stringMetadata(operationMetadata, "expectedPublishedProfileRevisionId")); err != nil {
		return err
	}
	if err := s.validateCandidateRoutingHashTx(ctx, tx, RoutingBundleCandidate{PublishedProfileRevisions: map[string]string{profileID: profileRevisionID}}, routingHash); err != nil {
		return err
	}
	snapshots, err := profileApplySnapshotsTx(ctx, tx, operationID, profileID, profileRevision, clientKind, routingHash)
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		if _, err := pinDesiredSnapshotTx(ctx, tx, snapshot.targetID, "profile_apply", profileID, snapshot.operationTargetID, snapshot.manifest, true); err != nil {
			return err
		}
	}
	if err := publishProfileTx(ctx, tx, profileID, profileRevisionID); err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]string{"operationId": operationID, "profileRevisionId": profileRevisionID})
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata) VALUES($1,'profile_publish','profile',$2,'success',$3)`, uuid.NewString(), profileID, jsonText(metadata)); err != nil {
		return err
	}
	if err := markGovernanceFinalizedTx(ctx, tx, operationID, "profile_apply"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type profileApplySnapshot struct {
	operationTargetID string
	targetID          string
	manifest          bridgeprotocol.DesiredManifest
}

func profileApplySnapshotsTx(ctx context.Context, tx pgx.Tx, operationID, profileID string, profileRevision int64, clientKind, routingHash string) ([]profileApplySnapshot, error) {
	rows, err := tx.Query(ctx, `SELECT ot.id::text,ot.target_id::text,t.runtime,coalesce(ot.request->>'sourceKind',''),coalesce(ot.request->>'sourceId',''),ot.request->'manifest' FROM operation_targets ot JOIN targets t ON t.id=ot.target_id WHERE ot.operation_id=$1 ORDER BY ot.target_id`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]profileApplySnapshot, 0, 2)
	clientTargets, relayTargets := 0, 0
	for rows.Next() {
		var item profileApplySnapshot
		var runtime, sourceKind, sourceID string
		var manifestJSON []byte
		if err := rows.Scan(&item.operationTargetID, &item.targetID, &runtime, &sourceKind, &sourceID, &manifestJSON); err != nil {
			return nil, err
		}
		manifest, err := bridgeprotocol.DecodeManifest(manifestJSON, true)
		if err != nil || sourceKind != "profile_apply" || sourceID != profileID || manifest.Target.ID != item.targetID || manifest.Target.Runtime != runtime || manifest.ProfileID != profileID || manifest.ProfileRevision != profileRevision {
			return nil, ErrConflict
		}
		switch runtime {
		case clientKind:
			if manifest.SchemaVersion != bridgeprotocol.ManifestSchemaVersion || manifest.RelayGovernance != nil {
				return nil, ErrConflict
			}
			clientTargets++
		case domain.RuntimeSharedRelay:
			if manifest.SchemaVersion != bridgeprotocol.ManifestSchemaVersionV2 || manifest.RelayGovernance == nil || manifest.RelayGovernance.RoutingHash != routingHash {
				return nil, ErrConflict
			}
			relayTargets++
		default:
			return nil, ErrConflict
		}
		item.manifest = manifest
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != 2 || clientTargets != 1 || relayTargets != 1 {
		return nil, ErrConflict
	}
	return result, nil
}
