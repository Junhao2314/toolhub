package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/profilebundle"
)

const maxProfileMembers = 500

type ProfileInput struct {
	Name            string                      `json:"name"`
	Description     string                      `json:"description"`
	ClientKind      string                      `json:"clientKind,omitempty"`
	Category        string                      `json:"category,omitempty"`
	Variant         string                      `json:"variant,omitempty"`
	MigrationState  string                      `json:"migrationState,omitempty"`
	SkillIDs        []string                    `json:"skillIds"`
	MCPServerIDs    []string                    `json:"mcpServerIds"`
	SkillVersionIDs map[string]string           `json:"skillVersionIds,omitempty"`
	MCPRevisionIDs  map[string]string           `json:"mcpRevisionIds,omitempty"`
	MCPGovernance   []ProfileMCPGovernanceInput `json:"mcpGovernance,omitempty"`
	ToolRules       []ProfileToolRuleInput      `json:"toolRules,omitempty"`
	Revision        int64                       `json:"revision,omitempty"`
	ArchivedRestore bool                        `json:"-"`
	PendingBindings bool                        `json:"-"`
}

type ProfileMCPGovernanceInput struct {
	ServerID                   string `json:"serverId"`
	MCPRevisionID              string `json:"mcpRevisionId"`
	AcceptedContractRevisionID string `json:"acceptedContractRevisionId,omitempty"`
	VisibilityMode             string `json:"visibilityMode"`
}

type ProfileToolRuleInput struct {
	ToolID      string   `json:"toolId"`
	Visible     bool     `json:"visible"`
	Decision    string   `json:"decision"`
	ReasonCodes []string `json:"reasonCodes,omitempty"`
}

type profilePins struct {
	Skills []domain.ProfileSkillPin
	MCP    []domain.ProfileMCPPin
}

