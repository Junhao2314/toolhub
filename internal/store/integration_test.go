package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/security"
)

func TestGenerationTwoDatabaseInvariantsIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)

	if err := st.RequireSchemaGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	created, err := st.BootstrapAccount(ctx, "admin", "ToolHub-Integration-2026")
	if err != nil || !created {
		t.Fatalf("bootstrap account: created=%v err=%v", created, err)
	}
	created, err = st.BootstrapAccount(ctx, "bad@example.com", "")
	if err != nil || created {
		t.Fatalf("existing account must ignore bootstrap credentials: created=%v err=%v", created, err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO account(singleton,username,password_hash) SELECT true,'other',password_hash FROM account`); err == nil {
		t.Fatal("singleton account constraint accepted a second account")
	}
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}

	target := integrationTarget(t, st, "local/claude")
	manifest := bridgeprotocol.DesiredManifest{
		SchemaVersion:    bridgeprotocol.ManifestSchemaVersion,
		Target:           bridgeTarget(target),
		Skills:           []bridgeprotocol.SkillMember{},
		MCPServers:       []bridgeprotocol.MCPMember{},
		ManagedMemberIDs: []string{},
	}
	snapshot, err := st.PinDesiredSnapshot(ctx, target.ID, "target_edit", "", "", manifest)
	if err != nil {
		t.Fatalf("pin valid manifest: %v", err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE desired_snapshots SET manifest_hash=$2 WHERE id=$1`, snapshot.ID, strings.Repeat("f", 64)); err == nil {
		t.Fatal("immutable desired snapshot accepted an update")
	}

	validBody, _, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	memberID, serverID, secretID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	relayManifest := bridgeprotocol.DesiredManifest{
		SchemaVersion: bridgeprotocol.ManifestSchemaVersion,
		Target:        bridgeTarget(relayTarget),
		MCPServers: []bridgeprotocol.MCPMember{{
			MemberID: memberID, ServerID: serverID, Revision: 1, Name: "safe-server", Transport: "http",
			URL: "https://example.invalid/mcp", EnvRefs: map[string]string{"TOKEN": secretID}, ContentHash: strings.Repeat("a", 64),
		}},
		Skills:           []bridgeprotocol.SkillMember{},
		ManagedMemberIDs: []string{memberID},
		RelayPort:        6276,
	}
	relayBody, relayHash, err := relayManifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,1,'target_edit',1,$3,$4)`, uuid.NewString(), relayTarget.ID, relayHash, jsonText(relayBody)); err != nil {
		t.Fatalf("valid shared-relay MCP manifest was rejected: %v", err)
	}

	badPlaintext := decodeObject(t, relayBody)
	badPlaintext["mcpServers"] = []any{map[string]any{
		"memberId": memberID, "serverId": serverID, "revision": 1, "name": "unsafe",
		"transport": "http", "url": "https://example.invalid/mcp", "contentHash": strings.Repeat("a", 64),
		"env": map[string]string{"TOKEN": "plaintext"},
	}}
	badPlaintext["managedMemberIds"] = []string{memberID}
	assertManifestInsertRejected(t, st, relayTarget.ID, 100, badPlaintext)

	badReference := decodeObject(t, relayBody)
	badReference["mcpServers"] = []any{map[string]any{
		"memberId": memberID, "serverId": serverID, "revision": 1, "name": "unsafe",
		"transport": "http", "url": "https://example.invalid/mcp", "contentHash": strings.Repeat("b", 64),
		"envRefs": map[string]string{"TOKEN": "not-a-secret-uuid"},
	}}
	badReference["managedMemberIds"] = []string{memberID}
	assertManifestInsertRejected(t, st, relayTarget.ID, 101, badReference)

	badManagedID := decodeObject(t, validBody)
	badManagedID["managedMemberIds"] = []any{nil}
	assertManifestInsertRejected(t, st, target.ID, 102, badManagedID)

	badLocalScope := decodeObject(t, relayBody)
	localTarget := bridgeTarget(target)
	badLocalScope["target"] = localTarget
	delete(badLocalScope, "relayPort")
	assertManifestInsertRejected(t, st, target.ID, 103, badLocalScope)

	badHermes := decodeObject(t, validBody)
	hermesTarget := integrationTarget(t, st, "local/hermes")
	badHermes["target"] = bridgeTarget(hermesTarget)
	assertManifestInsertRejected(t, st, hermesTarget.ID, 104, badHermes)

	operation, err := st.CreateOperation(ctx, CreateOperationInput{
		Kind:           "edit",
		Request:        map[string]any{},
		TargetIDs:      []string{relayTarget.ID},
		TargetRequests: map[string]any{relayTarget.ID: map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var operationTargetID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM operation_targets WHERE operation_id=$1`, operation.ID).Scan(&operationTargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,source_operation_target_id,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,2,'target_edit',$3,1,$4,$5)`, uuid.NewString(), relayTarget.ID, operationTargetID, relayHash, jsonText(relayBody)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,source_operation_target_id,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,3,'target_edit',$3,1,$4,$5)`, uuid.NewString(), relayTarget.ID, operationTargetID, relayHash, jsonText(relayBody)); err == nil {
		t.Fatal("source operation target produced more than one desired snapshot")
	}
}

