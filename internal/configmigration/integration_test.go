package configmigration

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/store"
)

//go:embed testdata/legacy_v11_minimal.sql
var legacyV11Fixture string

func TestLegacySourceRejectsMissingRequiredColumnIntegration(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	sourceURL := newDisposableMigrationDatabase(t, baseURL, "missing_column")
	source, err := pgx.Connect(ctx, sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	if _, err := source.Exec(ctx, legacyV11Fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(ctx, "ALTER TABLE mcp_servers DROP COLUMN header_refs"); err != nil {
		t.Fatal(err)
	}
	_, err = ReadSnapshot(ctx, sourceURL)
	if err == nil {
		t.Fatal("legacy source with a missing required column was accepted")
	}
	code, message := PublicError(err)
	if code != "source_contract_mismatch" || !strings.Contains(message, "mcp_servers.header_refs") {
		t.Fatalf("unexpected contract error: code=%s message=%q", code, message)
	}
}

func TestLegacyToGenerationTwoImportIntegration(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	sourceURL := newDisposableMigrationDatabase(t, baseURL, "legacy")
	destinationURL := newDisposableMigrationDatabase(t, baseURL, "destination")
	source, err := pgx.Connect(ctx, sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	if _, err := source.Exec(ctx, legacyV11Fixture); err != nil {
		t.Fatal(err)
	}

	legacyKey := bytes.Repeat([]byte{4}, 32)
	legacyCipher, err := security.NewCipher(legacyKey)
	if err != nil {
		t.Fatal(err)
	}
	fixture := insertLegacyIntegrationConfiguration(t, source, legacyCipher)
	var sourceCiphertextBefore []byte
	if err := source.QueryRow(ctx, "SELECT ciphertext FROM encrypted_secrets WHERE id=$1", fixture.secretID).Scan(&sourceCiphertextBefore); err != nil {
		t.Fatal(err)
	}

	dryOptions := Options{LegacyDatabaseURL: sourceURL, LegacyMasterKey: append([]byte(nil), legacyKey...)}
	dryReport, err := Execute(ctx, dryOptions)
	if err != nil {
		t.Fatal(err)
	}
	if dryReport.Status != "validated" || dryReport.Skills != 1 || dryReport.MCPServers != 2 || dryReport.MCPSecrets != 2 {
		t.Fatalf("unexpected dry-run report: %+v", dryReport)
	}
	destinationBefore, err := pgx.Connect(ctx, destinationURL)
	if err != nil {
		t.Fatal(err)
	}
	var destinationTables int
	if err := destinationBefore.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname='public'").Scan(&destinationTables); err != nil {
		t.Fatal(err)
	}
	_ = destinationBefore.Close(ctx)
	if destinationTables != 0 {
		t.Fatalf("dry-run changed destination schema: tables=%d", destinationTables)
	}

	destinationKey := bytes.Repeat([]byte{7}, 32)
	legacyDestination := dryOptions
	legacyDestination.Apply = true
	legacyDestination.ExpectedSourceFingerprint = dryReport.SourceFingerprint
	legacyDestination.Destination = DestinationOptions{
		DatabaseURL: sourceURL, MasterKey: append([]byte(nil), destinationKey...),
		BootstrapUsername: "admin", BootstrapPassword: "correct horse battery staple",
		LocalNodeName: "migration-integration", ManagedUsername: "root", Timezone: "UTC", RelayPort: 6276,
	}
	if _, err := Execute(ctx, legacyDestination); err == nil {
		t.Fatal("legacy database was accepted as a generation-2 destination")
	} else if code, _ := PublicError(err); code != "destination_initialize_failed" {
		t.Fatalf("legacy destination returned code %q", code)
	}
	var ledgerRows int
	if err := source.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&ledgerRows); err != nil || ledgerRows != 11 {
		t.Fatalf("legacy destination attempt changed migration ledger: rows=%d err=%v", ledgerRows, err)
	}

	applyOptions := dryOptions
	applyOptions.Apply = true
	applyOptions.ExpectedSourceFingerprint = dryReport.SourceFingerprint
	applyOptions.Destination = DestinationOptions{
		DatabaseURL: destinationURL, MasterKey: append([]byte(nil), destinationKey...),
		BootstrapUsername: "admin", BootstrapPassword: "correct horse battery staple",
		LocalNodeName: "migration-integration", ManagedUsername: "root", Timezone: "UTC", RelayPort: 6276,
	}
	applied, err := Execute(ctx, applyOptions)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != "imported" || applied.AlreadyImported {
		t.Fatalf("unexpected apply report: %+v", applied)
	}

	destinationCipher, err := security.NewCipher(destinationKey)
	if err != nil {
		t.Fatal(err)
	}
	destinationStore, err := store.Open(ctx, destinationURL, destinationCipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(destinationStore.Close)
	for table, want := range map[string]int{
		"skills": 1, "mcp_servers": 2, "encrypted_secrets": 2, "profiles": 3,
		"operations": 0, "desired_snapshots": 0, "backups": 0, "runtime_snapshots": 0,
	} {
		var count int
		if err := destinationStore.Pool().QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	rows, err := destinationStore.Pool().Query(ctx, "SELECT id::text,ciphertext FROM encrypted_secrets ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	decrypted := map[string]bool{}
	for rows.Next() {
		var id string
		var ciphertext []byte
		if err := rows.Scan(&id, &ciphertext); err != nil {
			t.Fatal(err)
		}
		plaintext, err := destinationCipher.Decrypt(ciphertext, id)
		if err != nil {
			t.Fatal(err)
		}
		decrypted[string(plaintext)] = true
		clear(plaintext)
		if _, err := legacyCipher.Decrypt(ciphertext, id); err == nil {
			t.Fatal("destination ciphertext decrypted under the legacy key")
		}
	}
	if !decrypted["integration-env-value"] || !decrypted["integration-header-value"] {
		t.Fatalf("destination encrypted-record values did not round trip; resolved=%d", len(decrypted))
	}

	var sourceCiphertextAfter []byte
	if err := source.QueryRow(ctx, "SELECT ciphertext FROM encrypted_secrets WHERE id=$1", fixture.secretID).Scan(&sourceCiphertextAfter); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sourceCiphertextBefore, sourceCiphertextAfter) {
		t.Fatal("source ciphertext changed during dry-run or apply")
	}
	var sourceSkills, sourceMCP int
	if err := source.QueryRow(ctx, "SELECT (SELECT count(*) FROM skills),(SELECT count(*) FROM mcp_servers)").Scan(&sourceSkills, &sourceMCP); err != nil {
		t.Fatal(err)
	}
	if sourceSkills != 1 || sourceMCP != 3 {
		t.Fatalf("source rows changed: Skills=%d MCP=%d", sourceSkills, sourceMCP)
	}

	var targetStateBefore string
	if err := destinationStore.Pool().QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('id',id,'updatedAt',updated_at) ORDER BY id)::text,'[]') FROM targets`).Scan(&targetStateBefore); err != nil {
		t.Fatal(err)
	}
	rerun, err := Execute(ctx, applyOptions)
	if err != nil || !rerun.AlreadyImported || rerun.Status != "already imported" {
		t.Fatalf("idempotent rerun: report=%+v err=%v", rerun, err)
	}
	var targetStateAfter string
	if err := destinationStore.Pool().QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('id',id,'updatedAt',updated_at) ORDER BY id)::text,'[]') FROM targets`).Scan(&targetStateAfter); err != nil {
		t.Fatal(err)
	}
	if targetStateAfter != targetStateBefore {
		t.Fatal("same-fingerprint rerun changed baseline target state")
	}
	var audits int
	if err := destinationStore.Pool().QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='legacy_config_import'").Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("migration audits=%d err=%v", audits, err)
	}
}