func (s *Store) SaveProfile(ctx context.Context, id string, input ProfileInput) (domain.Profile, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Profile{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	profileID, _, err := s.saveProfileTx(ctx, tx, id, input)
	if err != nil {
		return domain.Profile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Profile{}, err
	}
	return s.Profile(ctx, profileID)
}

func (s *Store) saveProfileTx(ctx context.Context, tx pgx.Tx, id string, input ProfileInput) (string, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" || len(input.Name) > 120 {
		return "", "", errors.New("Profile name must contain 1-120 characters")
	}
	input.SkillIDs = uniqueIDs(input.SkillIDs)
	input.MCPServerIDs = uniqueIDs(input.MCPServerIDs)
	if len(input.SkillIDs) > maxProfileMembers || len(input.MCPServerIDs) > maxProfileMembers {
		return "", "", errors.New("Profile membership exceeds safety limit")
	}
	input.ClientKind = strings.TrimSpace(input.ClientKind)
	input.Category = strings.TrimSpace(input.Category)
	input.Variant = strings.TrimSpace(input.Variant)
	variantProvided := input.Variant != ""
	if input.ClientKind != "" && input.ClientKind != "claude" && input.ClientKind != "codex" && input.ClientKind != "shared" && input.ClientKind != "unknown" {
		return "", "", errors.New("invalid Profile client kind")
	}
	if input.Variant == "" {
		input.Variant = "standard"
	}
	if len(input.Category) > 120 || len(input.Variant) > 64 {
		return "", "", errors.New("invalid Profile category or variant")
	}
	if id == "" {
		id = uuid.NewString()
	}
	if uuid.Validate(id) != nil {
		return "", "", ErrNotFound
	}

	var current int64
	var archived bool
	var existingClientKind, existingCategory, existingVariant, existingMigrationState string
	err := tx.QueryRow(ctx, `SELECT revision,archived_at IS NOT NULL,client_kind,category,variant,migration_state FROM profiles WHERE id=$1 FOR UPDATE`, id).Scan(&current, &archived, &existingClientKind, &existingCategory, &existingVariant, &existingMigrationState)
	if errors.Is(err, pgx.ErrNoRows) {
		current = 0
		archived = false
		existingClientKind, existingCategory, existingVariant, existingMigrationState = "unknown", "", "standard", "needs_review"
	} else if err != nil {
		return "", "", err
	}
	if current > 0 && input.Revision != current {
		return "", "", ErrConflict
	}
	if archived && !input.ArchivedRestore {
		return "", "", ErrConflict
	}
	if input.ClientKind == "" {
		input.ClientKind = existingClientKind
	}
	if input.Category == "" {
		input.Category = existingCategory
	}
	if !variantProvided && existingVariant != "" {
		input.Variant = existingVariant
	}
	if input.MigrationState == "" {
		input.MigrationState = existingMigrationState
		if current == 0 {
			switch input.ClientKind {
			case "claude", "codex":
				input.MigrationState = "ready"
			case "shared":
				input.MigrationState = "compatibility"
			default:
				input.MigrationState = "needs_review"
			}
		}
	}
	if input.MigrationState != "ready" && input.MigrationState != "needs_review" && input.MigrationState != "compatibility" {
		return "", "", errors.New("invalid Profile migration state")
	}

	pins, err := resolveProfilePinsTx(ctx, tx, input)
	if err != nil {
		return "", "", err
	}
	profileServers := make(map[string]struct{}, len(pins.MCP))
	for _, pin := range pins.MCP {
		profileServers[pin.ServerID] = struct{}{}
	}
	governance := make(map[string]ProfileMCPGovernanceInput, len(input.MCPGovernance))
	for _, item := range input.MCPGovernance {
		if item.ServerID == "" || item.VisibilityMode == "" {
			return "", "", errors.New("invalid Profile MCP governance")
		}
		if item.VisibilityMode != "all_accepted" && item.VisibilityMode != "selected" && item.VisibilityMode != "hidden" {
			return "", "", errors.New("invalid Profile MCP visibility")
		}
		if _, exists := governance[item.ServerID]; exists {
			return "", "", errors.New("duplicate Profile MCP governance")
		}
		governance[item.ServerID] = item
	}
	for serverID := range governance {
		if _, ok := profileServers[serverID]; !ok {
			return "", "", ErrConflict
		}
	}
	for _, rule := range input.ToolRules {
		var serverID string
		if err := tx.QueryRow(ctx, `SELECT server_id::text FROM mcp_tools WHERE id=$1`, rule.ToolID).Scan(&serverID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", "", ErrNotFound
			}
			return "", "", err
		}
		if _, ok := profileServers[serverID]; !ok {
			return "", "", ErrConflict
		}
		contractRevisionID := strings.TrimSpace(governance[serverID].AcceptedContractRevisionID)
		if contractRevisionID == "" {
			return "", "", ErrConflict
		}
		var acceptedTool bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_contract_revision_tools WHERE contract_revision_id=$1 AND tool_id=$2)`, contractRevisionID, rule.ToolID).Scan(&acceptedTool); err != nil {
			return "", "", err
		}
		if !acceptedTool {
			return "", "", ErrConflict
		}
	}
	if err := validateProfileToolCeiling(ctx, tx, input.ToolRules); err != nil {
		return "", "", err
	}
	canonicalHash, err := CanonicalGovernedProfileHash(input, pins.Skills, pins.MCP)
	if err != nil {
		return "", "", err
	}

	revision := current + 1
	revisionID := uuid.NewString()
	if current == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO profiles(id,name,description,client_kind,category,variant,migration_state,revision,current_revision_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, input.Name, input.Description, input.ClientKind, input.Category, input.Variant, input.MigrationState, revision, revisionID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE profiles SET name=$2,description=$3,client_kind=$4,category=$5,variant=$6,migration_state=$7,revision=$8,current_revision_id=$9,archived_at=NULL,updated_at=now() WHERE id=$1`, id, input.Name, input.Description, input.ClientKind, input.Category, input.Variant, input.MigrationState, revision, revisionID)
	}
	if err != nil {
		if isUniqueViolation(err) {
			return "", "", ErrConflict
		}
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO profile_revisions(id,profile_id,revision,name,description,client_kind,category,variant,migration_state,canonical_hash,pending_bindings,archived_restore) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, revisionID, id, revision, input.Name, input.Description, input.ClientKind, input.Category, input.Variant, input.MigrationState, canonicalHash, input.PendingBindings, input.ArchivedRestore); err != nil {
		return "", "", err
	}
	for position, pin := range pins.Skills {
		if _, err := tx.Exec(ctx, `INSERT INTO profile_revision_skills(profile_revision_id,skill_id,skill_version_id,position) VALUES($1,$2,$3,$4)`, revisionID, pin.SkillID, pin.VersionID, position); err != nil {
			return "", "", err
		}
	}
	for position, pin := range pins.MCP {
		if _, err := tx.Exec(ctx, `INSERT INTO profile_revision_mcp_servers(profile_revision_id,server_id,mcp_revision_id,position) VALUES($1,$2,$3,$4)`, revisionID, pin.ServerID, pin.RevisionID, position); err != nil {
			return "", "", err
		}
	}
	for _, pin := range pins.MCP {
		item := governance[pin.ServerID]
		if item.MCPRevisionID != "" && item.MCPRevisionID != pin.RevisionID {
			return "", "", ErrConflict
		}
		if item.VisibilityMode == "" {
			item.VisibilityMode = "all_accepted"
		}
		var acceptedRevisionID string
		err := tx.QueryRow(ctx, `SELECT coalesce(accepted_revision_id::text,'') FROM mcp_contract_state WHERE server_id=$1`, pin.ServerID).Scan(&acceptedRevisionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", "", err
		}
		if strings.TrimSpace(item.AcceptedContractRevisionID) != acceptedRevisionID {
			return "", "", ErrConflict
		}
		var accepted any
		if strings.TrimSpace(item.AcceptedContractRevisionID) != "" {
			accepted = item.AcceptedContractRevisionID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO profile_revision_mcp_governance(profile_revision_id,server_id,mcp_revision_id,accepted_contract_revision_id,visibility_mode) VALUES($1,$2,$3,$4,$5)`, revisionID, pin.ServerID, pin.RevisionID, accepted, item.VisibilityMode); err != nil {
			return "", "", err
		}
	}
	for _, rule := range input.ToolRules {
		if _, err := tx.Exec(ctx, `INSERT INTO profile_revision_tool_rules(profile_revision_id,tool_id,visible,decision,reason_codes) VALUES($1,$2,$3,$4,$5)`, revisionID, rule.ToolID, rule.Visible, rule.Decision, rule.ReasonCodes); err != nil {
			return "", "", err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO profile_revision_seals(profile_revision_id) VALUES($1)`, revisionID); err != nil {
		return "", "", err
	}
	if err := replaceProfileHeadProjection(ctx, tx, id, pins); err != nil {
		return "", "", err
	}
	return id, revisionID, nil
}

