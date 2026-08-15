package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/skills"
)

const (
	configImportMarkerKey              = "legacy_config_import_v1"
	configImportLock                   = int64(1848002)
	configImportExpectedMigrationCount = 7
)

var ErrConfigImportConflict = errors.New("legacy configuration import conflict")

type ConfigImportInput struct {
	SourceFingerprint        string
	SourceDatabaseIdentity   string
	Skills                   []ConfigImportSkill
	MCPServers               []ConfigImportMCP
	Secrets                  []ConfigImportSecret
	Profiles                 []ConfigImportProfile
	UpdateCron               string
	Timezone                 string
	MCPRenameCount           int
	TransportConversionCount int
	failAfterPhase           string
}

type ConfigImportSkill struct {
	LegacyID         string
	LegacySourceID   string
	LegacyVersionID  string
	LegacyArtifactID string
	Source           SourceInput
	Package          skills.Package
	Provenance       map[string]any
}

type ConfigImportMCP struct {
	LegacyID   string
	Input      MCPInput
	EnvRefs    map[string]string
	HeaderRefs map[string]string
}

type ConfigImportSecret struct {
	LegacyID   string
	Kind       string
	Ciphertext []byte
}

type ConfigImportProfile struct {
	Name               string
	Description        string
	LegacySkillIDs     []string
	LegacyMCPServerIDs []string
}

type ConfigImportCounts struct {
	Skills             int            `json:"skills"`
	SkillSources       int            `json:"skillSources"`
	SkillArtifacts     int            `json:"skillArtifacts"`
	SkillVersions      int            `json:"skillVersions"`
	MCPServers         int            `json:"mcpServers"`
	EncryptedRecords   int            `json:"encryptedRecords"`
	Profiles           int            `json:"profiles"`
	ProfileSkills      int            `json:"profileSkills"`
	ProfileMCPServers  int            `json:"profileMcpServers"`
	ProfileSkillCounts map[string]int `json:"profileSkillCounts"`
	ProfileMCPCounts   map[string]int `json:"profileMcpCounts"`
}

type configImportMarker struct {
	Version           int                `json:"version"`
	SourceFingerprint string             `json:"sourceFingerprint"`
	Counts            ConfigImportCounts `json:"counts"`
	UpdateCron        string             `json:"updateCron"`
	Timezone          string             `json:"timezone"`
	Renames           int                `json:"renames"`
	TransportChanges  int                `json:"transportChanges"`
}

type ConfigImportResult struct {
	AlreadyImported   bool
	SourceFingerprint string
	Counts            ConfigImportCounts
}

// ConfigImportDestinationNeedsBootstrap distinguishes a schema-only fresh
// database from any destination that has already created its singleton account.
// Full pristine/import-marker validation remains inside ImportLegacyConfig.
func (s *Store) ConfigImportDestinationNeedsBootstrap(ctx context.Context) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())

	var accounts int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM account").Scan(&accounts); err != nil {
		return false, err
	}
	if accounts == 1 {
		return false, nil
	}
	if accounts != 0 {
		return false, fmt.Errorf("%w: destination singleton account count=%d", ErrConfigImportConflict, accounts)
	}

	checks := []struct {
		name     string
		query    string
		expected int
	}{
		{"generation marker", "SELECT count(*) FROM app_meta WHERE key='schema_generation' AND value='2'", 1},
		{"application metadata", "SELECT count(*) FROM app_meta", 1},
		{"migration ledger", "SELECT count(*) FROM schema_migrations WHERE version=1", 1},
		{"migration ledger entries", "SELECT count(*) FROM schema_migrations", configImportExpectedMigrationCount},
		{"sessions", "SELECT count(*) FROM sessions", 0},
		{"settings", "SELECT count(*) FROM settings", 0},
		{"nodes", "SELECT count(*) FROM nodes", 0},
		{"targets", "SELECT count(*) FROM targets", 0},
		{"market providers", "SELECT count(*) FROM market_providers WHERE kind IN ('skillsmp','xiaping')", 2},
		{"all market providers", "SELECT count(*) FROM market_providers", 2},
		{"audit events", "SELECT count(*) FROM audit_events", 0},
	}
	for _, table := range configImportEmptyTables() {
		checks = append(checks, struct {
			name     string
			query    string
			expected int
		}{name: table, query: "SELECT count(*) FROM " + table, expected: 0})
	}
	for _, check := range checks {
		var count int
		if err := tx.QueryRow(ctx, check.query).Scan(&count); err != nil {
			return false, err
		}
		if count != check.expected {
			return false, fmt.Errorf("%w: uninitialized destination %s count=%d expected=%d", ErrConfigImportConflict, check.name, count, check.expected)
		}
	}
	return true, nil
}

