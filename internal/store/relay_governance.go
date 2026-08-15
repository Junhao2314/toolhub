package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	canonicaljson "github.com/gibson042/canonicaljson-go"
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

func (b RoutingBundle) Canonical() ([]byte, string, error) {
	body, err := canonicaljson.Marshal(b)
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
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_configuration_revisions WHERE id=$1)`, revisionID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE relay_configuration_state SET applied_revision_id=$1,updated_at=now() WHERE singleton`, revisionID); err != nil {
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
	return tx.Commit(ctx)
}

func (s *Store) PrepareAffectedProfileUpdates(ctx context.Context, relayRevisionID string) ([]string, error) {
	if uuid.Validate(relayRevisionID) != nil {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT pp.profile_id::text FROM published_profiles pp JOIN profile_revision_mcp_servers pins ON pins.profile_revision_id=pp.profile_revision_id WHERE NOT EXISTS(SELECT 1 FROM relay_configuration_revision_mcp_servers relay WHERE relay.relay_configuration_revision_id=$1 AND relay.server_id=pins.server_id AND relay.mcp_revision_id=pins.mcp_revision_id) ORDER BY pp.profile_id`, relayRevisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
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
	if err := publishProfileTx(ctx, tx, profileID, revisionID); err != nil {
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
	if _, err := validateSucceededGovernanceReloadTx(ctx, tx, operationID, "apply", profileID, ""); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `DELETE FROM published_profiles WHERE profile_id=$1`, profileID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
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
	if kind != expectedKind || status != "succeeded" || (expectedSourceID != "" && sourceID != expectedSourceID) {
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
	if relayTargets == 0 || reloadedRelayTargets != relayTargets {
		return nil, ErrConflict
	}
	return metadata, nil
}

func stringMetadata(metadata json.RawMessage, key string) string {
	var values map[string]any
	if json.Unmarshal(metadata, &values) != nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RoutingBundle{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	bundle, hash, err := s.RenderRoutingBundleTx(ctx, tx)
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
	var bundle RoutingBundle
	var relayID, relayHash, mode, globalID, globalHash string
	var defaultProfile string
	if err := tx.QueryRow(ctx, `SELECT s.mode,s.applied_revision_id::text,r.canonical_hash,coalesce(s.default_profile_id::text,''),g.applied_revision_id::text,gr.canonical_hash FROM relay_configuration_state s JOIN relay_configuration_revisions r ON r.id=s.applied_revision_id CROSS JOIN global_policy_state g JOIN global_policy_revisions gr ON gr.id=g.applied_revision_id WHERE s.singleton AND g.singleton`).Scan(&mode, &relayID, &relayHash, &defaultProfile, &globalID, &globalHash); err != nil {
		return bundle, "", err
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
		server.AcceptedContractRevisionID, server.AcceptedContractHash = entry.acceptedContractRevisionID, entry.acceptedContractHash
		if entry.acceptedContractRevisionID != nil {
			toolRows, err := tx.Query(ctx, `SELECT t.id::text,t.name,crt.input_schema,crt.output_schema,crt.annotations,coalesce(latest.status,crt.status) FROM mcp_contract_revision_tools crt JOIN mcp_tools t ON t.id=crt.tool_id LEFT JOIN mcp_contract_state cs ON cs.server_id=t.server_id LEFT JOIN mcp_contract_revision_tools latest ON latest.contract_revision_id=cs.latest_revision_id AND latest.tool_id=crt.tool_id WHERE crt.contract_revision_id=$1 ORDER BY crt.position`, *entry.acceptedContractRevisionID)
			if err != nil {
				return bundle, "", err
			}
			for toolRows.Next() {
				var tool RoutingTool
				var inputJSON, outputJSON, annotationsJSON []byte
				var contractStatus string
				if err := toolRows.Scan(&tool.ToolID, &tool.Name, &inputJSON, &outputJSON, &annotationsJSON, &contractStatus); err != nil {
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
				tool.Paused = contractStatus == ContractToolPausedIncompatible
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
	profileRows, err := tx.Query(ctx, `SELECT pp.profile_id::text,pp.profile_revision_id::text,pr.canonical_hash,pr.name,pr.client_kind FROM published_profiles pp JOIN profile_revisions pr ON pr.id=pp.profile_revision_id JOIN profiles p ON p.id=pp.profile_id WHERE p.archived_at IS NULL ORDER BY pp.profile_id`)
	if err != nil {
		return bundle, "", err
	}
	profiles := []RoutingProfile{}
	for profileRows.Next() {
		var profile RoutingProfile
		if err := profileRows.Scan(&profile.ProfileID, &profile.ProfileRevisionID, &profile.ProfileRevisionHash, &profile.ProfileName, &profile.ClientKind); err != nil {
			profileRows.Close()
			return bundle, "", err
		}
		if profile.ClientKind != domain.RuntimeClaude && profile.ClientKind != domain.RuntimeCodex {
			profileRows.Close()
			return bundle, "", ErrConflict
		}
		profiles = append(profiles, profile)
	}
	if err := profileRows.Err(); err != nil {
		profileRows.Close()
		return bundle, "", err
	}
	profileRows.Close()
	for _, profile := range profiles {
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
	var mutating, readOnly bool
	if value, ok := tool.Annotations["mutatingHint"].(bool); ok {
		mutating = value
	}
	if value, ok := tool.Annotations["readOnlyHint"].(bool); ok {
		readOnly = value
	}
	explicit := global.ExplicitOverrides[tool.ToolID]
	if explicit == "" {
		explicit = global.ExplicitOverrides[tool.Name]
	}
	classification := policy.Classify(policy.ToolDescriptor{Name: tool.Name, InputSchema: mustJSON(tool.InputSchema), OutputSchema: mustJSON(tool.OutputSchema), Annotations: tool.Annotations, ExplicitOverride: explicit, ReadOnlyHint: readOnly, Mutating: mutating})
	decision := classification.Decision
	for _, reason := range classification.Reasons {
		if reason.Code == policy.ReasonUnclassifiedMutating && !mutating {
			decision = global.ReviewedReadOnly
		}
		if reason.Code == policy.ReasonUnclassified && !mutating && readOnly {
			decision = global.ReviewedReadOnly
		}
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
	if operationKind != "apply" || operationStatus != "succeeded" || operationSource != profileID || stringMetadata(operationMetadata, "profileRevisionId") != profileRevisionID {
		return ErrConflict
	}
	var total, succeeded int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='succeeded') FROM operation_targets WHERE operation_id=$1`, operationID).Scan(&total, &succeeded); err != nil {
		return err
	}
	if total == 0 || total != succeeded {
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
	var owner string
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT profile_id::text FROM profile_revisions WHERE id=$1 FOR SHARE`, profileRevisionID).Scan(&owner); err != nil {
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
	if err := publishProfileTx(ctx, tx, profileID, profileRevisionID); err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]string{"operationId": operationID, "profileRevisionId": profileRevisionID})
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata) VALUES($1,'profile_publish','profile',$2,'success',$3)`, uuid.NewString(), profileID, jsonText(metadata)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