func resolveProfilePinsTx(ctx context.Context, tx pgx.Tx, input ProfileInput) (profilePins, error) {
	result := profilePins{Skills: []domain.ProfileSkillPin{}, MCP: []domain.ProfileMCPPin{}}
	for _, skillID := range input.SkillIDs {
		if uuid.Validate(skillID) != nil {
			return profilePins{}, ErrNotFound
		}
		versionID := strings.TrimSpace(input.SkillVersionIDs[skillID])
		var pin domain.ProfileSkillPin
		err := tx.QueryRow(ctx, `SELECT sk.id::text,v.id::text,sk.slug,sk.name,a.canonical_sha256,a.content_hash,v.id=sk.current_version_id FROM skills sk JOIN skill_versions v ON v.skill_id=sk.id JOIN skill_artifacts a ON a.id=v.artifact_id WHERE sk.id=$1 AND v.id=coalesce(nullif($2,'')::uuid,sk.current_version_id) AND sk.archived_at IS NULL`, skillID, versionID).Scan(&pin.SkillID, &pin.VersionID, &pin.Slug, &pin.Name, &pin.SHA256, &pin.ContentHash, &pin.Current)
		if errors.Is(err, pgx.ErrNoRows) {
			return profilePins{}, ErrNotFound
		}
		if err != nil {
			return profilePins{}, err
		}
		result.Skills = append(result.Skills, pin)
	}
	for _, serverID := range input.MCPServerIDs {
		if uuid.Validate(serverID) != nil {
			return profilePins{}, ErrNotFound
		}
		revisionID := strings.TrimSpace(input.MCPRevisionIDs[serverID])
		var pin domain.ProfileMCPPin
		err := tx.QueryRow(ctx, `SELECT mr.server_id::text,mr.id::text,mr.revision,mr.name,mr.description,mr.transport,mr.command,mr.args,mr.url,mr.env_slots,mr.header_slots,mr.content_hash,mr.id=ms.current_revision_id FROM mcp_revisions mr JOIN mcp_servers ms ON ms.id=mr.server_id WHERE mr.server_id=$1 AND mr.id=coalesce(nullif($2,'')::uuid,ms.current_revision_id)`, serverID, revisionID).Scan(&pin.ServerID, &pin.RevisionID, &pin.Revision, &pin.Name, &pin.Description, &pin.Transport, &pin.Command, &pin.Args, &pin.URL, &pin.EnvKeys, &pin.HeaderKeys, &pin.ContentHash, &pin.Current)
		if errors.Is(err, pgx.ErrNoRows) {
			return profilePins{}, ErrNotFound
		}
		if err != nil {
			return profilePins{}, err
		}
		result.MCP = append(result.MCP, pin)
	}
	return result, nil
}