func TestRelayProjectionRetryAndProbeCadenceIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	target := integrationTarget(t, st, "local/shared-relay")
	manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: bridgeTarget(target), Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{}, RelayPort: 6276}
	if _, err := st.PinDesiredSnapshot(ctx, target.ID, "target_edit", "", "", manifest); err != nil {
		t.Fatal(err)
	}
	healthy := bridgeprotocol.RelayStatus{State: "active", Healthy: true, Endpoint: "http://127.0.0.1:6276/mcp", FixedPort: 6276, SystemdEnabled: true, Contract: "verified", MemberStatuses: []bridgeprotocol.RelayMemberStatus{}}
	if _, err := st.UpdateRelayProjection(ctx, target.ID, healthy, bridgeprotocol.HealthHealthy, "", "", map[string]any{}, false, RelayProjectionReset); err != nil {
		t.Fatal(err)
	}
	drift := bridgeprotocol.Diff{Add: []bridgeprotocol.DiffItem{{Kind: "mcp", Name: "missing", Reason: "missing"}}, Replace: []bridgeprotocol.DiffItem{}, Delete: []bridgeprotocol.DiffItem{}, Excluded: []bridgeprotocol.DiffItem{}}
	if _, err := st.UpdateTargetHealth(ctx, target.ID, bridgeprotocol.HealthDrifted, "", "", drift, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRelayProjection(ctx, target.ID, healthy, bridgeprotocol.HealthDrifted, "", "", nil, false, RelayProjectionObserve); err != nil {
		t.Fatal(err)
	}
	projectedDrift, err := st.Target(ctx, target.ID)
	if err != nil || !strings.Contains(string(projectedDrift.DriftSummary), `"name": "missing"`) && !strings.Contains(string(projectedDrift.DriftSummary), `"name":"missing"`) {
		t.Fatalf("relay observation erased config drift: drift=%s err=%v", projectedDrift.DriftSummary, err)
	}
	created, err := st.EnqueueReconciles(ctx)
	if err != nil || created != 1 {
		t.Fatalf("five-minute config reconcile created=%d err=%v", created, err)
	}
	operationTargetID, fullProbe := relayScheduledRequest(t, st, target.ID)
	if fullProbe {
		t.Fatal("fresh member check unexpectedly requested another full probe")
	}
	finishScheduledTarget(t, st, operationTargetID)
	if _, err := st.pool.Exec(ctx, `UPDATE target_desired_snapshots SET relay_last_member_check_at=now()-interval '31 minutes' WHERE target_id=$1`, target.ID); err != nil {
		t.Fatal(err)
	}
	if created, err = st.EnqueueReconciles(ctx); err != nil || created != 1 {
		t.Fatalf("30-minute member reconcile created=%d err=%v", created, err)
	}
	operationTargetID, fullProbe = relayScheduledRequest(t, st, target.ID)
	if !fullProbe {
		t.Fatal("stale member check did not request a full relay probe")
	}
	finishScheduledTarget(t, st, operationTargetID)

	blocked := bridgeprotocol.RelayStatus{State: "blocked", Endpoint: "http://127.0.0.1:6276/mcp", FixedPort: 6276, SystemdEnabled: true, Contract: "unavailable", ErrorCode: bridgeprotocol.ErrRelayUnhealthy, ErrorReason: "relay unit is not active", MemberStatuses: []bridgeprotocol.RelayMemberStatus{}}
	if _, err := st.UpdateRelayProjection(ctx, target.ID, blocked, bridgeprotocol.HealthBlocked, blocked.ErrorCode, blocked.ErrorReason, map[string]any{}, false, RelayProjectionReset); err != nil {
		t.Fatal(err)
	}
	projected, err := st.Target(ctx, target.ID)
	if err != nil || projected.RelayFailureCount != 0 || projected.RelayNextRetryAt == nil || projected.RelaySuspended {
		t.Fatalf("initial blocked projection=%+v err=%v", projected, err)
	}
	if created, err = st.EnqueueReconciles(ctx); err != nil || created != 0 {
		t.Fatalf("future retry was not deferred: created=%d err=%v", created, err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE target_desired_snapshots SET relay_next_retry_at=now()-interval '1 second' WHERE target_id=$1`, target.ID); err != nil {
		t.Fatal(err)
	}
	if created, err = st.EnqueueReconciles(ctx); err != nil || created != 1 {
		t.Fatalf("due retry created=%d err=%v", created, err)
	}
	operationTargetID, fullProbe = relayScheduledRequest(t, st, target.ID)
	if !fullProbe {
		t.Fatal("due retry did not request full member health")
	}
	finishScheduledTarget(t, st, operationTargetID)
	for retry := 1; retry <= 3; retry++ {
		if _, err := st.UpdateRelayProjection(ctx, target.ID, blocked, bridgeprotocol.HealthBlocked, blocked.ErrorCode, blocked.ErrorReason, map[string]any{}, false, RelayProjectionRetry); err != nil {
			t.Fatal(err)
		}
	}
	projected, err = st.Target(ctx, target.ID)
	if err != nil || projected.RelayFailureCount != 3 || !projected.RelaySuspended || projected.RelayNextRetryAt != nil {
		t.Fatalf("suspended projection=%+v err=%v", projected, err)
	}
	if created, err = st.EnqueueReconciles(ctx); err != nil || created != 0 {
		t.Fatalf("suspended relay was scheduled: created=%d err=%v", created, err)
	}
	if _, err := st.UpdateRelayProjection(ctx, target.ID, healthy, bridgeprotocol.HealthHealthy, "", "", map[string]any{}, false, RelayProjectionReset); err != nil {
		t.Fatal(err)
	}
	projected, err = st.Target(ctx, target.ID)
	if err != nil || projected.RelayFailureCount != 0 || projected.RelaySuspended || projected.RelayNextRetryAt != nil || projected.RelayLastMemberCheckAt == nil {
		t.Fatalf("healthy reset projection=%+v err=%v", projected, err)
	}
}

func TestRuntimeRelayStatusBootstrapsMissingSnapshotIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	target := integrationTarget(t, st, "local/shared-relay")
	status := bridgeprotocol.RelayStatus{State: "active", Healthy: true, Endpoint: "http://127.0.0.1:6276/mcp", FixedPort: 6276, SystemdEnabled: true, Contract: "verified", MemberStatuses: []bridgeprotocol.RelayMemberStatus{}}
	revision := strings.Repeat("a", 64)
	if err := st.UpdateRuntimeRelayStatus(ctx, target.ID, revision, status); err != nil {
		t.Fatal(err)
	}
	raw, storedRevision, err := st.RuntimeSnapshot(ctx, target.ID)
	if err != nil || storedRevision != revision {
		t.Fatalf("runtime snapshot revision=%q err=%v", storedRevision, err)
	}
	var inventory struct {
		Members []bridgeprotocol.InventoryMember `json:"members"`
		Relay   bridgeprotocol.RelayStatus       `json:"relay"`
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Members == nil || inventory.Relay.FixedPort != 6276 || inventory.Relay.Contract != "verified" {
		t.Fatalf("runtime relay inventory=%s", raw)
	}
}

func TestRelayProjectionMigrationUpgradesGenerationTwoDatabaseIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, false)
	body, err := migrationFS.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `CREATE TABLE schema_migrations (version bigint PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var versions, relayColumns int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='target_desired_snapshots' AND column_name LIKE 'relay_%'`).Scan(&relayColumns); err != nil {
		t.Fatal(err)
	}
	if versions != 7 || relayColumns != 5 {
		t.Fatalf("migration versions=%d relayColumns=%d", versions, relayColumns)
	}
	var partialAllowed bool
	if err := st.pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) LIKE '%partial%' FROM pg_constraint WHERE conrelid='operation_targets'::regclass AND conname='operation_targets_status_check'`).Scan(&partialAllowed); err != nil {
		t.Fatal(err)
	}
	if !partialAllowed {
		t.Fatal("operation target status constraint does not allow partial")
	}
}

func relayScheduledRequest(t *testing.T, st *Store, targetID string) (string, bool) {
	t.Helper()
	var operationTargetID string
	var request json.RawMessage
	if err := st.pool.QueryRow(context.Background(), `SELECT id::text,request FROM operation_targets WHERE target_id=$1 AND status='queued' ORDER BY created_at DESC LIMIT 1`, targetID).Scan(&operationTargetID, &request); err != nil {
		t.Fatal(err)
	}
	var options struct {
		FullRelayProbe bool `json:"fullRelayProbe"`
	}
	if err := json.Unmarshal(request, &options); err != nil {
		t.Fatal(err)
	}
	return operationTargetID, options.FullRelayProbe
}

func finishScheduledTarget(t *testing.T, st *Store, operationTargetID string) {
	t.Helper()
	if _, err := st.pool.Exec(context.Background(), `WITH finished AS (UPDATE operation_targets SET status='succeeded',finished_at=now(),updated_at=now() WHERE id=$1 RETURNING operation_id) UPDATE operations SET status='succeeded',finished_at=now(),updated_at=now() WHERE id=(SELECT operation_id FROM finished)`, operationTargetID); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyDatabaseIsRefusedIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, false)
	if _, err := st.pool.Exec(ctx, `CREATE TABLE legacy_users(id bigint PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); !errors.Is(err, ErrLegacySchema) || !strings.Contains(err.Error(), "fresh PostgreSQL volume") {
		t.Fatalf("expected actionable legacy refusal, got %v", err)
	}
}

func TestBootstrapEnvironmentPreservesPersistedSettingsIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	updated, err := st.UpdateSettings(ctx, domain.Settings{
		ManagedUsername: "operator",
		UpdateCron:      "15 3 * * *",
		Timezone:        "Asia/Shanghai",
		RelayPort:       6380,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BootstrapEnvironment(ctx, "ignored-host", "environment-user", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	persisted, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ManagedUsername != updated.ManagedUsername || persisted.UpdateCron != updated.UpdateCron || persisted.Timezone != updated.Timezone || persisted.RelayPort != updated.RelayPort {
		t.Fatalf("restart changed persisted settings: before=%+v after=%+v", updated, persisted)
	}
	target := integrationTarget(t, st, "local/claude")
	if target.ManagedUsername != updated.ManagedUsername {
		t.Fatalf("local target managed username=%q want=%q", target.ManagedUsername, updated.ManagedUsername)
	}
}

func TestTargetEditPreservesPinnedSkillVersionIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	target := integrationTarget(t, st, "local/claude")

	sourceID, skillID := uuid.NewString(), uuid.NewString()
	artifactOne, versionOne := uuid.NewString(), uuid.NewString()
	artifactTwo, versionTwo := uuid.NewString(), uuid.NewString()
	shaOne, contentOne := strings.Repeat("1", 64), strings.Repeat("2", 64)
	shaTwo, contentTwo := strings.Repeat("3", 64), strings.Repeat("4", 64)
	if _, err := st.pool.Exec(ctx, `INSERT INTO skill_sources(id,kind,name) VALUES($1,'local','pinned-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO skills(id,source_id,slug,name) VALUES($1,$2,'pinned-skill','Pinned Skill')`, skillID, sourceID); err != nil {
		t.Fatal(err)
	}
	insertArtifactVersion(t, st, skillID, artifactOne, versionOne, shaOne, contentOne, "v1")
	if _, err := st.pool.Exec(ctx, `UPDATE skills SET current_version_id=$2 WHERE id=$1`, skillID, versionOne); err != nil {
		t.Fatal(err)
	}

	memberID := stableMemberID("skill", skillID)
	pinned := bridgeprotocol.DesiredManifest{
		SchemaVersion: bridgeprotocol.ManifestSchemaVersion,
		Target:        bridgeTarget(target),
		Skills: []bridgeprotocol.SkillMember{{
			MemberID: memberID, SkillID: skillID, VersionID: versionOne, Slug: "pinned-skill", SHA256: shaOne, ContentHash: contentOne,
		}},
		MCPServers:       []bridgeprotocol.MCPMember{},
		ManagedMemberIDs: []string{memberID},
	}
	if _, err := st.PinDesiredSnapshot(ctx, target.ID, "target_edit", "", "", pinned); err != nil {
		t.Fatal(err)
	}

	insertArtifactVersion(t, st, skillID, artifactTwo, versionTwo, shaTwo, contentTwo, "v2")
	if _, err := st.pool.Exec(ctx, `UPDATE skills SET current_version_id=$2 WHERE id=$1`, skillID, versionTwo); err != nil {
		t.Fatal(err)
	}
	resolved, err := st.ResolveTargetManifest(ctx, target.ID, []string{skillID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Skills) != 1 || resolved.Skills[0].VersionID != versionOne || resolved.Skills[0].SHA256 != shaOne {
		t.Fatalf("target edit silently advanced pinned member: %+v", resolved.Skills)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE skill_artifacts SET size_bytes=size_bytes+1 WHERE id=$1`, artifactOne); err == nil {
		t.Fatal("immutable Skill artifact accepted an update")
	}
	if _, err := st.pool.Exec(ctx, `UPDATE skill_versions SET source_commit='mutated' WHERE id=$1`, versionOne); err == nil {
		t.Fatal("immutable Skill version accepted an update")
	}
}

func TestCancelOperationOnlyStopsUndispatchedWorkIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}

	queuedControl, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "refresh", Request: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CancelOperation(ctx, queuedControl.ID); err != nil {
		t.Fatal(err)
	}
	assertOperationStatus(t, st, queuedControl.ID, bridgeprotocol.OperationCancelled)

	runningControl, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "refresh", Request: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	claimedControl, err := st.ClaimControlOperation(ctx)
	if err != nil || claimedControl.ID != runningControl.ID {
		t.Fatalf("claim control operation: %+v err=%v", claimedControl, err)
	}
	if err := st.CancelOperation(ctx, runningControl.ID); err != nil {
		t.Fatal(err)
	}
	assertOperationStatus(t, st, runningControl.ID, bridgeprotocol.OperationRunning)
	if err := st.FinishControlOperation(ctx, runningControl.ID, bridgeprotocol.OperationSucceeded, map[string]any{"finished": true}, nil); err != nil {
		t.Fatal(err)
	}

	claude := integrationTarget(t, st, "local/claude")
	codex := integrationTarget(t, st, "local/codex")
	targetOperation, err := st.CreateOperation(ctx, CreateOperationInput{
		Kind:      "scan",
		Request:   map[string]any{},
		TargetIDs: []string{claude.ID, codex.ID},
		TargetRequests: map[string]any{
			claude.ID: map[string]any{},
			codex.ID:  map[string]any{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimedTarget, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CancelOperation(ctx, targetOperation.ID); err != nil {
		t.Fatal(err)
	}
	var running, cancelled int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='running'),count(*) FILTER(WHERE status='cancelled') FROM operation_targets WHERE operation_id=$1`, targetOperation.ID).Scan(&running, &cancelled); err != nil {
		t.Fatal(err)
	}
	if running != 1 || cancelled != 1 {
		t.Fatalf("cancelled target states: running=%d cancelled=%d", running, cancelled)
	}
	if err := st.FinishOperationTarget(ctx, claimedTarget.OperationTarget.ID, bridgeprotocol.OperationSucceeded, bridgeprotocol.TargetResult{}, nil); err != nil {
		t.Fatal(err)
	}
	assertOperationStatus(t, st, targetOperation.ID, bridgeprotocol.OperationPartial)
	if err := st.CancelOperation(ctx, targetOperation.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal cancel should conflict, got %v", err)
	}
	if err := st.CancelOperation(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing cancel should be not found, got %v", err)
	}
}

func TestDiscoveredNodeArchiveRestoreLifecycleIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	online := bridgeprotocol.NodeInfo{
		NodeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("salt:online-minion")).String(),
		Name:   "online-minion", Kind: bridgeprotocol.NodeKindSalt,
		SaltMinionID: "online-minion", Status: "online", Version: "3008.0",
	}
	offline := bridgeprotocol.NodeInfo{
		NodeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("salt:offline-minion")).String(),
		Name:   "offline-minion", Kind: bridgeprotocol.NodeKindSalt,
		SaltMinionID: "offline-minion", Status: "unavailable",
	}
	if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{online, offline}); err != nil {
		t.Fatal(err)
	}

	var remoteTargets int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM targets t JOIN nodes n ON n.id=t.node_id WHERE n.kind='salt' AND n.archived_at IS NULL`).Scan(&remoteTargets); err != nil {
		t.Fatal(err)
	}
	if remoteTargets != 6 {
		t.Fatalf("remote target count=%d want=6", remoteTargets)
	}
	var offlineNodeID, offlineStatus string
	if err := st.pool.QueryRow(ctx, `SELECT id::text,status FROM nodes WHERE salt_minion_id='offline-minion' AND archived_at IS NULL`).Scan(&offlineNodeID, &offlineStatus); err != nil {
		t.Fatal(err)
	}
	if offlineStatus != "unavailable" {
		t.Fatalf("accepted offline node status=%q want=unavailable", offlineStatus)
	}
	offlineTargets := map[string]string{}
	rows, err := st.pool.Query(ctx, `SELECT runtime,id::text FROM targets WHERE node_id=$1 ORDER BY runtime`, offlineNodeID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var runtime, targetID string
		if err := rows.Scan(&runtime, &targetID); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		offlineTargets[runtime] = targetID
	}
	rows.Close()
	if len(offlineTargets) != 3 {
		t.Fatalf("offline target identities=%v want three runtimes", offlineTargets)
	}
	if err := st.UpdateNodeManagedUsername(ctx, offlineNodeID, "operator"); err != nil {
		t.Fatal(err)
	}
	offlineClaude, err := st.Target(ctx, offlineTargets[domain.RuntimeClaude])
	if err != nil {
		t.Fatal(err)
	}
	manifest := bridgeprotocol.DesiredManifest{
		SchemaVersion:    bridgeprotocol.ManifestSchemaVersion,
		Target:           bridgeTarget(offlineClaude),
		Skills:           []bridgeprotocol.SkillMember{},
		MCPServers:       []bridgeprotocol.MCPMember{},
		ManagedMemberIDs: []string{},
	}
	desired, err := st.PinDesiredSnapshot(ctx, offlineClaude.ID, "target_edit", "", "", manifest)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := st.CreateOperation(ctx, CreateOperationInput{
		Kind:      "scan",
		Request:   map[string]any{"targetIds": []string{offlineClaude.ID, offlineTargets[domain.RuntimeCodex]}},
		TargetIDs: []string{offlineClaude.ID, offlineTargets[domain.RuntimeCodex]},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE operation_targets SET pending_rerun=true WHERE id=$1`, claimed.OperationTarget.ID); err != nil {
		t.Fatal(err)
	}

	if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{online}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Target(ctx, offlineClaude.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archived target remained active: %v", err)
	}
	var archivedStatus string
	var archivedAtSet bool
	if err := st.pool.QueryRow(ctx, `SELECT status,archived_at IS NOT NULL FROM nodes WHERE id=$1`, offlineNodeID).Scan(&archivedStatus, &archivedAtSet); err != nil {
		t.Fatal(err)
	}
	if archivedStatus != "archived" || !archivedAtSet {
		t.Fatalf("archived node status=%q archivedAtSet=%v", archivedStatus, archivedAtSet)
	}
	nodesJSON, err := st.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	targetsJSON, err := st.ListTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(nodesJSON), "offline-minion") || strings.Contains(string(targetsJSON), "offline-minion") {
		t.Fatalf("archived minion remained in active projection: nodes=%s targets=%s", nodesJSON, targetsJSON)
	}
	var runningStatus string
	var runningPending bool
	if err := st.pool.QueryRow(ctx, `SELECT status,pending_rerun FROM operation_targets WHERE id=$1`, claimed.OperationTarget.ID).Scan(&runningStatus, &runningPending); err != nil {
		t.Fatal(err)
	}
	if runningStatus != bridgeprotocol.OperationRunning || runningPending {
		t.Fatalf("running archived step status=%q pendingRerun=%v", runningStatus, runningPending)
	}
	var queuedStatus string
	if err := st.pool.QueryRow(ctx, `SELECT status FROM operation_targets WHERE operation_id=$1 AND id<>$2`, operation.ID, claimed.OperationTarget.ID).Scan(&queuedStatus); err != nil {
		t.Fatal(err)
	}
	if queuedStatus != bridgeprotocol.OperationCancelled {
		t.Fatalf("undispatched archived step status=%q want=cancelled", queuedStatus)
	}
	assertOperationStatus(t, st, operation.ID, bridgeprotocol.OperationRunning)
	if created, err := st.EnqueueReconciles(ctx); err != nil || created != 0 {
		t.Fatalf("archived desired target reconcile: created=%d err=%v", created, err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT pending_rerun FROM operation_targets WHERE id=$1`, claimed.OperationTarget.ID).Scan(&runningPending); err != nil {
		t.Fatal(err)
	}
	if runningPending {
		t.Fatal("archived running step regained pending reconcile rerun")
	}
	var activeLocalNodes, activeLocalTargets int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE kind='local' AND archived_at IS NULL`).Scan(&activeLocalNodes); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM targets t JOIN nodes n ON n.id=t.node_id WHERE n.kind='local' AND n.archived_at IS NULL`).Scan(&activeLocalTargets); err != nil {
		t.Fatal(err)
	}
	if activeLocalNodes != 1 || activeLocalTargets != 4 {
		t.Fatalf("local projection changed: nodes=%d targets=%d", activeLocalNodes, activeLocalTargets)
	}

	if err := st.FinishOperationTarget(ctx, claimed.OperationTarget.ID, bridgeprotocol.OperationSucceeded, bridgeprotocol.TargetResult{}, nil); err != nil {
		t.Fatal(err)
	}
	assertOperationStatus(t, st, operation.ID, bridgeprotocol.OperationPartial)
	if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{online, offline}); err != nil {
		t.Fatal(err)
	}
	var restoredNodeID, restoredUsername string
	var restoredArchivedAt *string
	if err := st.pool.QueryRow(ctx, `SELECT id::text,coalesce(managed_username_override,''),archived_at::text FROM nodes WHERE salt_minion_id='offline-minion'`).Scan(&restoredNodeID, &restoredUsername, &restoredArchivedAt); err != nil {
		t.Fatal(err)
	}
	if restoredNodeID != offlineNodeID || restoredUsername != "operator" || restoredArchivedAt != nil {
		t.Fatalf("restored node id=%s username=%q archivedAt=%v", restoredNodeID, restoredUsername, restoredArchivedAt)
	}
	for runtime, originalID := range offlineTargets {
		var restoredTargetID string
		if err := st.pool.QueryRow(ctx, `SELECT id::text FROM targets WHERE node_id=$1 AND runtime=$2`, restoredNodeID, runtime).Scan(&restoredTargetID); err != nil {
			t.Fatal(err)
		}
		if restoredTargetID != originalID {
			t.Fatalf("restored %s target id=%s want=%s", runtime, restoredTargetID, originalID)
		}
	}
	var restoredSnapshotID string
	if err := st.pool.QueryRow(ctx, `SELECT snapshot_id::text FROM target_desired_snapshots WHERE target_id=$1`, offlineClaude.ID).Scan(&restoredSnapshotID); err != nil {
		t.Fatal(err)
	}
	if restoredSnapshotID != desired.ID {
		t.Fatalf("restored desired snapshot=%s want=%s", restoredSnapshotID, desired.ID)
	}
	if created, err := st.EnqueueReconciles(ctx); err != nil || created != 1 {
		t.Fatalf("restored desired target reconcile: created=%d err=%v", created, err)
	}
}

func TestEmptyDiscoveryArchivesAllSaltNodesOnlyIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	node := bridgeprotocol.NodeInfo{
		NodeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("salt:only-minion")).String(),
		Name:   "only-minion", Kind: bridgeprotocol.NodeKindSalt,
		SaltMinionID: "only-minion", Status: "online", Version: "3008.0",
	}
	if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{node}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertDiscoveredNodes(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var saltNodes, saltTargets, localNodes, localTargets int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE kind='salt' AND archived_at IS NULL`).Scan(&saltNodes); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM targets t JOIN nodes n ON n.id=t.node_id WHERE n.kind='salt' AND n.archived_at IS NULL`).Scan(&saltTargets); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE kind='local' AND archived_at IS NULL`).Scan(&localNodes); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM targets t JOIN nodes n ON n.id=t.node_id WHERE n.kind='local' AND n.archived_at IS NULL`).Scan(&localTargets); err != nil {
		t.Fatal(err)
	}
	if saltNodes != 0 || saltTargets != 0 || localNodes != 1 || localTargets != 4 {
		t.Fatalf("active projection saltNodes=%d saltTargets=%d localNodes=%d localTargets=%d", saltNodes, saltTargets, localNodes, localTargets)
	}
}

func TestArchivedTargetsRejectNewAndScheduledWorkIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	node := bridgeprotocol.NodeInfo{NodeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("salt:archived-intake")).String(), Name: "archived-intake", Kind: bridgeprotocol.NodeKindSalt, SaltMinionID: "archived-intake", Status: "online", Version: "3008.0"}
	if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{node}); err != nil {
		t.Fatal(err)
	}
	target := integrationTarget(t, st, "salt:archived-intake/claude")
	manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: bridgeTarget(target), Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{}}
	if _, err := st.PinDesiredSnapshot(ctx, target.ID, "target_edit", "", "", manifest); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertDiscoveredNodes(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "scan", Request: map[string]any{"targetId": target.ID}, TargetIDs: []string{target.ID}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archived target accepted new work: %v", err)
	}
	if _, err := st.ClaimOperationTarget(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archived target claim result: %v", err)
	}
	if created, err := st.EnqueueReconciles(ctx); err != nil || created != 0 {
		t.Fatalf("archived target reconcile: created=%d err=%v", created, err)
	}
	var queued int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM operation_targets ot JOIN targets t ON t.id=ot.target_id JOIN nodes n ON n.id=t.node_id WHERE n.archived_at IS NOT NULL AND ot.status='queued'`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("archived target left queued work=%d", queued)
	}
}

