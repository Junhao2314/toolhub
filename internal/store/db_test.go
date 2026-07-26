package store

import (
	"strings"
	"testing"
)

func TestMigrationExecutionSQLDisambiguatesMigration006JSONOperators(t *testing.T) {
	const source = "SELECT kind || ':' || payload->>'deploymentId' || ':' || payload->>'desiredGeneration' FROM node_tasks"
	corrected := migrationExecutionSQL("006_orchestration_leases_generations.sql", []byte(source))
	if !strings.Contains(corrected, "(payload->>'deploymentId')") || !strings.Contains(corrected, "(payload->>'desiredGeneration')") {
		t.Fatalf("migration 006 compatibility SQL was not corrected: %s", corrected)
	}
	if untouched := migrationExecutionSQL("007_other.sql", []byte(source)); untouched != source {
		t.Fatalf("unrelated migration SQL changed: %s", untouched)
	}
}