func replaceProfileHeadProjection(ctx context.Context, tx pgx.Tx, profileID string, pins profilePins) error {
	if _, err := tx.Exec(ctx, `DELETE FROM profile_skills WHERE profile_id=$1`, profileID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM profile_mcp_servers WHERE profile_id=$1`, profileID); err != nil {
		return err
	}
	for _, pin := range pins.Skills {
		if _, err := tx.Exec(ctx, `INSERT INTO profile_skills(profile_id,skill_id) VALUES($1,$2)`, profileID, pin.SkillID); err != nil {
			return err
		}
	}
	for _, pin := range pins.MCP {
		if _, err := tx.Exec(ctx, `INSERT INTO profile_mcp_servers(profile_id,server_id) VALUES($1,$2)`, profileID, pin.ServerID); err != nil {
			return err
		}
	}
	return nil
}

func CanonicalProfileHash(name, description string, skills []domain.ProfileSkillPin, servers []domain.ProfileMCPPin) (string, error) {
	manifest := profilebundle.Manifest{Profile: profilebundle.Profile{Name: strings.TrimSpace(name), Description: strings.TrimSpace(description)}}
	for _, pin := range skills {
		manifest.Skills = append(manifest.Skills, profilebundle.Skill{Slug: pin.Slug, SHA256: pin.SHA256, ContentHash: pin.ContentHash})
	}
	for _, pin := range servers {
		item := profilebundle.MCP{Name: pin.Name, Description: pin.Description, Transport: pin.Transport, Command: pin.Command, Args: pin.Args, URL: pin.URL, EnvSlots: pin.EnvKeys, HeaderSlots: pin.HeaderKeys}
		key, err := profilebundle.MCPKey(item)
		if err != nil {
			return "", err
		}
		item.Key = key
		manifest.MCPServers = append(manifest.MCPServers, item)
	}
	return profilebundle.CanonicalProfileHash(manifest)
}

func CanonicalGovernedProfileHash(input ProfileInput, skills []domain.ProfileSkillPin, servers []domain.ProfileMCPPin) (string, error) {
	base, err := CanonicalProfileHash(input.Name, input.Description, skills, servers)
	if err != nil {
		return "", err
	}
	// Preserve the legacy portable hash for unclassified imported Profiles. New
	// governance fields are included as soon as a Profile opts into them.
	if input.ClientKind == "unknown" && input.Category == "" && input.Variant == "standard" && input.MigrationState == "needs_review" && len(input.MCPGovernance) == 0 && len(input.ToolRules) == 0 {
		return base, nil
	}
	provided := make(map[string]ProfileMCPGovernanceInput, len(input.MCPGovernance))
	for _, item := range input.MCPGovernance {
		provided[item.ServerID] = item
	}
	governance := make([]ProfileMCPGovernanceInput, 0, len(servers))
	for _, pin := range servers {
		item := provided[pin.ServerID]
		item.ServerID = pin.ServerID
		item.MCPRevisionID = pin.RevisionID
		if item.VisibilityMode == "" {
			item.VisibilityMode = "all_accepted"
		}
		governance = append(governance, item)
	}
	sort.Slice(governance, func(i, j int) bool { return governance[i].ServerID < governance[j].ServerID })
	rules := append([]ProfileToolRuleInput(nil), input.ToolRules...)
	sort.Slice(rules, func(i, j int) bool { return rules[i].ToolID < rules[j].ToolID })
	body, err := json.Marshal(struct {
		Base           string                      `json:"base"`
		ClientKind     string                      `json:"clientKind"`
		Category       string                      `json:"category"`
		Variant        string                      `json:"variant"`
		MigrationState string                      `json:"migrationState"`
		MCPGovernance  []ProfileMCPGovernanceInput `json:"mcpGovernance"`
		ToolRules      []ProfileToolRuleInput      `json:"toolRules"`
	}{base, input.ClientKind, input.Category, input.Variant, input.MigrationState, governance, rules})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) ListProfiles(ctx context.Context, includeArchived bool) (json.RawMessage, error) {
	query := `SELECT id::text FROM profiles WHERE archived_at IS NULL ORDER BY lower(name),id`
	if includeArchived {
		query = `SELECT id::text FROM profiles ORDER BY (archived_at IS NOT NULL), lower(name), id`
	}
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := []domain.Profile{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		profile, err := s.Profile(ctx, id)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(profiles)
}

func (s *Store) Profile(ctx context.Context, id string) (domain.Profile, error) {
	var profile domain.Profile
	err := s.pool.QueryRow(ctx, `SELECT p.id::text,p.current_revision_id::text,p.name,p.description,p.client_kind,p.category,p.variant,p.migration_state,p.revision,pr.canonical_hash,pr.pending_bindings,p.archived_at,p.created_at,p.updated_at FROM profiles p JOIN profile_revisions pr ON pr.id=p.current_revision_id WHERE p.id=$1`, id).Scan(&profile.ID, &profile.CurrentRevisionID, &profile.Name, &profile.Description, &profile.ClientKind, &profile.Category, &profile.Variant, &profile.MigrationState, &profile.Revision, &profile.CanonicalHash, &profile.PendingBindings, &profile.ArchivedAt, &profile.CreatedAt, &profile.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Profile{}, ErrNotFound
	}
	if err != nil {
		return domain.Profile{}, err
	}
	revision, err := s.ProfileRevision(ctx, profile.CurrentRevisionID)
	if err != nil {
		return domain.Profile{}, err
	}
	profile.Skills, profile.MCPServers = revision.Skills, revision.MCPServers
	profile.SkillIDs = make([]string, 0, len(profile.Skills))
	profile.MCPServerIDs = make([]string, 0, len(profile.MCPServers))
	for _, pin := range profile.Skills {
		profile.SkillIDs = append(profile.SkillIDs, pin.SkillID)
	}
	for _, pin := range profile.MCPServers {
		profile.MCPServerIDs = append(profile.MCPServerIDs, pin.ServerID)
	}
	profile.EffectiveVisibleCount, err = s.ProfileEffectiveVisibleCount(ctx, profile.CurrentRevisionID)
	if err != nil {
		return domain.Profile{}, err
	}
	return profile, nil
}

func (s *Store) ProfileEffectiveVisibleCount(ctx context.Context, revisionID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM profile_revision_mcp_governance g
		JOIN mcp_contract_revision_tools crt ON crt.contract_revision_id=g.accepted_contract_revision_id
		JOIN mcp_tools t ON t.id=crt.tool_id
		LEFT JOIN profile_revision_tool_rules r ON r.profile_revision_id=g.profile_revision_id AND r.tool_id=t.id
		JOIN global_policy_state gps ON gps.singleton
		JOIN global_policy_revisions gp ON gp.id=gps.applied_revision_id
		WHERE g.profile_revision_id=$1
		  AND CASE WHEN r.tool_id IS NOT NULL THEN r.visible ELSE g.visibility_mode='all_accepted' END
		  AND coalesce(r.decision,'allow') <> 'deny'
		  AND coalesce(gp.explicit_overrides->>t.id::text,gp.explicit_overrides->>t.name,gp.unclassified_mutating) <> 'deny'`, revisionID).Scan(&count)
	return count, err
}