type legacyIntegrationFixture struct {
	secretID string
}

func insertLegacyIntegrationConfiguration(t *testing.T, conn *pgx.Conn, cipher *security.Cipher) legacyIntegrationFixture {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	skill := legacySkillFixture(t, "integration-skill", "git", "https://example.test/integration.git")
	if _, err := tx.Exec(ctx, `INSERT INTO skill_sources(id,kind,name,url,subdirectory) VALUES($1,'git','integration-source',$2,'')`, skill.SourceID, skill.SourceURL); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO skills(id,slug,name,description,source_id,review_status,protected,current_version_id)
		VALUES($1,$2,$3,$4,$5,'approved',false,$6)`, skill.ID, skill.Slug, skill.Name, skill.Description, skill.SourceID, skill.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO skill_artifacts(id,sha256,size_bytes,content,scan_report) VALUES($1,$2,$3,$4,'{}')`, skill.ArtifactID, skill.ArtifactSHA256, skill.SizeBytes, skill.Archive); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO skill_versions(id,skill_id,source_commit,content_sha256,artifact_id,provenance,manifest,approved_at)
		VALUES($1,$2,'0123456789abcdef',$3,$4,'{}','{}',now())`, skill.VersionID, skill.ID, skill.ArtifactSHA256, skill.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deployments(skill_id,runtime_kind,desired_enabled,state)
		VALUES($1,'claude',true,'in_sync'),($1,'codex',true,'in_sync')`, skill.ID); err != nil {
		t.Fatal(err)
	}

	envID, headerID, inactiveID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	envCiphertext, err := cipher.Encrypt([]byte("integration-env-value"), envID)
	if err != nil {
		t.Fatal(err)
	}
	headerCiphertext, err := cipher.Encrypt([]byte("integration-header-value"), headerID)
	if err != nil {
		t.Fatal(err)
	}
	inactiveCiphertext, err := cipher.Encrypt([]byte("integration-inactive-value"), inactiveID)
	if err != nil {
		t.Fatal(err)
	}
	desiredServerID, otherServerID, inactiveServerID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	claudeProfileID, codexProfileID := uuid.NewString(), uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO encrypted_secrets(id,kind,ciphertext) VALUES($1,'mcp-env',$2),($3,'mcp-header',$4),($5,'mcp-env',$6)`, envID, envCiphertext, headerID, headerCiphertext, inactiveID, inactiveCiphertext); err != nil {
		t.Fatal(err)
	}
	envRefs, _ := json.Marshal(map[string]string{"TOKEN": envID})
	headerRefs, _ := json.Marshal(map[string]string{"Authorization": headerID})
	inactiveRefs, _ := json.Marshal(map[string]string{"TOKEN": inactiveID})
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_servers(id,name,runtime_name,transport,command,args,url,env_refs,header_refs,enabled,source,origin,authority,credential_mode)
		VALUES($1,'desired-acemcp','ACEMCP','stdio','/usr/bin/acemcp','["serve"]','',$2,'{}',true,'toolhub','{}','toolhub','toolhub-secret')`, desiredServerID, string(envRefs)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_servers(id,name,runtime_name,transport,command,args,url,env_refs,header_refs,enabled,source,origin,authority,credential_mode)
		VALUES($1,'other-acemcp','acemcp','streamable-http','','[]','https://example.test/mcp','{}',$2,true,'toolhub','{}','toolhub','toolhub-secret')`, otherServerID, string(headerRefs)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_servers(id,name,runtime_name,transport,command,args,url,env_refs,header_refs,enabled,source,origin,authority,credential_mode)
		VALUES($1,'inactive-server','inactive-server','stdio','/usr/bin/inactive','[]','',$2,'{}',false,'toolhub','{}','toolhub','toolhub-secret')`, inactiveServerID, string(inactiveRefs)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_profiles(id,name,enabled,source,origin)
		VALUES($1,'toolhub-claude',true,'toolhub','{"managedRuntime":"claude"}'),
		      ($2,'toolhub-codex',true,'toolhub','{"managedRuntime":"codex"}')`, claudeProfileID, codexProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_profile_servers(profile_id,server_id) VALUES($1,$2),($3,$2)`, claudeProfileID, desiredServerID, codexProfileID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return legacyIntegrationFixture{secretID: envID}
}

func newDisposableMigrationDatabase(t *testing.T, baseURL, prefix string) string {
	t.Helper()
	ctx := context.Background()
	config, err := pgx.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig := config.Copy()
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	name := "toolhub_" + prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	parsedURL, err := url.Parse(baseURL)
	if err == nil && (parsedURL.Scheme == "postgres" || parsedURL.Scheme == "postgresql") {
		parsedURL.Path = "/" + name
		return parsedURL.String()
	}
	return baseURL + " dbname=" + name
}