func TestArchivedTargetRecoveryReplaysOnlyDispatchedWorkIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	node := bridgeprotocol.NodeInfo{NodeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("salt:archive-recovery")).String(), Name: "archive-recovery", Kind: bridgeprotocol.NodeKindSalt, SaltMinionID: "archive-recovery", Status: "online", Version: "3008.0"}
	if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{node}); err != nil {
		t.Fatal(err)
	}
	claude := integrationTarget(t, st, "salt:archive-recovery/claude")
	codex := integrationTarget(t, st, "salt:archive-recovery/codex")
	first, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "scan", Request: map[string]any{"targetId": claude.ID}, TargetIDs: []string{claude.ID}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "scan", Request: map[string]any{"targetId": codex.ID}, TargetIDs: []string{codex.ID}})
	if err != nil {
		t.Fatal(err)
	}
	firstClaim, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatalf("claim first target: %v", err)
	}
	secondClaim, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatalf("claim second target: %v", err)
	}
	if firstClaim.Target.ID == codex.ID && secondClaim.Target.ID == claude.ID {
		firstClaim, secondClaim = secondClaim, firstClaim
	}
	if firstClaim.Target.ID != claude.ID || secondClaim.Target.ID != codex.ID {
		t.Fatalf("claimed targets=(%s,%s) want=(%s,%s)", firstClaim.Target.ID, secondClaim.Target.ID, claude.ID, codex.ID)
	}
	if err := st.SetBridgeOperationMetadata(ctx, firstClaim.OperationTarget.ID, "bridge-dispatched", "salt-jid"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertDiscoveredNodes(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.RequeueRunningOperationTarget(ctx, firstClaim.OperationTarget.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.RequeueRunningOperationTarget(ctx, secondClaim.OperationTarget.ID); err != nil {
		t.Fatal(err)
	}
	var firstStatus, secondStatus string
	if err := st.pool.QueryRow(ctx, `SELECT status FROM operation_targets WHERE id=$1`, firstClaim.OperationTarget.ID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT status FROM operation_targets WHERE id=$1`, secondClaim.OperationTarget.ID).Scan(&secondStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != bridgeprotocol.OperationQueued || secondStatus != bridgeprotocol.OperationCancelled {
		t.Fatalf("recovery statuses dispatched=%q undispatched=%q", firstStatus, secondStatus)
	}
	replay, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatalf("claim dispatched replay: %v", err)
	}
	if replay.Target.ID != claude.ID {
		t.Fatalf("replay target=%s want=%s", replay.Target.ID, claude.ID)
	}
	if err := st.FinishOperationTarget(ctx, replay.OperationTarget.ID, bridgeprotocol.OperationSucceeded, bridgeprotocol.TargetResult{}, nil); err != nil {
		t.Fatal(err)
	}
	assertOperationStatus(t, st, second.ID, bridgeprotocol.OperationCancelled)
	assertOperationStatus(t, st, first.ID, bridgeprotocol.OperationSucceeded)
	if created, err := st.EnqueuePendingRerun(ctx, replay.OperationTarget.ID); err != nil || created {
		t.Fatalf("archived replay scheduled rerun: created=%v err=%v", created, err)
	}
}

func TestArchivedTargetDoesNotConsumeProfileApplyConfirmationIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "Archive race", ClientKind: "claude", SkillIDs: []string{}, MCPServerIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	skillTarget := integrationTarget(t, st, "local/claude")
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	skillManifest, err := st.ResolveProfileManifest(ctx, profile.ID, skillTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	relayManifest, err := st.ResolveProfileManifest(ctx, profile.ID, relayTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	emptyDiff := bridgeprotocol.Diff{Add: []bridgeprotocol.DiffItem{}, Replace: []bridgeprotocol.DiffItem{}, Delete: []bridgeprotocol.DiffItem{}, Excluded: []bridgeprotocol.DiffItem{}}
	skillToken, _, err := st.CreatePreflightConfirmation(ctx, profile.ID, skillTarget.ID, strings.Repeat("a", 64), skillManifest, emptyDiff, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	relayToken, _, err := st.CreatePreflightConfirmation(ctx, profile.ID, relayTarget.ID, strings.Repeat("b", 64), relayManifest, emptyDiff, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE nodes SET archived_at=now(),status='archived' WHERE kind='local' AND archived_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProfileApplyOperation(ctx, profile.ID, []string{skillToken, relayToken}, "apply-archive-fail"); !errors.Is(err, ErrConflict) {
		t.Fatalf("archived Apply error=%v want conflict", err)
	}
	var consumedCount int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM preflight_confirmations WHERE token_hash=ANY($1::bytea[]) AND consumed_at IS NOT NULL`, [][]byte{security.TokenHash(skillToken), security.TokenHash(relayToken)}).Scan(&consumedCount); err != nil {
		t.Fatal(err)
	}
	if consumedCount != 0 {
		t.Fatalf("failed archived Apply consumed %d confirmations", consumedCount)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE nodes SET archived_at=NULL,status='online' WHERE kind='local'`); err != nil {
		t.Fatal(err)
	}
	operation, err := st.CreateProfileApplyOperation(ctx, profile.ID, []string{skillToken, relayToken}, "apply-archive-success")
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != bridgeprotocol.OperationQueued {
		t.Fatalf("restored Apply status=%q", operation.Status)
	}
	var operationTargets int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM operation_targets WHERE operation_id=$1`, operation.ID).Scan(&operationTargets); err != nil {
		t.Fatal(err)
	}
	if operationTargets != 2 {
		t.Fatalf("restored Apply target count=%d want=2", operationTargets)
	}
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM preflight_confirmations WHERE token_hash=ANY($1::bytea[]) AND consumed_at IS NOT NULL`, [][]byte{security.TokenHash(skillToken), security.TokenHash(relayToken)}).Scan(&consumedCount); err != nil {
		t.Fatal(err)
	}
	if consumedCount != 2 {
		t.Fatalf("successful Apply consumed %d confirmations want=2", consumedCount)
	}
}

func TestArchiveAndFinishUseCanonicalLockOrderIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	node := bridgeprotocol.NodeInfo{NodeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("salt:lock-order")).String(), Name: "lock-order", Kind: bridgeprotocol.NodeKindSalt, SaltMinionID: "lock-order", Status: "online", Version: "3008.0"}
	if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{node}); err != nil {
		t.Fatal(err)
	}
	target := integrationTarget(t, st, "salt:lock-order/claude")
	operation, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "scan", Request: map[string]any{"targetId": target.ID}, TargetIDs: []string{target.ID}})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	barrier, finishStore, archiveStore, closePools := integrationLockOrderStores(t, st)
	defer closePools()
	barrierTx, err := barrier.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrierTx.Rollback(ctx)
	if _, err := barrierTx.Exec(ctx, `SELECT id FROM operation_targets WHERE id=$1 FOR UPDATE`, claimed.OperationTarget.ID); err != nil {
		t.Fatal(err)
	}
	finishCtx, finishCancel := context.WithTimeout(ctx, 10*time.Second)
	defer finishCancel()
	finishDone := make(chan error, 1)
	go func() {
		finishDone <- finishStore.FinishOperationTarget(finishCtx, claimed.OperationTarget.ID, bridgeprotocol.OperationSucceeded, bridgeprotocol.TargetResult{}, nil)
	}()
	waitForBackendLock(t, st, "toolhub-finish-lock-order")
	probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Second)
	defer probeCancel()
	var probeErr error
	probeTx, err := st.pool.Begin(probeCtx)
	if err != nil {
		t.Fatal(err)
	}
	var ignored string
	probeErr = probeTx.QueryRow(probeCtx, `SELECT id::text FROM operations WHERE id=$1 FOR UPDATE NOWAIT`, operation.ID).Scan(&ignored)
	_ = probeTx.Rollback(probeCtx)
	var pgErr *pgconn.PgError
	if !errors.As(probeErr, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("finish did not lock parent before waiting: err=%v", probeErr)
	}
	archiveCtx, archiveCancel := context.WithTimeout(ctx, 10*time.Second)
	defer archiveCancel()
	archiveDone := make(chan error, 1)
	go func() { archiveDone <- archiveStore.UpsertDiscoveredNodes(archiveCtx, nil) }()
	waitForBackendLock(t, st, "toolhub-archive-lock-order")
	if err := barrierTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-finishDone; err != nil {
		t.Fatalf("finish returned %v", err)
	}
	if err := <-archiveDone; err != nil {
		t.Fatalf("archive returned %v", err)
	}
	assertOperationStatus(t, st, operation.ID, bridgeprotocol.OperationSucceeded)
	var status string
	if err := st.pool.QueryRow(ctx, `SELECT status FROM nodes WHERE id=$1`, node.NodeID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "archived" {
		t.Fatalf("node status=%q want archived", status)
	}
}

func TestUpdateNodeManagedUsernameOverrideIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "integration-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	nodeID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("salt:override-minion")).String()
	if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{{NodeID: nodeID, Name: "override-minion", Kind: bridgeprotocol.NodeKindSalt, SaltMinionID: "override-minion", Status: "online", Version: "3008.0"}}); err != nil {
		t.Fatal(err)
	}

	if err := st.UpdateNodeManagedUsername(ctx, nodeID, " operator "); err != nil {
		t.Fatal(err)
	}
	assertNodeManagedUsername(t, st, nodeID, "operator", true)
	if err := st.UpdateNodeManagedUsername(ctx, nodeID, ""); err != nil {
		t.Fatal(err)
	}
	assertNodeManagedUsername(t, st, nodeID, "runner", false)

	if err := st.UpdateNodeManagedUsername(ctx, nodeID, "Root User"); err == nil {
		t.Fatal("invalid managed username was accepted")
	}
	if err := st.UpdateNodeManagedUsername(ctx, uuid.NewString(), "operator"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Salt node returned %v", err)
	}
	var localNodeID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM nodes WHERE kind='local'`).Scan(&localNodeID); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateNodeManagedUsername(ctx, localNodeID, "operator"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("local node override returned %v", err)
	}
}

func assertNodeManagedUsername(t *testing.T, st *Store, nodeID, expected string, override bool) {
	t.Helper()
	ctx := context.Background()
	var actualOverride *string
	if err := st.pool.QueryRow(ctx, `SELECT managed_username_override FROM nodes WHERE id=$1`, nodeID).Scan(&actualOverride); err != nil {
		t.Fatal(err)
	}
	if override != (actualOverride != nil) || actualOverride != nil && *actualOverride != expected {
		t.Fatalf("managed username override=%v, expected value=%q configured=%v", actualOverride, expected, override)
	}
	var targetCount int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM targets WHERE node_id=$1 AND managed_username=$2`, nodeID, expected).Scan(&targetCount); err != nil {
		t.Fatal(err)
	}
	if targetCount != 3 {
		t.Fatalf("managed username propagated to %d targets, want 3", targetCount)
	}
}

func newIntegrationStore(t *testing.T, migrate bool) *Store {
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
	databaseName := "toolhub_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = databaseName
	poolConfig.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	poolConfig.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	cipher, err := security.NewCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	st := &Store{pool: pool, cipher: cipher}
	t.Cleanup(func() {
		st.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop integration database: %v", err)
		}
		_ = admin.Close(context.Background())
	})
	if migrate {
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func integrationLockOrderStores(t *testing.T, st *Store) (*pgxpool.Pool, *Store, *Store, func()) {
	t.Helper()
	ctx := context.Background()
	newPool := func(applicationName string) *pgxpool.Pool {
		config := st.pool.Config()
		config.MaxConns = 1
		config.MinConns = 1
		config.ConnConfig.RuntimeParams["application_name"] = applicationName
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			t.Fatal(err)
		}
		return pool
	}
	barrier := newPool("toolhub-barrier-lock-order")
	finishPool := newPool("toolhub-finish-lock-order")
	archivePool := newPool("toolhub-archive-lock-order")
	closePools := func() {
		archivePool.Close()
		finishPool.Close()
		barrier.Close()
	}
	return barrier, &Store{pool: finishPool, cipher: st.cipher}, &Store{pool: archivePool, cipher: st.cipher}, closePools
}

func waitForBackendLock(t *testing.T, st *Store, applicationName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var locked bool
		err := st.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1 AND wait_event_type='Lock')`, applicationName).Scan(&locked)
		if err != nil {
			t.Fatal(err)
		}
		if locked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("backend %q did not wait on a lock: %v", applicationName, ctx.Err())
		case <-ticker.C:
		}
	}
}