func (s *Store) ProfileRevision(ctx context.Context, id string) (domain.ProfileRevision, error) {
	var revision domain.ProfileRevision
	err := s.pool.QueryRow(ctx, `SELECT id::text,profile_id::text,revision,name,description,client_kind,category,variant,migration_state,canonical_hash,pending_bindings,archived_restore,created_at FROM profile_revisions WHERE id=$1`, id).Scan(&revision.ID, &revision.ProfileID, &revision.Revision, &revision.Name, &revision.Description, &revision.ClientKind, &revision.Category, &revision.Variant, &revision.MigrationState, &revision.CanonicalHash, &revision.PendingBindings, &revision.ArchivedRestore, &revision.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ProfileRevision{}, ErrNotFound
	}
	if err != nil {
		return domain.ProfileRevision{}, err
	}
	revision.Skills = []domain.ProfileSkillPin{}
	rows, err := s.pool.Query(ctx, `SELECT prs.skill_id::text,prs.skill_version_id::text,sk.slug,sk.name,a.canonical_sha256,a.content_hash,prs.skill_version_id=sk.current_version_id FROM profile_revision_skills prs JOIN skills sk ON sk.id=prs.skill_id JOIN skill_versions sv ON sv.id=prs.skill_version_id JOIN skill_artifacts a ON a.id=sv.artifact_id WHERE prs.profile_revision_id=$1 ORDER BY prs.position`, id)
	if err != nil {
		return domain.ProfileRevision{}, err
	}
	for rows.Next() {
		var pin domain.ProfileSkillPin
		if err := rows.Scan(&pin.SkillID, &pin.VersionID, &pin.Slug, &pin.Name, &pin.SHA256, &pin.ContentHash, &pin.Current); err != nil {
			rows.Close()
			return domain.ProfileRevision{}, err
		}
		revision.Skills = append(revision.Skills, pin)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.ProfileRevision{}, err
	}
	rows.Close()
	revision.MCPServers = []domain.ProfileMCPPin{}
	rows, err = s.pool.Query(ctx, `SELECT prms.server_id::text,prms.mcp_revision_id::text,mr.revision,mr.name,mr.description,mr.transport,mr.command,mr.args,mr.url,mr.env_slots,mr.header_slots,mr.content_hash,prms.mcp_revision_id=ms.current_revision_id FROM profile_revision_mcp_servers prms JOIN mcp_revisions mr ON mr.id=prms.mcp_revision_id JOIN mcp_servers ms ON ms.id=prms.server_id WHERE prms.profile_revision_id=$1 ORDER BY prms.position`, id)
	if err != nil {
		return domain.ProfileRevision{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var pin domain.ProfileMCPPin
		if err := rows.Scan(&pin.ServerID, &pin.RevisionID, &pin.Revision, &pin.Name, &pin.Description, &pin.Transport, &pin.Command, &pin.Args, &pin.URL, &pin.EnvKeys, &pin.HeaderKeys, &pin.ContentHash, &pin.Current); err != nil {
			return domain.ProfileRevision{}, err
		}
		revision.MCPServers = append(revision.MCPServers, pin)
	}
	return revision, rows.Err()
}

func (s *Store) ProfileHistory(ctx context.Context, profileID string) ([]domain.ProfileRevision, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM profile_revisions WHERE profile_id=$1 ORDER BY revision DESC`, profileID)
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
	result := make([]domain.ProfileRevision, 0, len(ids))
	for _, id := range ids {
		revision, err := s.ProfileRevision(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, revision)
	}
	return result, nil
}

func (s *Store) profileGovernanceInputs(ctx context.Context, revisionID string) ([]ProfileMCPGovernanceInput, []ProfileToolRuleInput, error) {
	governance := []ProfileMCPGovernanceInput{}
	rows, err := s.pool.Query(ctx, `SELECT server_id::text,mcp_revision_id::text,coalesce(accepted_contract_revision_id::text,''),visibility_mode FROM profile_revision_mcp_governance WHERE profile_revision_id=$1 ORDER BY server_id`, revisionID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var item ProfileMCPGovernanceInput
		if err := rows.Scan(&item.ServerID, &item.MCPRevisionID, &item.AcceptedContractRevisionID, &item.VisibilityMode); err != nil {
			rows.Close()
			return nil, nil, err
		}
		governance = append(governance, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	rules := []ProfileToolRuleInput{}
	rows, err = s.pool.Query(ctx, `SELECT tool_id::text,visible,decision,reason_codes FROM profile_revision_tool_rules WHERE profile_revision_id=$1 ORDER BY tool_id`, revisionID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ProfileToolRuleInput
		if err := rows.Scan(&item.ToolID, &item.Visible, &item.Decision, &item.ReasonCodes); err != nil {
			return nil, nil, err
		}
		rules = append(rules, item)
	}
	return governance, rules, rows.Err()
}

func (s *Store) RefreshProfile(ctx context.Context, id string, expectedRevision int64) (domain.Profile, error) {
	profile, err := s.Profile(ctx, id)
	if err != nil {
		return domain.Profile{}, err
	}
	if profile.PendingBindings {
		return domain.Profile{}, ErrConflict
	}
	governance, rules, err := s.profileGovernanceInputs(ctx, profile.CurrentRevisionID)
	if err != nil {
		return domain.Profile{}, err
	}
	for index := range governance {
		governance[index].MCPRevisionID = ""
	}
	return s.SaveProfile(ctx, id, ProfileInput{Name: profile.Name, Description: profile.Description, ClientKind: profile.ClientKind, Category: profile.Category, Variant: profile.Variant, MigrationState: profile.MigrationState, SkillIDs: profile.SkillIDs, MCPServerIDs: profile.MCPServerIDs, MCPGovernance: governance, ToolRules: rules, Revision: expectedRevision})
}

func (s *Store) CloneProfile(ctx context.Context, id, name string) (domain.Profile, error) {
	profile, err := s.Profile(ctx, id)
	if err != nil {
		return domain.Profile{}, err
	}
	if profile.PendingBindings {
		return domain.Profile{}, ErrConflict
	}
	governance, rules, err := s.profileGovernanceInputs(ctx, profile.CurrentRevisionID)
	if err != nil {
		return domain.Profile{}, err
	}
	input := ProfileInput{Name: name, Description: profile.Description, ClientKind: profile.ClientKind, Category: profile.Category, Variant: profile.Variant, MigrationState: profile.MigrationState, SkillIDs: profile.SkillIDs, MCPServerIDs: profile.MCPServerIDs, SkillVersionIDs: profileSkillVersions(profile), MCPRevisionIDs: profileMCPRevisions(profile), MCPGovernance: governance, ToolRules: rules}
	return s.SaveProfile(ctx, uuid.NewString(), input)
}

func profileSkillVersions(profile domain.Profile) map[string]string {
	result := make(map[string]string, len(profile.Skills))
	for _, pin := range profile.Skills {
		result[pin.SkillID] = pin.VersionID
	}
	return result
}

func profileMCPRevisions(profile domain.Profile) map[string]string {
	result := make(map[string]string, len(profile.MCPServers))
	for _, pin := range profile.MCPServers {
		result[pin.ServerID] = pin.RevisionID
	}
	return result
}

func (s *Store) ArchiveProfile(ctx context.Context, id string, expectedRevision int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var published, isDefault bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM published_profiles WHERE profile_id=$1),EXISTS(SELECT 1 FROM relay_configuration_state WHERE singleton AND default_profile_id=$1)`, id).Scan(&published, &isDefault); err != nil {
		return err
	}
	if published || isDefault {
		return ErrConflict
	}
	command, err := tx.Exec(ctx, `UPDATE profiles SET archived_at=now(),updated_at=now() WHERE id=$1 AND revision=$2 AND archived_at IS NULL`, id, expectedRevision)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

// DeleteProfile is retained for the existing Browser route and now performs
// the reversible archive transition. Irreversible removal uses PurgeProfile.
func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	profile, err := s.Profile(ctx, id)
	if err != nil {
		return err
	}
	return s.ArchiveProfile(ctx, id, profile.Revision)
}

