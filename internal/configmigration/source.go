package configmigration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

var requiredLegacyColumns = map[string][]string{
	"schema_migrations":           {"version"},
	"skill_sources":               {"id", "kind", "name", "url", "subdirectory", "credentials_secret_id"},
	"skills":                      {"id", "slug", "name", "description", "source_id", "review_status", "protected", "current_version_id", "archived_at"},
	"skill_artifacts":             {"id", "sha256", "size_bytes", "content", "scan_report"},
	"skill_versions":              {"id", "skill_id", "source_commit", "content_sha256", "artifact_id", "provenance", "manifest", "approved_at"},
	"encrypted_secrets":           {"id", "kind", "ciphertext"},
	"deployments":                 {"skill_id", "runtime_kind", "desired_enabled", "state"},
	"mcp_servers":                 {"id", "name", "runtime_name", "transport", "command", "args", "url", "env_refs", "header_refs", "enabled", "source", "origin", "authority", "credential_mode", "archived_at"},
	"mcp_profiles":                {"id", "name", "enabled", "source", "origin"},
	"mcp_profile_servers":         {"profile_id", "server_id"},
	"toolhub_profiles":            {"id"},
	"toolhub_profile_skills":      {"profile_id", "skill_id"},
	"toolhub_profile_mcp_servers": {"profile_id", "server_id"},
	"toolhub_profile_activations": {"profile_id", "runtime_kind", "state"},
	"update_policies":             {"scope_type", "scope_id", "schedule", "timezone", "enabled"},
	"ai_providers":                {"id"},
}

func ReadSnapshot(ctx context.Context, databaseURL string) (Snapshot, error) {
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return Snapshot{}, migrationError("source_connection_failed", "legacy database connection settings are invalid", err)
	}
	if config.RuntimeParams == nil {
		config.RuntimeParams = map[string]string{}
	}
	config.RuntimeParams["default_transaction_read_only"] = "on"
	config.RuntimeParams["search_path"] = "pg_catalog,public"
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return Snapshot{}, migrationError("source_connection_failed", "legacy database connection failed", err)
	}
	defer conn.Close(context.Background())

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Snapshot{}, migrationError("source_snapshot_failed", "legacy read-only snapshot could not start", err)
	}
	defer tx.Rollback(context.Background())
	var readOnly string
	if err := tx.QueryRow(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil || readOnly != "on" {
		return Snapshot{}, migrationError("source_not_read_only", "legacy snapshot is not read-only", err)
	}

	migrations, err := readMigrationLedger(ctx, tx)
	if err != nil {
		return Snapshot{}, err
	}
	if err := validateMigrationLedger(migrations); err != nil {
		return Snapshot{}, err
	}
	if err := validateLegacyColumns(ctx, tx); err != nil {
		return Snapshot{}, err
	}
	if err := rejectUnsupportedLegacyState(ctx, tx); err != nil {
		return Snapshot{}, err
	}

	var snapshot Snapshot
	snapshot.Migrations = migrations
	if err := tx.QueryRow(ctx, `SELECT current_database(),coalesce(inet_server_addr()::text,current_setting('unix_socket_directories')),coalesce(inet_server_port(),current_setting('port')::integer)`).Scan(&snapshot.Identity.Database, &snapshot.Identity.Address, &snapshot.Identity.Port); err != nil {
		return Snapshot{}, migrationError("source_snapshot_failed", "legacy database identity could not be read", err)
	}
	snapshot.Skills, err = readLegacySkills(ctx, tx)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.MCPServers, err = readLegacyMCPServers(ctx, tx)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Secrets, err = readLegacySecrets(ctx, tx, snapshot.MCPServers)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.SkillDesired, err = readLegacySkillDesired(ctx, tx)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.MCPDesired, err = readLegacyMCPDesired(ctx, tx)
	if err != nil {
		return Snapshot{}, err
	}
	var updateEnabled bool
	if err := tx.QueryRow(ctx, `SELECT schedule,timezone,enabled FROM update_policies WHERE scope_type='global' AND scope_id=''`).Scan(&snapshot.UpdateCron, &snapshot.Timezone, &updateEnabled); err != nil {
		return Snapshot{}, migrationError("source_contract_mismatch", "legacy global update policy is missing or ambiguous", err)
	}
	if !updateEnabled {
		return Snapshot{}, migrationError("unsupported_source_state", "legacy global update policy must be enabled", nil)
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, migrationError("source_snapshot_failed", "legacy read-only snapshot could not finish", err)
	}
	return snapshot, nil
}