func (s *Store) ImportLegacyConfig(ctx context.Context, input ConfigImportInput, legacyCipher *security.Cipher) (ConfigImportResult, error) {
	if err := validateConfigImportInput(input, legacyCipher); err != nil {
		return ConfigImportResult{}, err
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return ConfigImportResult{}, err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ConfigImportResult{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", configImportLock); err != nil {
		return ConfigImportResult{}, err
	}

	destinationIdentity, err := databaseIdentityHash(ctx, tx)
	if err != nil {
		return ConfigImportResult{}, err
	}
	if destinationIdentity == input.SourceDatabaseIdentity {
		return ConfigImportResult{}, fmt.Errorf("%w: source and destination databases are identical", ErrConfigImportConflict)
	}

	var markerBody string
	err = tx.QueryRow(ctx, "SELECT value FROM app_meta WHERE key=$1", configImportMarkerKey).Scan(&markerBody)
	if err == nil {
		var marker configImportMarker
		if json.Unmarshal([]byte(markerBody), &marker) != nil || marker.Version != 1 || marker.SourceFingerprint == "" {
			return ConfigImportResult{}, fmt.Errorf("%w: existing import marker is invalid", ErrConfigImportConflict)
		}
		if marker.SourceFingerprint != input.SourceFingerprint {
			return ConfigImportResult{}, fmt.Errorf("%w: destination was imported from a different source fingerprint", ErrConfigImportConflict)
		}
		expectedCounts := configImportExpectedCounts(input)
		if !reflect.DeepEqual(marker.Counts, expectedCounts) || marker.UpdateCron != input.UpdateCron || marker.Timezone != input.Timezone || marker.Renames != input.MCPRenameCount || marker.TransportChanges != input.TransportConversionCount {
			return ConfigImportResult{}, fmt.Errorf("%w: existing import marker does not match the reviewed source shape", ErrConfigImportConflict)
		}
		if err := verifyConfigImportCore(ctx, tx, marker); err != nil {
			return ConfigImportResult{}, fmt.Errorf("%w: imported destination verification failed: %v", ErrConfigImportConflict, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return ConfigImportResult{}, err
		}
		return ConfigImportResult{AlreadyImported: true, SourceFingerprint: marker.SourceFingerprint, Counts: marker.Counts}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ConfigImportResult{}, err
	}
	if err := validatePristineConfigImportDestination(ctx, tx); err != nil {
		return ConfigImportResult{}, err
	}

	skillMap := make(map[string]string, len(input.Skills))
	for _, item := range input.Skills {
		skillID, err := insertConfigImportSkill(ctx, tx, item)
		if err != nil {
			return ConfigImportResult{}, fmt.Errorf("insert imported Skill %s: %w", item.LegacyID, err)
		}
		skillMap[item.LegacyID] = skillID
	}
	if err := failConfigImportPhase(input, "skills"); err != nil {
		return ConfigImportResult{}, err
	}

	secretMap := make(map[string]string, len(input.Secrets))
	for _, item := range input.Secrets {
		plaintext, err := legacyCipher.Decrypt(item.Ciphertext, item.LegacyID)
		if err != nil {
			return ConfigImportResult{}, errors.New("authenticate legacy encrypted record")
		}
		destinationID := uuid.NewString()
		ciphertext, encryptErr := s.cipher.Encrypt(plaintext, destinationID)
		clear(plaintext)
		if encryptErr != nil {
			return ConfigImportResult{}, encryptErr
		}
		name := "migration:mcp:" + destinationID
		if _, err := tx.Exec(ctx, `INSERT INTO encrypted_secrets(id,name,kind,ciphertext,metadata)
			VALUES($1,$2,$3,$4,'{"writeOnly":true,"migration":"legacy_config_import_v1"}')`, destinationID, name, item.Kind, ciphertext); err != nil {
			return ConfigImportResult{}, err
		}
		secretMap[item.LegacyID] = destinationID
	}
	if err := failConfigImportPhase(input, "secrets"); err != nil {
		return ConfigImportResult{}, err
	}

	mcpMap := make(map[string]string, len(input.MCPServers))
	for _, item := range input.MCPServers {
		serverID, revisionID := uuid.NewString(), uuid.NewString()
		envRefs, err := remapConfigImportRefs(item.EnvRefs, secretMap)
		if err != nil {
			return ConfigImportResult{}, err
		}
		headerRefs, err := remapConfigImportRefs(item.HeaderRefs, secretMap)
		if err != nil {
			return ConfigImportResult{}, err
		}
		argsJSON, _ := json.Marshal(item.Input.Args)
		envJSON, _ := json.Marshal(envRefs)
		headerJSON, _ := json.Marshal(headerRefs)
		contentHash := MCPContentHash(item.Input, envRefs, headerRefs)
		if _, err := tx.Exec(ctx, `INSERT INTO mcp_servers(id,current_revision_id,name,description,revision,transport,command,args,url,env_refs,header_refs,content_hash)
			VALUES($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,$11)`, serverID, revisionID, item.Input.Name, item.Input.Description, item.Input.Transport, item.Input.Command, jsonText(argsJSON), item.Input.URL, jsonText(envJSON), jsonText(headerJSON), contentHash); err != nil {
			return ConfigImportResult{}, err
		}
		provenance, _ := json.Marshal(map[string]any{"source": "generation-1-config-import"})
		if _, err := tx.Exec(ctx, `INSERT INTO mcp_revisions(id,server_id,revision,name,description,transport,command,args,url,env_slots,header_slots,env_refs,header_refs,content_hash,provenance)
			VALUES($1,$2,1,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, revisionID, serverID, item.Input.Name, item.Input.Description, item.Input.Transport, item.Input.Command, jsonText(argsJSON), item.Input.URL, sortedMapKeys(envRefs), sortedMapKeys(headerRefs), jsonText(envJSON), jsonText(headerJSON), contentHash, jsonText(provenance)); err != nil {
			return ConfigImportResult{}, err
		}
		mcpMap[item.LegacyID] = serverID
	}
	if err := failConfigImportPhase(input, "mcp"); err != nil {
		return ConfigImportResult{}, err
	}

	for _, profile := range input.Profiles {
		profileID := uuid.NewString()
		profileInput := ProfileInput{Name: profile.Name, Description: profile.Description}
		for _, legacyID := range uniqueSortedStrings(profile.LegacySkillIDs) {
			skillID := skillMap[legacyID]
			if skillID == "" {
				return ConfigImportResult{}, errors.New("imported Profile references an unavailable Skill")
			}
			profileInput.SkillIDs = append(profileInput.SkillIDs, skillID)
		}
		for _, legacyID := range uniqueSortedStrings(profile.LegacyMCPServerIDs) {
			serverID := mcpMap[legacyID]
			if serverID == "" {
				return ConfigImportResult{}, errors.New("imported Profile references an unavailable MCP definition")
			}
			profileInput.MCPServerIDs = append(profileInput.MCPServerIDs, serverID)
		}
		if _, _, err := s.saveProfileTx(ctx, tx, profileID, profileInput); err != nil {
			return ConfigImportResult{}, err
		}
	}
	if err := failConfigImportPhase(input, "profiles"); err != nil {
		return ConfigImportResult{}, err
	}

	if _, err := tx.Exec(ctx, "UPDATE settings SET update_cron=$1,timezone=$2,updated_at=now() WHERE singleton", input.UpdateCron, input.Timezone); err != nil {
		return ConfigImportResult{}, err
	}
	if err := failConfigImportPhase(input, "settings"); err != nil {
		return ConfigImportResult{}, err
	}

	counts := configImportExpectedCounts(input)
	auditMetadata, _ := json.Marshal(map[string]any{
		"sourceFingerprint":    input.SourceFingerprint,
		"skillCount":           counts.Skills,
		"mcpServerCount":       counts.MCPServers,
		"encryptedRecordCount": counts.EncryptedRecords,
		"profileCount":         counts.Profiles,
		"renameCount":          input.MCPRenameCount,
		"transportChangeCount": input.TransportConversionCount,
	})
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata)
		VALUES($1,'legacy_config_import','configuration',$2,'success',$3)`, uuid.NewString(), input.SourceFingerprint, jsonText(auditMetadata)); err != nil {
		return ConfigImportResult{}, err
	}
	if err := failConfigImportPhase(input, "audit"); err != nil {
		return ConfigImportResult{}, err
	}

	marker := configImportMarker{
		Version: 1, SourceFingerprint: input.SourceFingerprint, Counts: counts,
		UpdateCron: input.UpdateCron, Timezone: input.Timezone,
		Renames: input.MCPRenameCount, TransportChanges: input.TransportConversionCount,
	}
	markerJSON, _ := json.Marshal(marker)
	if _, err := tx.Exec(ctx, "INSERT INTO app_meta(key,value) VALUES($1,$2)", configImportMarkerKey, string(markerJSON)); err != nil {
		return ConfigImportResult{}, err
	}
	if err := failConfigImportPhase(input, "marker"); err != nil {
		return ConfigImportResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ConfigImportResult{}, err
	}
	return ConfigImportResult{SourceFingerprint: input.SourceFingerprint, Counts: counts}, nil
}

func validateConfigImportInput(input ConfigImportInput, legacyCipher *security.Cipher) error {
	if legacyCipher == nil || !isLowerSHA256(input.SourceFingerprint) || !isLowerSHA256(input.SourceDatabaseIdentity) {
		return errors.New("configuration import identity is invalid")
	}
	if len(input.Skills) == 0 || len(input.Profiles) != 3 {
		return errors.New("configuration import selection is incomplete")
	}
	if _, err := cron.ParseStandard(strings.TrimSpace(input.UpdateCron)); err != nil {
		return errors.New("configuration import schedule is invalid")
	}
	if _, err := time.LoadLocation(strings.TrimSpace(input.Timezone)); err != nil {
		return errors.New("configuration import timezone is invalid")
	}
	skillIDs, slugs := map[string]bool{}, map[string]bool{}
	for _, item := range input.Skills {
		if uuid.Validate(item.LegacyID) != nil || skillIDs[item.LegacyID] || slugs[item.Package.Slug] {
			return errors.New("configuration import contains duplicate or invalid Skills")
		}
		skillIDs[item.LegacyID], slugs[item.Package.Slug] = true, true
		if err := validateSourceInput(item.Source); err != nil {
			return err
		}
		rescanned, err := skills.ScanZIP(item.Package.CanonicalZIP, skills.DefaultLimits)
		if err != nil || rescanned.SHA256 != item.Package.SHA256 || rescanned.ContentHash != item.Package.ContentHash || rescanned.Slug != item.Package.Slug {
			return errors.New("configuration import contains an invalid Skill package")
		}
	}
	secretIDs := map[string]string{}
	for _, item := range input.Secrets {
		if uuid.Validate(item.LegacyID) != nil || secretIDs[item.LegacyID] != "" || (item.Kind != "mcp-env" && item.Kind != "mcp-header") || len(item.Ciphertext) <= 40 || len(item.Ciphertext) > 1<<20 {
			return errors.New("configuration import contains an invalid encrypted record")
		}
		secretIDs[item.LegacyID] = item.Kind
	}
	serverIDs, names, referenced := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, item := range input.MCPServers {
		if uuid.Validate(item.LegacyID) != nil || serverIDs[item.LegacyID] {
			return errors.New("configuration import contains duplicate or invalid MCP definitions")
		}
		serverIDs[item.LegacyID] = true
		normalized, err := NormalizeMCPInput(item.Input)
		if err != nil || normalized.Name != item.Input.Name || names[item.Input.Name] || len(item.Input.Env) != 0 || len(item.Input.Headers) != 0 {
			return errors.New("configuration import contains an invalid MCP definition")
		}
		names[item.Input.Name] = true
		for _, refs := range []struct {
			values map[string]string
			kind   string
		}{{item.EnvRefs, "mcp-env"}, {item.HeaderRefs, "mcp-header"}} {
			for key, id := range refs.values {
				if strings.TrimSpace(key) != key || key == "" || secretIDs[id] != refs.kind || referenced[id] {
					return errors.New("configuration import contains an invalid MCP encrypted-record reference")
				}
				referenced[id] = true
			}
		}
	}
	if len(referenced) != len(secretIDs) {
		return errors.New("configuration import contains unreferenced encrypted records")
	}
	profileNames := map[string]bool{}
	for _, profile := range input.Profiles {
		if profile.Name == "" || profileNames[profile.Name] {
			return errors.New("configuration import contains invalid Profiles")
		}
		profileNames[profile.Name] = true
		for _, id := range profile.LegacySkillIDs {
			if !skillIDs[id] {
				return errors.New("configuration import Profile references an unavailable Skill")
			}
		}
		for _, id := range profile.LegacyMCPServerIDs {
			if !serverIDs[id] {
				return errors.New("configuration import Profile references an unavailable MCP definition")
			}
		}
	}
	for _, required := range []string{"claude-skills", "codex-skills", "shared-mcp"} {
		if !profileNames[required] {
			return errors.New("configuration import generated Profile set is incomplete")
		}
	}
	return nil
}

func validatePristineConfigImportDestination(ctx context.Context, tx pgx.Tx) error {
	checks := []struct {
		name     string
		query    string
		expected int
	}{
		{"generation marker", "SELECT count(*) FROM app_meta WHERE key='schema_generation' AND value='2'", 1},
		{"application metadata", "SELECT count(*) FROM app_meta", 1},
		{"migration ledger", "SELECT count(*) FROM schema_migrations WHERE version=1", 1},
		{"migration ledger entries", "SELECT count(*) FROM schema_migrations", configImportExpectedMigrationCount},
		{"singleton account", "SELECT count(*) FROM account", 1},
		{"sessions", "SELECT count(*) FROM sessions", 0},
		{"settings", "SELECT count(*) FROM settings", 1},
		{"local nodes", "SELECT count(*) FROM nodes WHERE kind='local' AND archived_at IS NULL", 1},
		{"all nodes", "SELECT count(*) FROM nodes", 1},
		{"baseline targets", "SELECT count(*) FROM targets", 4},
		{"invalid baseline targets", "SELECT count(*) FROM targets WHERE target_key NOT IN ('local/claude','local/codex','local/hermes','local/shared-relay')", 0},
		{"market providers", "SELECT count(*) FROM market_providers WHERE kind IN ('skillsmp','xiaping')", 2},
		{"all market providers", "SELECT count(*) FROM market_providers", 2},
		{"bootstrap audit", "SELECT count(*) FROM audit_events WHERE action='bootstrap' AND resource_type='account' AND outcome='success'", 1},
		{"all audit events", "SELECT count(*) FROM audit_events", 1},
	}
	for _, table := range configImportEmptyTables() {
		checks = append(checks, struct {
			name     string
			query    string
			expected int
		}{name: table, query: "SELECT count(*) FROM " + table, expected: 0})
	}
	for _, check := range checks {
		var count int
		if err := tx.QueryRow(ctx, check.query).Scan(&count); err != nil {
			return err
		}
		if count != check.expected {
			return fmt.Errorf("%w: destination baseline %s count=%d expected=%d", ErrConfigImportConflict, check.name, count, check.expected)
		}
	}
	return nil
}

func configImportEmptyTables() []string {
	return []string{
		"encrypted_secrets", "runtime_snapshots", "skill_sources", "skills", "skill_artifacts", "skill_versions",
		"mcp_servers", "mcp_revisions", "profiles", "profile_revisions", "profile_revision_skills", "profile_revision_mcp_servers", "profile_skills", "profile_mcp_servers", "pending_secret_bindings", "bundle_import_confirmations", "bundle_import_fingerprints", "retention_runs", "operations", "operation_targets",
		"desired_snapshots", "target_desired_snapshots", "preflight_confirmations", "local_mcp_import_confirmations",
		"backups", "ai_providers", "alerts",
	}
}

func insertConfigImportSkill(ctx context.Context, tx pgx.Tx, item ConfigImportSkill) (string, error) {
	sourceID, skillID, artifactID, versionID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	metadata, err := json.Marshal(item.Source.Metadata)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO skill_sources(id,kind,name,url,subdirectory,current_commit,metadata)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, sourceID, item.Source.Kind, item.Source.Name, item.Source.URL, item.Source.Subdirectory, item.Source.Commit, jsonText(metadata)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO skills(id,source_id,slug,name,description) VALUES($1,$2,$3,$4,$5)", skillID, sourceID, item.Package.Slug, item.Package.Name, item.Package.Description); err != nil {
		return "", err
	}
	manifest, err := json.Marshal(item.Package.Manifest)
	if err != nil {
		return "", err
	}
	report, err := json.Marshal(item.Package.Report)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO skill_artifacts(id,canonical_sha256,content_hash,archive,size_bytes,manifest,scan_report)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(canonical_sha256) DO NOTHING`, artifactID, item.Package.SHA256, item.Package.ContentHash, item.Package.CanonicalZIP, len(item.Package.CanonicalZIP), jsonText(manifest), jsonText(report)); err != nil {
		return "", err
	}
	var storedHash string
	var storedSize int64
	if err := tx.QueryRow(ctx, "SELECT id::text,content_hash,size_bytes FROM skill_artifacts WHERE canonical_sha256=$1", item.Package.SHA256).Scan(&artifactID, &storedHash, &storedSize); err != nil {
		return "", err
	}
	if storedHash != item.Package.ContentHash || storedSize != int64(len(item.Package.CanonicalZIP)) {
		return "", errors.New("immutable Skill artifact collision")
	}
	provenance, err := json.Marshal(item.Provenance)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO skill_versions(id,skill_id,artifact_id,source_commit,provenance)
		VALUES($1,$2,$3,$4,$5)`, versionID, skillID, artifactID, item.Source.Commit, jsonText(provenance)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "UPDATE skills SET current_version_id=$2 WHERE id=$1", skillID, versionID); err != nil {
		return "", err
	}
	return skillID, nil
}

func remapConfigImportRefs(refs, mapping map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(refs))
	for key, legacyID := range refs {
		id := mapping[legacyID]
		if id == "" {
			return nil, errors.New("missing encrypted-record mapping")
		}
		result[key] = id
	}
	return result, nil
}