func (s *Store) RestoreArchivedProfile(ctx context.Context, id string, expectedRevision int64) (domain.Profile, error) {
	profile, err := s.Profile(ctx, id)
	if err != nil {
		return domain.Profile{}, err
	}
	governance, rules, err := s.profileGovernanceInputs(ctx, profile.CurrentRevisionID)
	if err != nil {
		return domain.Profile{}, err
	}
	input := ProfileInput{Name: profile.Name, Description: profile.Description, ClientKind: profile.ClientKind, Category: profile.Category, Variant: profile.Variant, MigrationState: profile.MigrationState, SkillIDs: profile.SkillIDs, MCPServerIDs: profile.MCPServerIDs, SkillVersionIDs: profileSkillVersions(profile), MCPRevisionIDs: profileMCPRevisions(profile), MCPGovernance: governance, ToolRules: rules, Revision: expectedRevision, ArchivedRestore: true, PendingBindings: profile.PendingBindings}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Profile{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, revisionID, err := s.saveProfileTx(ctx, tx, id, input)
	if err != nil {
		return domain.Profile{}, err
	}
	if profile.PendingBindings {
		rows, queryErr := tx.Query(ctx, `SELECT mcp_revision_id::text,namespace,key,slot_hash FROM pending_secret_bindings WHERE profile_revision_id=$1`, profile.CurrentRevisionID)
		if queryErr != nil {
			return domain.Profile{}, queryErr
		}
		for rows.Next() {
			var mcpRevisionID, namespace, key, slotHash string
			if scanErr := rows.Scan(&mcpRevisionID, &namespace, &key, &slotHash); scanErr != nil {
				rows.Close()
				return domain.Profile{}, scanErr
			}
			if _, insertErr := tx.Exec(ctx, `INSERT INTO pending_secret_bindings(profile_revision_id,mcp_revision_id,namespace,key,slot_hash) VALUES($1,$2,$3,$4,$5)`, revisionID, mcpRevisionID, namespace, key, slotHash); insertErr != nil {
				rows.Close()
				return domain.Profile{}, insertErr
			}
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			rows.Close()
			return domain.Profile{}, rowsErr
		}
		rows.Close()
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Profile{}, err
	}
	return s.Profile(ctx, id)
}

func (s *Store) PurgeProfile(ctx context.Context, id string) error {
	command, err := s.pool.Exec(ctx, `DELETE FROM profiles p WHERE p.id=$1 AND p.archived_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM desired_snapshots ds WHERE ds.source_kind='profile_apply' AND ds.source_id=p.id) AND NOT EXISTS(SELECT 1 FROM preflight_confirmations pc WHERE pc.profile_id=p.id AND (pc.consumed_at IS NULL OR pc.expires_at>now())) AND NOT EXISTS(SELECT 1 FROM bundle_import_fingerprints bf WHERE bf.profile_id=p.id) AND NOT EXISTS(SELECT 1 FROM operations o WHERE o.source_id=p.id AND o.status IN ('queued','running'))`, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) AdoptRestoredTarget(ctx context.Context, targetID, name string) (domain.Profile, error) {
	snapshot, manifest, err := s.ActiveDesiredManifest(ctx, targetID)
	if err != nil {
		return domain.Profile{}, err
	}
	if snapshot.SourceKind != "restore" {
		return domain.Profile{}, ErrConflict
	}
	input := ProfileInput{Name: name, Description: "Adopted restored desired state", SkillVersionIDs: map[string]string{}, MCPRevisionIDs: map[string]string{}}
	for _, member := range manifest.Skills {
		input.SkillIDs = append(input.SkillIDs, member.SkillID)
		input.SkillVersionIDs[member.SkillID] = member.VersionID
	}
	for _, member := range manifest.MCPServers {
		var revisionID string
		if err := s.pool.QueryRow(ctx, `SELECT id::text FROM mcp_revisions WHERE server_id=$1 AND revision=$2`, member.ServerID, member.Revision).Scan(&revisionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.Profile{}, ErrConflict
			}
			return domain.Profile{}, err
		}
		input.MCPServerIDs = append(input.MCPServerIDs, member.ServerID)
		input.MCPRevisionIDs[member.ServerID] = revisionID
	}
	return s.SaveProfile(ctx, uuid.NewString(), input)
}

func uniqueIDs(ids []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}