func readMigrationLedger(ctx context.Context, tx pgx.Tx) ([]int64, error) {
	rows, err := tx.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, migrationError("source_contract_mismatch", "legacy migration ledger is unavailable", err)
	}
	defer rows.Close()
	var versions []int64
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, migrationError("source_contract_mismatch", "legacy migration ledger is unreadable", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, migrationError("source_contract_mismatch", "legacy migration ledger is unreadable", err)
	}
	return versions, nil
}

func validateMigrationLedger(versions []int64) error {
	if len(versions) != LegacySchemaVersion {
		return migrationError("source_contract_mismatch", fmt.Sprintf("legacy migration ledger has %d entries; expected 11", len(versions)), nil)
	}
	for index, version := range versions {
		if version != int64(index+1) {
			return migrationError("source_contract_mismatch", fmt.Sprintf("legacy migration ledger diverges at entry %d", index+1), nil)
		}
	}
	return nil
}

func validateLegacyColumns(ctx context.Context, tx pgx.Tx) error {
	var appMeta bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass('public.app_meta') IS NOT NULL").Scan(&appMeta); err != nil {
		return migrationError("source_contract_mismatch", "legacy schema marker check failed", err)
	}
	if appMeta {
		return migrationError("source_contract_mismatch", "source database is not a legacy generation-1 database", nil)
	}
	rows, err := tx.Query(ctx, `SELECT table_name,column_name FROM information_schema.columns WHERE table_schema='public'`)
	if err != nil {
		return migrationError("source_contract_mismatch", "legacy schema contract could not be inspected", err)
	}
	defer rows.Close()
	found := map[string]map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return migrationError("source_contract_mismatch", "legacy schema contract could not be inspected", err)
		}
		if _, required := requiredLegacyColumns[table]; required {
			if found[table] == nil {
				found[table] = map[string]bool{}
			}
			found[table][column] = true
		}
	}
	for table, columns := range requiredLegacyColumns {
		for _, column := range columns {
			if !found[table][column] {
				return migrationError("source_contract_mismatch", fmt.Sprintf("legacy schema is missing required field %s.%s", table, column), nil)
			}
		}
	}
	return nil
}

func rejectUnsupportedLegacyState(ctx context.Context, tx pgx.Tx) error {
	checks := []struct {
		query   string
		message string
	}{
		{"SELECT count(*) FROM toolhub_profiles", "legacy user-defined Profiles are not supported by this import"},
		{"SELECT count(*) FROM ai_providers", "legacy AI provider configuration is not supported by this import"},
		{`SELECT count(*) FROM skills s JOIN skill_sources source ON source.id=s.source_id WHERE s.archived_at IS NULL AND s.review_status='approved' AND source.credentials_secret_id IS NOT NULL`, "selected Skill sources cannot contain credentials"},
		{`SELECT count(*) FROM skills WHERE archived_at IS NULL AND review_status='approved' AND protected`, "selected protected Skills cannot be imported"},
	}
	for _, check := range checks {
		var count int
		if err := tx.QueryRow(ctx, check.query).Scan(&count); err != nil {
			return migrationError("source_contract_mismatch", "legacy unsupported-state gate failed", err)
		}
		if count != 0 {
			return migrationError("unsupported_source_state", fmt.Sprintf("%s: count=%d", check.message, count), nil)
		}
	}
	return nil
}