func configImportExpectedCounts(input ConfigImportInput) ConfigImportCounts {
	artifacts := map[string]bool{}
	counts := ConfigImportCounts{
		Skills: len(input.Skills), SkillSources: len(input.Skills), SkillVersions: len(input.Skills),
		MCPServers: len(input.MCPServers), EncryptedRecords: len(input.Secrets), Profiles: len(input.Profiles),
		ProfileSkillCounts: map[string]int{}, ProfileMCPCounts: map[string]int{},
	}
	for _, skill := range input.Skills {
		artifacts[skill.Package.SHA256] = true
	}
	counts.SkillArtifacts = len(artifacts)
	for _, profile := range input.Profiles {
		skillCount := len(uniqueSortedStrings(profile.LegacySkillIDs))
		mcpCount := len(uniqueSortedStrings(profile.LegacyMCPServerIDs))
		counts.ProfileSkillCounts[profile.Name] = skillCount
		counts.ProfileMCPCounts[profile.Name] = mcpCount
		counts.ProfileSkills += skillCount
		counts.ProfileMCPServers += mcpCount
	}
	return counts
}

func verifyConfigImportCore(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, marker configImportMarker) error {
	counts := []struct {
		table string
		want  int
	}{
		{"skills", marker.Counts.Skills},
		{"skill_sources", marker.Counts.SkillSources},
		{"skill_artifacts", marker.Counts.SkillArtifacts},
		{"skill_versions", marker.Counts.SkillVersions},
		{"mcp_servers", marker.Counts.MCPServers},
		{"mcp_revisions", marker.Counts.MCPServers},
		{"encrypted_secrets", marker.Counts.EncryptedRecords},
		{"profiles", marker.Counts.Profiles},
		{"profile_revisions", marker.Counts.Profiles},
		{"profile_skills", marker.Counts.ProfileSkills},
		{"profile_revision_skills", marker.Counts.ProfileSkills},
		{"profile_mcp_servers", marker.Counts.ProfileMCPServers},
		{"profile_revision_mcp_servers", marker.Counts.ProfileMCPServers},
	}
	for _, check := range counts {
		var actual int
		if err := query.QueryRow(ctx, "SELECT count(*) FROM "+check.table).Scan(&actual); err != nil {
			return err
		}
		if actual != check.want {
			return fmt.Errorf("%s count=%d expected=%d", check.table, actual, check.want)
		}
	}
	for name, want := range marker.Counts.ProfileSkillCounts {
		var actual int
		if err := query.QueryRow(ctx, `SELECT count(*) FROM profile_skills member JOIN profiles p ON p.id=member.profile_id WHERE p.name=$1`, name).Scan(&actual); err != nil || actual != want {
			return fmt.Errorf("Profile %s Skill membership count=%d expected=%d", name, actual, want)
		}
	}
	for name, want := range marker.Counts.ProfileMCPCounts {
		var actual int
		if err := query.QueryRow(ctx, `SELECT count(*) FROM profile_mcp_servers member JOIN profiles p ON p.id=member.profile_id WHERE p.name=$1`, name).Scan(&actual); err != nil || actual != want {
			return fmt.Errorf("Profile %s MCP membership count=%d expected=%d", name, actual, want)
		}
	}
	var cronValue, timezone string
	if err := query.QueryRow(ctx, "SELECT update_cron,timezone FROM settings WHERE singleton").Scan(&cronValue, &timezone); err != nil {
		return err
	}
	if cronValue != marker.UpdateCron || timezone != marker.Timezone {
		return errors.New("imported settings do not match the import marker")
	}
	var audits int
	if err := query.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='legacy_config_import' AND resource_id=$1 AND outcome='success'", marker.SourceFingerprint).Scan(&audits); err != nil || audits != 1 {
		return errors.New("migration audit event is missing or duplicated")
	}
	return nil
}

