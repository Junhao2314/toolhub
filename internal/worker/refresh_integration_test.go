package worker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

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
	config.ConnConfig.Database = databaseName
	databaseURL := config.ConnString()
	cipher, err := security.NewCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	st, err := store.Open(ctx, databaseURL, cipher)
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
