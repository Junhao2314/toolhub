package store

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/skills"
)

func TestConfigImportRollsBackEveryPhaseIntegration(t *testing.T) {
	for _, phase := range []string{"skills", "secrets", "mcp", "profiles", "settings", "audit", "marker"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			st := newIntegrationStore(t, true)
			bootstrapConfigImportDestination(t, st)
			input, legacyCipher := configImportFixture(t)
			input.failAfterPhase = phase
			if _, err := st.ImportLegacyConfig(ctx, input, legacyCipher); err == nil {
				t.Fatalf("injected failure after %s was not returned", phase)
			}
			for _, table := range []string{"skill_sources", "skills", "skill_artifacts", "skill_versions", "encrypted_secrets", "mcp_servers", "profiles", "profile_skills", "profile_mcp_servers"} {
				var count int
				if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s rows after rollback=%d err=%v", table, count, err)
				}
			}
			var markerCount, auditCount int
			var schedule string
			if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM app_meta WHERE key=$1", configImportMarkerKey).Scan(&markerCount); err != nil {
				t.Fatal(err)
			}
			if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM audit_events").Scan(&auditCount); err != nil {
				t.Fatal(err)
			}
			if err := st.pool.QueryRow(ctx, "SELECT update_cron FROM settings WHERE singleton").Scan(&schedule); err != nil {
				t.Fatal(err)
			}
			if markerCount != 0 || auditCount != 1 || schedule != "0 2 * * *" {
				t.Fatalf("rollback marker=%d audit=%d schedule=%q", markerCount, auditCount, schedule)
			}
		})
	}
}

func TestConfigImportReencryptsVerifiesAndIsIdempotentIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	bootstrapConfigImportDestination(t, st)
	input, legacyCipher := configImportFixture(t)
	result, err := st.ImportLegacyConfig(ctx, input, legacyCipher)
	if err != nil {
		t.Fatal(err)
	}
	if result.AlreadyImported || result.Counts.Skills != 1 || result.Counts.MCPServers != 1 || result.Counts.EncryptedRecords != 1 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	if err := st.VerifyConfigImportAcceptance(ctx, result); err != nil {
		t.Fatal(err)
	}

	var destinationID, contentHash string
	var ciphertext []byte
	var envRefs map[string]string
	if err := st.pool.QueryRow(ctx, "SELECT id::text,ciphertext FROM encrypted_secrets").Scan(&destinationID, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if destinationID == input.Secrets[0].LegacyID {
		t.Fatal("legacy Secret UUID was reused")
	}
	plaintext, err := st.cipher.Decrypt(ciphertext, destinationID)
	if err != nil || string(plaintext) != "store-migration-fixture-value" {
		t.Fatalf("destination Secret did not decrypt with destination key: length=%d err=%v", len(plaintext), err)
	}
	clear(plaintext)
	if _, err := legacyCipher.Decrypt(ciphertext, destinationID); err == nil {
		t.Fatal("destination ciphertext decrypted with the legacy key")
	}
	if err := st.pool.QueryRow(ctx, "SELECT env_refs,content_hash FROM mcp_servers").Scan(&envRefs, &contentHash); err != nil {
		t.Fatal(err)
	}
	if contentHash != MCPContentHash(input.MCPServers[0].Input, envRefs, map[string]string{}) {
		t.Fatal("migrated MCP content hash differs from the normal Store helper")
	}

	second, err := st.ImportLegacyConfig(ctx, input, legacyCipher)
	if err != nil || !second.AlreadyImported {
		t.Fatalf("same-fingerprint rerun: result=%+v err=%v", second, err)
	}
	var audits int
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='legacy_config_import'").Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("migration audit count=%d err=%v", audits, err)
	}
	different := input
	different.SourceFingerprint = strings.Repeat("c", 64)
	if _, err := st.ImportLegacyConfig(ctx, different, legacyCipher); !errors.Is(err, ErrConfigImportConflict) {
		t.Fatalf("different source fingerprint returned %v", err)
	}
}

func TestConfigImportRejectsNonPristineDestinationIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	bootstrapConfigImportDestination(t, st)
	if _, err := st.pool.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata)
		VALUES($1,'preexisting_state','configuration','extra','success','{}')`, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	input, legacyCipher := configImportFixture(t)
	if _, err := st.ImportLegacyConfig(ctx, input, legacyCipher); !errors.Is(err, ErrConfigImportConflict) {
		t.Fatalf("non-pristine destination returned %v", err)
	}
	var markerCount, skillCount int
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM app_meta WHERE key=$1", configImportMarkerKey).Scan(&markerCount); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM skills").Scan(&skillCount); err != nil {
		t.Fatal(err)
	}
	if markerCount != 0 || skillCount != 0 {
		t.Fatalf("non-pristine rejection wrote marker=%d Skills=%d", markerCount, skillCount)
	}
}

func bootstrapConfigImportDestination(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.BootstrapAccount(ctx, "admin", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if err := st.BootstrapEnvironment(ctx, "migration-test", "root", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
}

func configImportFixture(t *testing.T) (ConfigImportInput, *security.Cipher) {
	t.Helper()
	pkg := configImportSkillPackage(t)
	legacyCipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	legacySecretID := uuid.NewString()
	ciphertext, err := legacyCipher.Encrypt([]byte("store-migration-fixture-value"), legacySecretID)
	if err != nil {
		t.Fatal(err)
	}
	legacySkillID, legacyMCPID := uuid.NewString(), uuid.NewString()
	return ConfigImportInput{
		SourceFingerprint:      strings.Repeat("b", 64),
		SourceDatabaseIdentity: strings.Repeat("a", 64),
		Skills: []ConfigImportSkill{{
			LegacyID:   legacySkillID,
			Source:     SourceInput{Kind: "zip", Name: "fixture", Metadata: map[string]any{"migration": "test"}},
			Package:    pkg,
			Provenance: map[string]any{"migration": "test"},
		}},
		Secrets: []ConfigImportSecret{{LegacyID: legacySecretID, Kind: "mcp-env", Ciphertext: ciphertext}},
		MCPServers: []ConfigImportMCP{{
			LegacyID: legacyMCPID,
			Input:    MCPInput{Name: "fixture-mcp", Description: "fixture", Transport: "stdio", Command: "/usr/bin/fixture", Args: []string{}},
			EnvRefs:  map[string]string{"TOKEN": legacySecretID}, HeaderRefs: map[string]string{},
		}},
		Profiles: []ConfigImportProfile{
			{Name: "claude-skills", LegacySkillIDs: []string{legacySkillID}},
			{Name: "codex-skills", LegacySkillIDs: []string{legacySkillID}},
			{Name: "shared-mcp", LegacyMCPServerIDs: []string{legacyMCPID}},
		},
		UpdateCron: "15 4 * * *", Timezone: "UTC",
	}, legacyCipher
}

func configImportSkillPackage(t *testing.T) skills.Package {
	t.Helper()
	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	header := &zip.FileHeader{Name: "SKILL.md", Method: zip.Deflate}
	header.SetMode(0o644)
	header.Modified = time.Unix(0, 0).UTC()
	file, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("---\nname: fixture-skill\ndescription: Store import fixture\n---\nFixture.\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	pkg, err := skills.ScanZIP(raw.Bytes(), skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}