func readLegacySkills(ctx context.Context, tx pgx.Tx) ([]LegacySkill, error) {
	rows, err := tx.Query(ctx, `SELECT s.id::text,s.slug,s.name,s.description,source.id::text,source.kind,source.name,
		coalesce(source.url,''),source.subdirectory,v.id::text,v.source_commit,a.id::text,a.sha256,v.content_sha256,a.size_bytes,a.content
		FROM skills s
		JOIN skill_sources source ON source.id=s.source_id
		JOIN skill_versions v ON v.id=s.current_version_id AND v.skill_id=s.id
		JOIN skill_artifacts a ON a.id=v.artifact_id
		WHERE s.archived_at IS NULL AND s.review_status='approved' AND v.approved_at IS NOT NULL
		ORDER BY s.id`)
	if err != nil {
		return nil, migrationError("source_snapshot_failed", "selected legacy Skills could not be read", err)
	}
	defer rows.Close()
	var result []LegacySkill
	for rows.Next() {
		var skill LegacySkill
		if err := rows.Scan(&skill.ID, &skill.Slug, &skill.Name, &skill.Description, &skill.SourceID, &skill.SourceKind, &skill.SourceName, &skill.SourceURL, &skill.Subdirectory, &skill.VersionID, &skill.SourceCommit, &skill.ArtifactID, &skill.ArtifactSHA256, &skill.VersionSHA256, &skill.SizeBytes, &skill.Archive); err != nil {
			return nil, migrationError("source_snapshot_failed", "a selected legacy Skill could not be read", err)
		}
		result = append(result, skill)
	}
	if err := rows.Err(); err != nil {
		return nil, migrationError("source_snapshot_failed", "selected legacy Skills could not be read", err)
	}
	var expected int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM skills WHERE archived_at IS NULL AND review_status='approved'`).Scan(&expected); err != nil {
		return nil, migrationError("source_snapshot_failed", "selected legacy Skill count could not be read", err)
	}
	if expected != len(result) {
		return nil, migrationError("source_contract_mismatch", fmt.Sprintf("selected legacy Skill graph is incomplete: selected=%d resolved=%d", expected, len(result)), nil)
	}
	return result, nil
}

func readLegacyMCPServers(ctx context.Context, tx pgx.Tx) ([]LegacyMCPServer, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,coalesce(nullif(runtime_name,''),name),transport,command,args,url,env_refs,header_refs,source,credential_mode
		FROM mcp_servers WHERE enabled AND authority='toolhub' AND archived_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, migrationError("source_snapshot_failed", "active legacy MCP definitions could not be read", err)
	}
	defer rows.Close()
	var result []LegacyMCPServer
	for rows.Next() {
		var server LegacyMCPServer
		var argsJSON, envJSON, headerJSON []byte
		var credentialMode string
		if err := rows.Scan(&server.ID, &server.Name, &server.Transport, &server.Command, &argsJSON, &server.URL, &envJSON, &headerJSON, &server.Source, &credentialMode); err != nil {
			return nil, migrationError("source_snapshot_failed", "an active legacy MCP definition could not be read", err)
		}
		if credentialMode != "toolhub-secret" {
			return nil, migrationError("unsupported_source_state", fmt.Sprintf("legacy MCP definition %s uses an unsupported credential mode", server.ID), nil)
		}
		if err := decodeStringArray(argsJSON, &server.Args); err != nil {
			return nil, migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP definition %s has invalid arguments", server.ID), err)
		}
		if err := decodeStringMap(envJSON, &server.EnvRefs); err != nil {
			return nil, migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP definition %s has invalid environment references", server.ID), err)
		}
		if err := decodeStringMap(headerJSON, &server.HeaderRefs); err != nil {
			return nil, migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP definition %s has invalid header references", server.ID), err)
		}
		result = append(result, server)
	}
	if err := rows.Err(); err != nil {
		return nil, migrationError("source_snapshot_failed", "active legacy MCP definitions could not be read", err)
	}
	return result, nil
}

func readLegacySecrets(ctx context.Context, tx pgx.Tx, servers []LegacyMCPServer) ([]LegacySecret, error) {
	referenced := map[string]bool{}
	for _, server := range servers {
		for _, refs := range []map[string]string{server.EnvRefs, server.HeaderRefs} {
			for _, id := range refs {
				referenced[id] = true
			}
		}
	}
	ids := make([]string, 0, len(referenced))
	for id := range referenced {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return []LegacySecret{}, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text,kind,ciphertext FROM encrypted_secrets WHERE id::text=ANY($1::text[]) ORDER BY id`, ids)
	if err != nil {
		return nil, migrationError("source_snapshot_failed", "legacy MCP Secret records could not be read", err)
	}
	defer rows.Close()
	var result []LegacySecret
	for rows.Next() {
		var secret LegacySecret
		if err := rows.Scan(&secret.ID, &secret.Kind, &secret.Ciphertext); err != nil {
			return nil, migrationError("source_snapshot_failed", "a legacy MCP Secret record could not be read", err)
		}
		result = append(result, secret)
	}
	if err := rows.Err(); err != nil {
		return nil, migrationError("source_snapshot_failed", "legacy MCP Secret records could not be read", err)
	}
	return result, nil
}