func integrationTarget(t *testing.T, st *Store, key string) domain.Target {
	t.Helper()
	ctx := context.Background()
	var targetID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key=$1`, key).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	target, err := st.Target(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func insertArtifactVersion(t *testing.T, st *Store, skillID, artifactID, versionID, sha, contentHash, commit string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.pool.Exec(ctx, `INSERT INTO skill_artifacts(id,canonical_sha256,content_hash,archive,size_bytes,manifest,scan_report) VALUES($1,$2,$3,$4,1,'{}','{}')`, artifactID, sha, contentHash, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO skill_versions(id,skill_id,artifact_id,source_commit,provenance) VALUES($1,$2,$3,$4,'{}')`, versionID, skillID, artifactID, commit); err != nil {
		t.Fatal(err)
	}
}

func decodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertManifestInsertRejected(t *testing.T, st *Store, targetID string, revision int64, manifest map[string]any) {
	t.Helper()
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.pool.Exec(context.Background(), `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,$3,'target_edit',1,$4,$5)`, uuid.NewString(), targetID, revision, strings.Repeat("e", 64), jsonText(body))
	if err == nil {
		t.Fatalf("invalid desired manifest was accepted: %s", body)
	}
}

func assertOperationStatus(t *testing.T, st *Store, operationID, want string) {
	t.Helper()
	operation, err := st.Operation(context.Background(), operationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != want {
		t.Fatalf("operation %s status=%s want=%s", operationID, operation.Status, want)
	}
}
