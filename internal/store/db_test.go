package store

import (
	"strings"
	"testing"
)

func TestGenerationTwoMigrationContainsFreshOnlyBoundaries(t *testing.T) {
	body, err := migrationFS.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"schema_generation','2'",
		"CREATE TABLE account",
		"CREATE TABLE desired_snapshots",
		"CREATE FUNCTION validate_desired_manifest",
		"manifest jsonb NOT NULL CHECK (validate_desired_manifest(manifest))",
		"item - ARRAY['memberId','serverId','revision','name','transport','command','args','url','envRefs','headerRefs','contentHash']",
		"desired_snapshots_immutable",
		"skill_artifacts_immutable",
		"skill_versions_immutable",
		"CREATE TABLE operation_targets",
		"operation_targets_one_active_idx",
		"UNIQUE(source_operation_target_id)",
		"CREATE TABLE local_mcp_import_confirmations",
		"local_mcp_import_confirmation_expiry_idx",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("generation-2 migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{"CREATE TABLE users", "CREATE TABLE roles", "CREATE TABLE jobs", "CREATE TABLE node_tasks", "CREATE TABLE deployments", "CREATE TABLE enrollment_tokens"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("generation-2 migration retained legacy table %q", forbidden)
		}
	}
}

func TestRelayProjectionUsesAdditiveMigration(t *testing.T) {
	body, err := migrationFS.ReadFile("migrations/002_relay_projection.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{"ALTER TABLE target_desired_snapshots", "relay_failure_count", "relay_next_retry_at", "relay_suspended", "relay_last_member_check_at", "relay_member_status", "jsonb_typeof(relay_member_status) = 'array'"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("relay projection migration is missing %q", required)
		}
	}
}

func TestTextProcessingCleanupMigrationIsExactAndScoped(t *testing.T) {
	body, err := migrationFS.ReadFile("migrations/017_remove_text_processing_profiles.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"claude-text-processing",
		"codex-text-processing",
		"toolhub_text_processing_profiles",
		"toolhub_text_processing_revisions",
		"ALTER TABLE profile_revision_skills DISABLE TRIGGER USER",
		"ALTER TABLE profile_revision_skills ENABLE TRIGGER USER",
		"archived_at IS NOT NULL",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("text-processing cleanup migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{"TRUNCATE", "DROP TABLE", "DELETE FROM account", "DELETE FROM sessions"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("text-processing cleanup migration contains broad or security-sensitive operation %q", forbidden)
		}
	}
}

func TestSubagentRoutingMigrationRetiresOnlyLegacyRequiredTag(t *testing.T) {
	body, err := migrationFS.ReadFile("migrations/018_subagent_routing_policy.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{"array_remove(tags, 'required')", "same-model-subagents", "archived_at IS NULL"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("subagent routing migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{"DELETE FROM skills", "DROP TABLE", "TRUNCATE"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("subagent routing migration contains destructive operation %q", forbidden)
		}
	}
}

func TestLegacySchemaErrorIsActionable(t *testing.T) {
	err := legacySchemaError()
	if !strings.Contains(err.Error(), "fresh PostgreSQL volume") || !strings.Contains(err.Error(), "generation 2") {
		t.Fatalf("legacy schema error is not actionable: %v", err)
	}
}
