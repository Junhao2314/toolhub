package worker

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Junhao2314/toolhub/internal/bridgeclient"
	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/market"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/store"
)

func TestFailedNodeRefreshPreservesActiveTargetsIntegration(t *testing.T) {
	ctx := context.Background()
	st := newWorkerIntegrationStore(t)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	node := bridgeprotocol.NodeInfo{
		NodeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("salt:refresh-failure")).String(),
		Name:   "refresh-failure", Kind: bridgeprotocol.NodeKindSalt,
		SaltMinionID: "refresh-failure", Status: "online", Version: "3008.0",
	}
	if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{node}); err != nil {
		t.Fatal(err)
	}
	var targetID string
	if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='salt:refresh-failure/claude'`).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	bridge, err := bridgeclient.New(t.TempDir()+"/missing-bridge.sock", []byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	w := New(st, bridge, market.NewMulti(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, apiErr := w.executeControl(ctx, domain.Operation{ID: uuid.NewString(), Kind: "refresh"})
	if apiErr == nil || apiErr.Code != bridgeprotocol.ErrTargetUnavailable {
		t.Fatalf("refresh error=%+v want %s", apiErr, bridgeprotocol.ErrTargetUnavailable)
	}
	if _, err := st.Target(ctx, targetID); err != nil {
		t.Fatalf("active Target disappeared after failed refresh: %v", err)
	}
	var status string
	var archived bool
	if err := st.Pool().QueryRow(ctx, `SELECT status,archived_at IS NOT NULL FROM nodes WHERE id=$1`, node.NodeID).Scan(&status, &archived); err != nil {
		t.Fatal(err)
	}
	if status != "online" || archived {
		t.Fatalf("failed refresh changed node status=%q archived=%v", status, archived)
	}
}

func TestProfileApplyFinalizationFailureIsPersistedIntegration(t *testing.T) {
	ctx := context.Background()
	st := newWorkerIntegrationStore(t)
	if err := st.BootstrapEnvironment(ctx, "finalization-worker-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	var targetID string
	if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/shared-relay'`).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	operationID, operationTargetID := uuid.NewString(), uuid.NewString()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,'apply','running','{}')`, operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,governance_finalization_pending,finished_at) VALUES($1,$2,$3,'succeeded',true,now())`, operationTargetID, operationID, targetID); err != nil {
		t.Fatal(err)
	}
	w := &Worker{store: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	item := store.WorkItem{
		Operation:       domain.Operation{ID: operationID, Kind: "apply", Metadata: []byte(`{}`)},
		OperationTarget: domain.OperationTarget{ID: operationTargetID},
		Target:          domain.Target{ID: targetID, Runtime: domain.RuntimeSharedRelay},
	}
	if err := w.finalizeProfileApply(ctx, item); err == nil {
		t.Fatal("invalid Profile Apply metadata unexpectedly finalized")
	}
	var status string
	var pending bool
	if err := st.Pool().QueryRow(ctx, `SELECT o.status,bool_or(ot.governance_finalization_pending) FROM operations o JOIN operation_targets ot ON ot.operation_id=o.id WHERE o.id=$1 GROUP BY o.id`, operationID).Scan(&status, &pending); err != nil {
		t.Fatal(err)
	}
	if status != bridgeprotocol.OperationFailed || pending {
		t.Fatalf("worker finalization failure status=%s pending=%v", status, pending)
	}
}

