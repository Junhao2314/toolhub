package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/security"
)

func TestRollbackFirstDeploymentDisablesAndCanBeReenabledIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "toolhub_discovery_test") {
		t.Fatal("integration test database URL must target toolhub_discovery_test")
	}
	ctx := context.Background()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(ctx, databaseURL, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = st.BootstrapAdmin(ctx, "admin", "admin@example.test", "Admin", "ToolHub-Test-Password-2026")
	var adminID string
	if err := st.pool.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN user_roles ur ON ur.user_id=u.id JOIN roles r ON r.id=ur.role_id WHERE r.name='admin' ORDER BY u.created_at LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	suffix := strings.SplitN(uuid.NewString(), "-", 2)[0]
	nodeID, _ := enrollTestNode(t, st, adminID, "first-rollback-node-"+suffix)
	pkg := testDiscoveredSkillPackage(t)
	pkg.Slug += "-" + suffix
	pkg.Name += " " + suffix
	imported, err := st.ImportSkill(ctx, SourceInput{Kind: "upload", Name: "first-rollback.zip"}, pkg, map[string]any{}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReviewSkill(ctx, imported.SkillID, "approved", adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSkillTargets(ctx, imported.SkillID, adminID, []DeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}, false); err != nil {
		t.Fatal(err)
	}
	var deploymentID string
	var generation int64
	if err := st.pool.QueryRow(ctx, `UPDATE deployments SET actual_version_id=desired_version_id,actual_enabled=true,actual_generation=desired_generation,state='in_sync'
		WHERE node_id=$1 AND skill_id=$2 AND runtime_kind='codex' RETURNING id::text,desired_generation`, nodeID, imported.SkillID).Scan(&deploymentID, &generation); err != nil {
		t.Fatal(err)
	}
	job, err := st.RollbackDeployment(ctx, deploymentID, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != "rollback" {
		t.Fatalf("rollback job kind=%q", job.Kind)
	}
	var desiredVersion, previousVersion, state string
	var desiredEnabled bool
	var rolledBackGeneration int64
	if err := st.pool.QueryRow(ctx, `SELECT desired_version_id::text,coalesce(previous_version_id::text,''),desired_enabled,state,desired_generation
		FROM deployments WHERE id=$1`, deploymentID).Scan(&desiredVersion, &previousVersion, &desiredEnabled, &state, &rolledBackGeneration); err != nil {
		t.Fatal(err)
	}
	if desiredVersion != imported.VersionID || previousVersion != "" || desiredEnabled || state != "rolling_back" || rolledBackGeneration != generation+1 {
		t.Fatalf("first rollback version=%q previous=%q enabled=%v state=%q generation=%d", desiredVersion, previousVersion, desiredEnabled, state, rolledBackGeneration)
	}
	pending, err := st.PendingSkillDeployments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rollbackTask *SkillDeploymentTask
	for index := range pending {
		if pending[index].DeploymentID == deploymentID {
			rollbackTask = &pending[index]
			break
		}
	}
	if rollbackTask == nil || rollbackTask.Enabled || rollbackTask.VersionID != imported.VersionID || rollbackTask.DesiredGeneration != rolledBackGeneration {
		t.Fatalf("first rollback task=%+v", rollbackTask)
	}
	if _, err := st.SetSkillTargets(ctx, imported.SkillID, adminID, []DeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}, false); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT desired_enabled,state,desired_generation FROM deployments WHERE id=$1`, deploymentID).
		Scan(&desiredEnabled, &state, &generation); err != nil {
		t.Fatal(err)
	}
	if !desiredEnabled || state != "pending" || generation != rolledBackGeneration+1 {
		t.Fatalf("reenable enabled=%v state=%q generation=%d", desiredEnabled, state, generation)
	}
}

func TestRollbackRejectsNeverAppliedFirstDeploymentIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "toolhub_discovery_test") {
		t.Fatal("integration test database URL must target toolhub_discovery_test")
	}
	ctx := context.Background()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(ctx, databaseURL, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = st.BootstrapAdmin(ctx, "admin", "admin@example.test", "Admin", "ToolHub-Test-Password-2026")
	var adminID string
	if err := st.pool.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN user_roles ur ON ur.user_id=u.id JOIN roles r ON r.id=ur.role_id WHERE r.name='admin' ORDER BY u.created_at LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	suffix := strings.SplitN(uuid.NewString(), "-", 2)[0]
	nodeID, _ := enrollTestNode(t, st, adminID, "pending-rollback-node-"+suffix)
	pkg := testDiscoveredSkillPackage(t)
	pkg.Slug += "-" + suffix
	pkg.Name += " " + suffix
	imported, err := st.ImportSkill(ctx, SourceInput{Kind: "upload", Name: "pending-rollback.zip"}, pkg, map[string]any{}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReviewSkill(ctx, imported.SkillID, "approved", adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSkillTargets(ctx, imported.SkillID, adminID, []DeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}, false); err != nil {
		t.Fatal(err)
	}
	var deploymentID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM deployments WHERE node_id=$1 AND skill_id=$2 AND runtime_kind='codex'`, nodeID, imported.SkillID).Scan(&deploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RollbackDeployment(ctx, deploymentID, adminID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pending first rollback error=%v", err)
	}
}
