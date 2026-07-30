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

func TestLegacySchemaErrorIsActionable(t *testing.T) {
	err := legacySchemaError()
	if !strings.Contains(err.Error(), "fresh PostgreSQL volume") || !strings.Contains(err.Error(), "generation 2") {
		t.Fatalf("legacy schema error is not actionable: %v", err)
	}
}