func TestRecoverFinalizesCompletedProfileApplyIntegration(t *testing.T) {
	ctx := context.Background()
	st := newWorkerIntegrationStore(t)
	if err := st.BootstrapEnvironment(ctx, "finalization-recovery-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	fixture := createCompletedProfileApplyForRecovery(t, st, "claude-recovery")
	w := &Worker{store: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := w.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	assertRecoveredProfileApply(t, st, fixture)
}

func TestRecoverContinuesAfterPersistedProfileApplyFinalizationFailureIntegration(t *testing.T) {
	ctx := context.Background()
	st := newWorkerIntegrationStore(t)
	if err := st.BootstrapEnvironment(ctx, "finalization-recovery-continue-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	var invalidTargetID string
	if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/codex'`).Scan(&invalidTargetID); err != nil {
		t.Fatal(err)
	}
	invalidOperationID := uuid.NewString()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,'apply','running','{}')`, invalidOperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,governance_finalization_pending,finished_at) VALUES($1,$2,$3,'succeeded',true,now())`, uuid.NewString(), invalidOperationID, invalidTargetID); err != nil {
		t.Fatal(err)
	}
	validFixture := createCompletedProfileApplyForRecovery(t, st, "claude-recovery-after-failure")

	w := &Worker{store: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := w.Recover(ctx); err != nil {
		t.Fatal(err)
	}

	var invalidStatus string
	var invalidPending int
	if err := st.Pool().QueryRow(ctx, `SELECT status FROM operations WHERE id=$1`, invalidOperationID).Scan(&invalidStatus); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM operation_targets WHERE operation_id=$1 AND governance_finalization_pending`, invalidOperationID).Scan(&invalidPending); err != nil {
		t.Fatal(err)
	}
	if invalidStatus != bridgeprotocol.OperationFailed || invalidPending != 0 {
		t.Fatalf("persisted recovery failure status=%s pending=%d", invalidStatus, invalidPending)
	}
	assertRecoveredProfileApply(t, st, validFixture)
}

type completedProfileApplyFixture struct {
	operation        domain.Operation
	profile          domain.Profile
	skillTargetRowID string
	relayTargetRowID string
}

func createCompletedProfileApplyForRecovery(t *testing.T, st *store.Store, name string) completedProfileApplyFixture {
	t.Helper()
	ctx := context.Background()
	profileID := uuid.NewString()
	profile, err := st.SaveProfile(ctx, profileID, store.ProfileInput{Name: name + "-" + profileID[:8], ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	var skillTargetID, relayTargetID string
	if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/claude'`).Scan(&skillTargetID); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/shared-relay'`).Scan(&relayTargetID); err != nil {
		t.Fatal(err)
	}
	skillManifest, err := st.ResolveProfileManifest(ctx, profile.ID, skillTargetID)
	if err != nil {
		t.Fatal(err)
	}
	relayManifest, err := st.ResolveProfileManifest(ctx, profile.ID, relayTargetID)
	if err != nil {
		t.Fatal(err)
	}
	skillToken, _, err := st.CreatePreflightConfirmation(ctx, profile.ID, skillTargetID, strings.Repeat("a", 64), skillManifest, bridgeprotocol.Diff{}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	relayToken, _, err := st.CreatePreflightConfirmation(ctx, profile.ID, relayTargetID, strings.Repeat("b", 64), relayManifest, bridgeprotocol.Diff{}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := st.CreateProfileApplyOperation(ctx, profile.ID, []string{skillToken, relayToken}, "recover-finalization-"+profileID)
	if err != nil {
		t.Fatal(err)
	}
	skillItem, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishOperationTarget(ctx, skillItem.OperationTarget.ID, bridgeprotocol.OperationSucceeded, bridgeprotocol.TargetResult{}, nil); err != nil {
		t.Fatal(err)
	}
	relayItem, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishOperationTarget(ctx, relayItem.OperationTarget.ID, bridgeprotocol.OperationSucceeded, map[string]any{
		"routingReloaded": true,
		"routingHash":     relayManifest.RelayGovernance.RoutingHash,
	}, nil); err != nil {
		t.Fatal(err)
	}

	return completedProfileApplyFixture{operation: operation, profile: profile, skillTargetRowID: skillItem.OperationTarget.ID, relayTargetRowID: relayItem.OperationTarget.ID}
}

func assertRecoveredProfileApply(t *testing.T, st *store.Store, fixture completedProfileApplyFixture) {
	t.Helper()
	ctx := context.Background()
	var status, publishedRevision string
	var snapshots, pending int
	if err := st.Pool().QueryRow(ctx, `SELECT status FROM operations WHERE id=$1`, fixture.operation.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT profile_revision_id::text FROM published_profiles WHERE profile_id=$1`, fixture.profile.ID).Scan(&publishedRevision); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM desired_snapshots WHERE source_operation_target_id IN ($1,$2)`, fixture.skillTargetRowID, fixture.relayTargetRowID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM operation_targets WHERE operation_id=$1 AND governance_finalization_pending`, fixture.operation.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if status != bridgeprotocol.OperationSucceeded || publishedRevision != fixture.profile.CurrentRevisionID || snapshots != 2 || pending != 0 {
		t.Fatalf("recovered Profile Apply status=%s published=%s snapshots=%d pending=%d", status, publishedRevision, snapshots, pending)
	}
}

func newWorkerIntegrationStore(t *testing.T) *store.Store {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	adminConfig, err := pgx.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	databaseName := "toolhub_worker_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	databaseURL, err := url.Parse(config.ConnConfig.ConnString())
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	databaseURL.Path = "/" + databaseName
	cipher, err := security.NewCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	st, err := store.Open(ctx, databaseURL.String(), cipher)
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop worker integration database: %v", err)
		}
		_ = admin.Close(context.Background())
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}