func (s *Store) VerifyConfigImportAcceptance(ctx context.Context, result ConfigImportResult) error {
	var markerBody string
	if err := s.pool.QueryRow(ctx, "SELECT value FROM app_meta WHERE key=$1", configImportMarkerKey).Scan(&markerBody); err != nil {
		return err
	}
	var marker configImportMarker
	if err := json.Unmarshal([]byte(markerBody), &marker); err != nil || marker.SourceFingerprint != result.SourceFingerprint {
		return errors.New("configuration import marker does not match")
	}
	if err := verifyConfigImportCore(ctx, s.pool, marker); err != nil {
		return err
	}
	normalReads := []struct {
		read func(context.Context) (json.RawMessage, error)
		want int
	}{
		{s.ListSkills, marker.Counts.Skills},
		{s.ListMCPServers, marker.Counts.MCPServers},
		{func(ctx context.Context) (json.RawMessage, error) { return s.ListProfiles(ctx, false) }, marker.Counts.Profiles},
	}
	for _, check := range normalReads {
		body, err := check.read(ctx)
		if err != nil {
			return err
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(body, &rows); err != nil || len(rows) != check.want {
			return errors.New("normal Store read does not match imported counts")
		}
	}
	settings, err := s.Settings(ctx)
	if err != nil || settings.UpdateCron != marker.UpdateCron || settings.Timezone != marker.Timezone {
		return errors.New("normal settings read does not match imported settings")
	}
	forbiddenTables := []string{"runtime_snapshots", "operations", "operation_targets", "desired_snapshots", "target_desired_snapshots", "backups"}
	for _, table := range forbiddenTables {
		var count int
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("configuration import unexpectedly created %s rows", table)
		}
	}
	return nil
}

func databaseIdentityHash(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (string, error) {
	var database, address string
	var port int
	if err := query.QueryRow(ctx, `SELECT current_database(),coalesce(inet_server_addr()::text,current_setting('unix_socket_directories')),coalesce(inet_server_port(),current_setting('port')::integer)`).Scan(&database, &address, &port); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(database + "\x00" + address + "\x00" + strconv.Itoa(port)))
	return hex.EncodeToString(sum[:]), nil
}

func uniqueSortedStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func failConfigImportPhase(input ConfigImportInput, phase string) error {
	if input.failAfterPhase == phase {
		return fmt.Errorf("injected configuration import failure after %s", phase)
	}
	return nil
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}