func readLegacySkillDesired(ctx context.Context, tx pgx.Tx) (map[string][]string, error) {
	result := map[string][]string{"claude": {}, "codex": {}}
	rows, err := tx.Query(ctx, `SELECT DISTINCT runtime_kind,skill_id::text FROM deployments
		WHERE runtime_kind IN ('claude','codex') AND desired_enabled AND state NOT IN ('archived','legacy_read_only')
		ORDER BY runtime_kind,skill_id::text`)
	if err != nil {
		return nil, migrationError("source_snapshot_failed", "legacy desired Skill selection could not be read", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runtime, id string
		if err := rows.Scan(&runtime, &id); err != nil {
			return nil, migrationError("source_snapshot_failed", "legacy desired Skill selection could not be read", err)
		}
		result[runtime] = append(result[runtime], id)
	}
	return result, rows.Err()
}

func readLegacyMCPDesired(ctx context.Context, tx pgx.Tx) (map[string][]string, error) {
	result := map[string][]string{"claude": {}, "codex": {}}
	rows, err := tx.Query(ctx, `SELECT p.origin->>'managedRuntime',member.server_id::text
		FROM mcp_profiles p
		JOIN mcp_profile_servers member ON member.profile_id=p.id
		JOIN mcp_servers server ON server.id=member.server_id
		WHERE p.enabled AND p.source='toolhub'
		  AND p.origin->>'managedRuntime' IN ('claude','codex')
		  AND p.name='toolhub-'||(p.origin->>'managedRuntime')
		  AND server.enabled AND server.authority='toolhub' AND server.archived_at IS NULL
		ORDER BY 1,2`)
	if err != nil {
		return nil, migrationError("source_snapshot_failed", "legacy desired MCP selection could not be read", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var runtime, id string
		if err := rows.Scan(&runtime, &id); err != nil {
			return nil, migrationError("source_snapshot_failed", "legacy desired MCP selection could not be read", err)
		}
		key := runtime + "\x00" + id
		if !seen[key] {
			seen[key] = true
			result[runtime] = append(result[runtime], id)
		}
	}
	for runtime := range result {
		sort.Strings(result[runtime])
	}
	return result, rows.Err()
}

func decodeStringArray(body []byte, target *[]string) error {
	if len(body) == 0 || strings.TrimSpace(string(body)) == "null" {
		*target = []string{}
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	if *target == nil {
		*target = []string{}
	}
	return nil
}

func decodeStringMap(body []byte, target *map[string]string) error {
	if len(body) == 0 {
		return errors.New("missing JSON object")
	}
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}
	if _, ok := raw.(map[string]any); !ok {
		return errors.New("expected JSON object")
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	if *target == nil {
		*target = map[string]string{}
	}
	return nil
}
