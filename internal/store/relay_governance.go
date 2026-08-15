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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
)

type RelayConfigurationInput struct {
	Revision       int64
	MCPServerIDs   []string
	MCPRevisionIDs map[string]string
	Metadata       map[string]any
}

type RoutingServer struct {
	ServerID                   string `json:"serverId"`
	MCPRevisionID              string `json:"mcpRevisionId"`
	AcceptedContractRevisionID string `json:"acceptedContractRevisionId,omitempty"`
}

type RoutingProfileServer struct {
	ServerID                   string `json:"serverId"`
	MCPRevisionID              string `json:"mcpRevisionId"`
	AcceptedContractRevisionID string `json:"acceptedContractRevisionId,omitempty"`
	VisibilityMode             string `json:"visibilityMode"`
}

type RoutingProfile struct {
	ProfileID         string                 `json:"profileId"`
	ProfileRevisionID string                 `json:"profileRevisionId"`
	ClientKind        string                 `json:"clientKind"`
	Category          string                 `json:"category"`
	Variant           string                 `json:"variant"`
	Servers           []RoutingProfileServer `json:"servers"`
	ToolRules         []domain.ToolRule      `json:"toolRules"`
}

type RoutingBundle struct {
	SchemaVersion                int                         `json:"schemaVersion"`
	Mode                         string                      `json:"mode"`
	RelayConfigurationRevisionID string                      `json:"relayConfigurationRevisionId"`
	RelayConfigurationHash       string                      `json:"relayConfigurationHash"`
	GlobalPolicyRevisionID       string                      `json:"globalPolicyRevisionId"`
	GlobalPolicyHash             string                      `json:"globalPolicyHash"`
	GlobalPolicy                 domain.GlobalPolicyRevision `json:"globalPolicy"`
	DefaultProfileID             *string                     `json:"defaultProfileId"`
	Servers                      []RoutingServer             `json:"servers"`
	Profiles                     []RoutingProfile            `json:"profiles"`
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var value any
	if strings.TrimSpace(profileID) != "" {
		if uuid.Validate(profileID) != nil {
			return ErrNotFound
		}
		var published bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM published_profiles WHERE profile_id=$1)`, profileID).Scan(&published); err != nil {
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var owner string
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT profile_id::text FROM profile_revisions WHERE id=$1 FOR SHARE`, revisionID).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
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
	if _, err := tx.Exec(ctx, `INSERT INTO published_profiles(profile_id,profile_revision_id) VALUES($1,$2) ON CONFLICT(profile_id) DO UPDATE SET profile_revision_id=EXCLUDED.profile_revision_id,published_at=now()`, profileID, revisionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) UnpublishProfile(ctx context.Context, profileID string) error {
	command, err := s.pool.Exec(ctx, `DELETE FROM published_profiles WHERE profile_id=$1`, profileID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
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
	var bundle RoutingBundle
	var relayID, globalID string
	if err := s.pool.QueryRow(ctx, `SELECT mode FROM relay_configuration_state WHERE singleton`).Scan(&bundle.Mode); err != nil {
		return bundle, "", err
	}
	if err := s.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&relayID); err != nil {
		return bundle, "", err
	}
	var relay domain.RelayConfigurationRevision
	var err error
	relay, err = s.RelayConfiguration(ctx, relayID)
	if err != nil {
		return bundle, "", err
	}
	if err := s.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM global_policy_state WHERE singleton`).Scan(&globalID); err != nil {
		return bundle, "", err
	}
	global, err := s.GlobalPolicy(ctx, globalID)
	if err != nil {
		return bundle, "", err
	}
	bundle.SchemaVersion = 1
	bundle.RelayConfigurationRevisionID, bundle.RelayConfigurationHash = relay.ID, relay.CanonicalHash
	bundle.GlobalPolicyRevisionID, bundle.GlobalPolicyHash = global.ID, global.CanonicalHash
	bundle.GlobalPolicy = global
	var defaultProfile string
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(default_profile_id::text,'') FROM relay_configuration_state WHERE singleton`).Scan(&defaultProfile); err != nil {
		return bundle, "", err
	}
	if defaultProfile != "" {
		bundle.DefaultProfileID = &defaultProfile
	}
	for _, pin := range relay.MCPServers {
		var accepted string
		if err := s.pool.QueryRow(ctx, `SELECT coalesce(accepted_revision_id::text,'') FROM mcp_contract_state WHERE server_id=$1`, pin.ServerID).Scan(&accepted); err != nil {
			return bundle, "", err
		}
		bundle.Servers = append(bundle.Servers, RoutingServer{ServerID: pin.ServerID, MCPRevisionID: pin.MCPRevisionID, AcceptedContractRevisionID: accepted})
	}
	published, err := s.ListPublishedProfiles(ctx)
	if err != nil {
		return bundle, "", err
	}
	for _, publishedProfile := range published {
		item := RoutingProfile{ProfileID: publishedProfile.ProfileID, ProfileRevisionID: publishedProfile.ProfileRevisionID, ClientKind: publishedProfile.ClientKind, Category: publishedProfile.Category, Variant: publishedProfile.Variant, Servers: []RoutingProfileServer{}, ToolRules: []domain.ToolRule{}}
		rows, err := s.pool.Query(ctx, `SELECT server_id::text,mcp_revision_id::text,coalesce(accepted_contract_revision_id::text,''),visibility_mode FROM profile_revision_mcp_governance WHERE profile_revision_id=$1 ORDER BY server_id`, publishedProfile.ProfileRevisionID)
		if err != nil {
			return bundle, "", err
		}
		for rows.Next() {
			var server RoutingProfileServer
			if err := rows.Scan(&server.ServerID, &server.MCPRevisionID, &server.AcceptedContractRevisionID, &server.VisibilityMode); err != nil {
				rows.Close()
				return bundle, "", err
			}
			item.Servers = append(item.Servers, server)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return bundle, "", err
		}
		rows.Close()
		for _, profileServer := range item.Servers {
			found := false
			for _, relayServer := range bundle.Servers {
				if relayServer.ServerID == profileServer.ServerID {
					if relayServer.MCPRevisionID != profileServer.MCPRevisionID {
						return bundle, "", ErrConflict
					}
					found = true
					break
				}
			}
			if !found {
				return bundle, "", ErrConflict
			}
		}
		ruleRows, err := s.pool.Query(ctx, `SELECT profile_revision_id::text,tool_id::text,visible,decision,reason_codes FROM profile_revision_tool_rules WHERE profile_revision_id=$1 ORDER BY tool_id`, publishedProfile.ProfileRevisionID)
		if err != nil {
			return bundle, "", err
		}
		for ruleRows.Next() {
			var rule domain.ToolRule
			if err := ruleRows.Scan(&rule.ProfileRevisionID, &rule.ToolID, &rule.Visible, &rule.Decision, &rule.ReasonCodes); err != nil {
				ruleRows.Close()
				return bundle, "", err
			}
			item.ToolRules = append(item.ToolRules, rule)
		}
		if err := ruleRows.Err(); err != nil {
			ruleRows.Close()
			return bundle, "", err
		}
		ruleRows.Close()
		bundle.Profiles = append(bundle.Profiles, item)
	}
	sort.Slice(bundle.Servers, func(i, j int) bool { return bundle.Servers[i].ServerID < bundle.Servers[j].ServerID })
	sort.Slice(bundle.Profiles, func(i, j int) bool { return bundle.Profiles[i].ProfileID < bundle.Profiles[j].ProfileID })
	body, err := json.Marshal(bundle)
	if err != nil {
		return bundle, "", err
	}
	sum := sha256.Sum256(body)
	return bundle, hex.EncodeToString(sum[:]), nil
}

func (s *Store) FinalizeProfileApply(ctx context.Context, operationID, profileID, profileRevisionID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var operationStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM operations WHERE id=$1 FOR UPDATE`, operationID).Scan(&operationStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if operationStatus != "succeeded" {
		return ErrConflict
	}
	var total, succeeded int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='succeeded') FROM operation_targets WHERE operation_id=$1`, operationID).Scan(&total, &succeeded); err != nil {
		return err
	}
	if total == 0 || total != succeeded {
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
	if _, err := tx.Exec(ctx, `INSERT INTO published_profiles(profile_id,profile_revision_id) VALUES($1,$2) ON CONFLICT(profile_id) DO UPDATE SET profile_revision_id=EXCLUDED.profile_revision_id,published_at=now()`, profileID, profileRevisionID); err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]string{"operationId": operationID, "profileRevisionId": profileRevisionID})
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata) VALUES($1,'profile_publish','profile',$2,'success',$3)`, uuid.NewString(), profileID, jsonText(metadata)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
