package worker

import (
	"context"
	"encoding/json"
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

type governanceWorkerBridge struct {
	workerBridge
	contracts bridgeprotocol.ContractObservationResponse
	drain     bridgeprotocol.ObservationDrainResponse
	gc        bridgeprotocol.BackupGCResponse
	operation bridgeprotocol.Operation
	commit    bridgeprotocol.TargetResult
	scan      bridgeprotocol.ScanResponse
	err       error
}

func (fake *governanceWorkerBridge) ObserveRelayContracts(context.Context) (bridgeprotocol.ContractObservationResponse, error) {
	return fake.contracts, fake.err
}

func (fake *governanceWorkerBridge) DrainRelayObservations(context.Context, bridgeprotocol.ObservationDrainRequest) (bridgeprotocol.ObservationDrainResponse, error) {
	return fake.drain, fake.err
}

func (fake *governanceWorkerBridge) GCBackups(context.Context, string, bridgeprotocol.BackupGCRequest) (bridgeprotocol.BackupGCResponse, error) {
	return fake.gc, fake.err
}

func (fake *governanceWorkerBridge) Operation(context.Context, string) (bridgeprotocol.Operation, error) {
	return fake.operation, fake.err
}

func (fake *governanceWorkerBridge) Commit(context.Context, string, string, bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	return fake.commit, fake.err
}

func (fake *governanceWorkerBridge) Scan(context.Context, string, bridgeprotocol.ScanRequest) (bridgeprotocol.ScanResponse, error) {
	return fake.scan, fake.err
}

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

func TestContractObserveControlIngestsNormalizedRelayBatchIntegration(t *testing.T) {
	ctx := context.Background()
	st := newWorkerIntegrationStore(t)
	server, _, relayRevisionID := setupWorkerTelemetryFixture(t, st, "contract-control")
	fake := &governanceWorkerBridge{contracts: bridgeprotocol.ContractObservationResponse{
		RelayConfigurationRevisionID: relayRevisionID,
		Servers: []bridgeprotocol.ContractServerObservation{{
			ServerID: server.ID, ServerName: server.Name, MCPConfigRevisionID: server.CurrentRevisionID,
			Tools: []bridgeprotocol.ContractToolDTO{{Name: "read_item", RuntimeName: "read_item", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}}, OutputSchema: map[string]any{}, Annotations: map[string]any{"readOnlyHint": true}}},
		}},
	}}
	w := &Worker{store: st, bridge: fake, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now}
	result, apiErr := w.executeControl(ctx, domain.Operation{ID: uuid.NewString(), Kind: "contract_observe"})
	if apiErr != nil || result["observed"] != 1 || result["paused"] != 1 {
		t.Fatalf("contract control result=%v error=%+v", result, apiErr)
	}
	var reviewState string
	if err := st.Pool().QueryRow(ctx, `SELECT review_state FROM mcp_contract_state WHERE server_id=$1`, server.ID).Scan(&reviewState); err != nil {
		t.Fatal(err)
	}
	if reviewState != "paused" {
		t.Fatalf("review state=%q", reviewState)
	}
}

func TestTelemetryPullControlAdvancesCursorAndAggregatesIntegration(t *testing.T) {
	ctx := context.Background()
	st := newWorkerIntegrationStore(t)
	server, profile, _ := setupWorkerTelemetryFixture(t, st, "telemetry-control")
	var toolID string
	if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM mcp_tools WHERE server_id=$1 AND name='read_item'`, server.ID).Scan(&toolID); err != nil {
		t.Fatal(err)
	}
	bootID := uuid.NewString()
	fake := &governanceWorkerBridge{drain: bridgeprotocol.ObservationDrainResponse{
		BootID: bootID, NextSequence: 1,
		Items: []bridgeprotocol.Observation{{
			BootID: bootID, Sequence: 1, ObservedAt: 1_786_838_400, MinuteBucket: "2026-08-16T00:00:00Z",
			ProfileID: profile.ID, ProfileRevisionID: profile.CurrentRevisionID, ServerID: server.ID, ToolID: toolID,
			Decision: "allow", Outcome: "executed", ErrorClass: "none", DurationBucket: "lt_100ms", ReasonCodes: []string{"reviewed-read-only"},
		}},
	}}
	w := &Worker{store: st, bridge: fake, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now}
	result, apiErr := w.executeControl(ctx, domain.Operation{ID: uuid.NewString(), Kind: "relay_telemetry_pull"})
	if apiErr != nil || result["accepted"] != 1 || result["nextSequence"] != int64(1) {
		t.Fatalf("telemetry control result=%v error=%+v", result, apiErr)
	}
	var calls int64
	if err := st.Pool().QueryRow(ctx, `SELECT coalesce(sum(call_count),0) FROM mcp_daily_aggregates`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("telemetry calls=%d", calls)
	}
}

func TestBackupGCAlsoDeletesExpiredTelemetryIntegration(t *testing.T) {
	ctx := context.Background()
	st := newWorkerIntegrationStore(t)
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	if _, err := st.Pool().Exec(ctx, `INSERT INTO mcp_daily_aggregates(day,client_kind,decision,outcome,call_count,error_count,duration_bucket) VALUES($1,'shared','allow','executed',1,0,'lt_10ms')`, now.AddDate(0, 0, -31)); err != nil {
		t.Fatal(err)
	}
	fake := &governanceWorkerBridge{gc: bridgeprotocol.BackupGCResponse{RemovedBackupIDs: []string{}}}
	w := &Worker{store: st, bridge: fake, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return now }}
	result, apiErr := w.executeControl(ctx, domain.Operation{ID: uuid.NewString(), Kind: "backup_gc"})
	if apiErr != nil || result["telemetryRowsDeleted"] != int64(1) {
		t.Fatalf("backup GC result=%v error=%+v", result, apiErr)
	}
}

func TestGovernanceApplyClearsIntentionalRelayPauseIntegration(t *testing.T) {
	for _, kind := range []string{"relay_config_apply", "policy_apply"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			st := newWorkerIntegrationStore(t)
			_, profile, sourceRevisionID := setupWorkerTelemetryFixture(t, st, "clear-pause-"+kind)
			if err := st.SetRelayIntentionalPaused(ctx, true); err != nil {
				t.Fatal(err)
			}
			var targetID string
			if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/shared-relay'`).Scan(&targetID); err != nil {
				t.Fatal(err)
			}
			target, err := st.Target(ctx, targetID)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := st.ResolveProfileManifest(ctx, profile.ID, targetID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.PinDesiredSnapshot(ctx, targetID, "relay_config_apply", sourceRevisionID, "", manifest); err != nil {
				t.Fatal(err)
			}
			request, err := json.Marshal(map[string]any{
				"manifest": manifest, "targetRevision": target.TargetRevision,
				"sourceKind": "relay_config_apply", "sourceId": sourceRevisionID,
			})
			if err != nil {
				t.Fatal(err)
			}
			result := bridgeprotocol.TargetResult{
				Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy,
				TargetRevision: strings.Repeat("f", 64),
				Relay:          &bridgeprotocol.RelayStatus{State: "active", Healthy: true, FixedPort: 6276, SystemdEnabled: true},
			}
			fake := &governanceWorkerBridge{
				commit: result,
				scan:   bridgeprotocol.ScanResponse{TargetRevision: result.TargetRevision, Members: []bridgeprotocol.InventoryMember{}, Relay: result.Relay},
			}
			worker := &Worker{store: st, bridge: fake, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now}
			if err := worker.validateTargetBinding(ctx, toBridgeTarget(target), manifest); err != nil {
				t.Fatalf("invalid governance Apply fixture: %v manifestTarget=%+v target=%+v", err, manifest.Target, toBridgeTarget(target))
			}
			item := store.WorkItem{
				Operation:       domain.Operation{ID: uuid.NewString(), Kind: kind},
				OperationTarget: domain.OperationTarget{ID: uuid.NewString(), Request: request},
				Target:          target,
			}

			if _, apiErr := worker.executeCommit(ctx, item, toBridgeTarget(target)); apiErr != nil {
				t.Fatalf("execute %s: %+v", kind, apiErr)
			}
			settings, err := st.Settings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if settings.RelayIntentionalPaused {
				t.Fatalf("%s left the Relay intentionally paused", kind)
			}
		})
	}
}

func setupWorkerTelemetryFixture(t *testing.T, st *store.Store, suffix string) (domain.MCPServer, domain.Profile, string) {
	t.Helper()
	ctx := context.Background()
	if err := st.BootstrapEnvironment(ctx, "worker-"+suffix, "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	server, err := st.SaveMCPServer(ctx, "", store.MCPInput{Name: "worker-" + suffix, Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := st.ObserveContracts(ctx, store.ContractObservationInput{ServerID: server.ID, Tools: []store.ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, contract.Revision.ID); err != nil {
		t.Fatal(err)
	}
	relay, err := st.SaveRelayConfiguration(ctx, store.RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE relay_configuration_state SET applied_revision_id=$1 WHERE singleton`, relay.ID); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), store.ProfileInput{
		Name: "worker-" + suffix, ClientKind: "claude", Category: "coding",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []store.ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: contract.Revision.ID, VisibilityMode: "all_accepted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, profile, relay.ID
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
	if err := w.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	assertRecoveredProfileApply(t, st, fixture)
}

func TestRecoverFinalizesRelayAndPolicyApplyExactlyOnceIntegration(t *testing.T) {
	for _, kind := range []string{"relay_config_apply", "policy_apply"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			st := newWorkerIntegrationStore(t)
			server, profile, appliedRelayRevisionID := setupWorkerTelemetryFixture(t, st, "recover-"+kind)
			var relayTargetID string
			if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/shared-relay'`).Scan(&relayTargetID); err != nil {
				t.Fatal(err)
			}
			manifest, err := st.ResolveProfileManifest(ctx, profile.ID, relayTargetID)
			if err != nil {
				t.Fatal(err)
			}
			metadata := map[string]any{}
			candidate := store.RoutingBundleCandidate{}
			revisionID := ""
			sourceRevisionID := appliedRelayRevisionID
			switch kind {
			case "relay_config_apply":
				revision, err := st.SaveRelayConfiguration(ctx, store.RelayConfigurationInput{
					MCPServerIDs:   []string{server.ID},
					MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
					Metadata:       map[string]any{"recovery": uuid.NewString()},
				})
				if err != nil {
					t.Fatal(err)
				}
				revisionID = revision.ID
				sourceRevisionID = revision.ID
				candidate.Mode = "compatibility"
				candidate.RelayConfigurationRevisionID = revision.ID
				metadata = map[string]any{
					"revisionId":   revision.ID,
					"mode":         "compatibility",
					"expectedMode": "compatibility",
					"expectedAppliedRelayConfigurationRevisionId": appliedRelayRevisionID,
					"affectedProfileRevisions":                    map[string]string{},
					"expectedPublishedProfileRevisions":           map[string]string{},
				}
			case "policy_apply":
				var expectedPolicyRevisionID string
				if err := st.Pool().QueryRow(ctx, `SELECT applied_revision_id::text FROM global_policy_state WHERE singleton`).Scan(&expectedPolicyRevisionID); err != nil {
					t.Fatal(err)
				}
				revision, err := st.SaveGlobalPolicy(ctx, store.GlobalPolicyInput{ExplicitOverrides: map[string]string{"recovery": "deny"}})
				if err != nil {
					t.Fatal(err)
				}
				revisionID = revision.ID
				candidate.GlobalPolicyRevisionID = revision.ID
				metadata = map[string]any{"revisionId": revision.ID, "expectedAppliedGlobalPolicyRevisionId": expectedPolicyRevisionID}
			}
			bundle, routingHash, err := st.RenderCandidateRoutingBundle(ctx, candidate)
			if err != nil {
				t.Fatal(err)
			}
			routingBody, canonicalHash, err := bundle.Canonical()
			if err != nil || canonicalHash != routingHash {
				t.Fatalf("canonical routing bundle hash=%s want=%s err=%v", canonicalHash, routingHash, err)
			}
			metadata["routingHash"] = routingHash
			manifest.RelayGovernance = &bridgeprotocol.RelayGovernanceManifest{
				RelayConfigurationRevisionID: bundle.RelayConfigurationRevisionID,
				RelayConfigurationHash:       bundle.RelayConfigurationHash,
				RoutingBundle:                routingBody,
				RoutingHash:                  routingHash,
			}
			if err := manifest.Validate(true); err != nil {
				t.Fatal(err)
			}

			operationID, operationTargetID := uuid.NewString(), uuid.NewString()
			metadataJSON, _ := json.Marshal(metadata)
			requestJSON, _ := json.Marshal(map[string]any{"manifest": manifest, "sourceKind": "relay_config_apply", "sourceId": sourceRevisionID})
			resultJSON, _ := json.Marshal(map[string]any{"routingReloaded": true, "routingHash": routingHash})
			if _, err := st.Pool().Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,$2,'running',$3::jsonb)`, operationID, kind, string(metadataJSON)); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Pool().Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,request,result,finished_at,governance_finalization_pending) VALUES($1,$2,$3,'succeeded',$4::jsonb,$5::jsonb,now(),true)`, operationTargetID, operationID, relayTargetID, string(requestJSON), string(resultJSON)); err != nil {
				t.Fatal(err)
			}

			w := &Worker{store: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			for attempt := 0; attempt < 2; attempt++ {
				if err := w.Recover(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var operationStatus, appliedRevisionID string
			var snapshots, pending int
			if err := st.Pool().QueryRow(ctx, `SELECT status FROM operations WHERE id=$1`, operationID).Scan(&operationStatus); err != nil {
				t.Fatal(err)
			}
			if kind == "relay_config_apply" {
				err = st.Pool().QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&appliedRevisionID)
			} else {
				err = st.Pool().QueryRow(ctx, `SELECT applied_revision_id::text FROM global_policy_state WHERE singleton`).Scan(&appliedRevisionID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM desired_snapshots WHERE source_operation_target_id=$1`, operationTargetID).Scan(&snapshots); err != nil {
				t.Fatal(err)
			}
			if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM operation_targets WHERE operation_id=$1 AND governance_finalization_pending`, operationID).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if operationStatus != bridgeprotocol.OperationSucceeded || appliedRevisionID != revisionID || snapshots != 1 || pending != 0 {
				t.Fatalf("recovered %s status=%s applied=%s snapshots=%d pending=%d", kind, operationStatus, appliedRevisionID, snapshots, pending)
			}
		})
	}
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

func TestRecoveryLeavesRelayQueuedAfterSkillSucceededIntegration(t *testing.T) {
	ctx := context.Background()
	st := newWorkerIntegrationStore(t)
	if err := st.BootstrapEnvironment(ctx, "dependency-recovery-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	waiting := createProfileApplyWaitingForRelay(t, st, "claude-dependency-recovery")
	w := &Worker{store: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := w.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	relayItem, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if relayItem.Operation.ID != waiting.operation.ID || relayItem.Target.Runtime != domain.RuntimeSharedRelay {
		t.Fatalf("recovered target operation=%s runtime=%s", relayItem.Operation.ID, relayItem.Target.Runtime)
	}
	var snapshots int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM desired_snapshots WHERE source_operation_target_id=$1`, waiting.skillTargetRowID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 0 {
		t.Fatalf("dependency-only recovery pinned %d snapshots before relay success", snapshots)
	}
}

func TestRecoveryRequeuesBridgeTerminalPostgresRunningTargetIntegration(t *testing.T) {
	ctx := context.Background()
	st := newWorkerIntegrationStore(t)
	if err := st.BootstrapEnvironment(ctx, "bridge-terminal-recovery-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	var targetID string
	if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/claude'`).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	operationID, operationTargetID := uuid.NewString(), uuid.NewString()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO operations(id,kind,status,metadata,started_at) VALUES($1,'edit','running','{}',now())`, operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,bridge_operation_id,started_at) VALUES($1,$2,$3,'running',$4,now())`, operationTargetID, operationID, targetID, "bridge-terminal-operation"); err != nil {
		t.Fatal(err)
	}
	fake := &governanceWorkerBridge{operation: bridgeprotocol.Operation{ID: "bridge-terminal-operation", Status: bridgeprotocol.OperationSucceeded}}
	w := &Worker{store: st, bridge: fake, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := w.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	var operationStatus, targetStatus string
	var attempt int
	if err := st.Pool().QueryRow(ctx, `SELECT o.status,ot.status,ot.attempt FROM operations o JOIN operation_targets ot ON ot.operation_id=o.id WHERE ot.id=$1`, operationTargetID).Scan(&operationStatus, &targetStatus, &attempt); err != nil {
		t.Fatal(err)
	}
	if operationStatus != bridgeprotocol.OperationQueued || targetStatus != bridgeprotocol.OperationQueued || attempt != 2 {
		t.Fatalf("recovered operation=%s target=%s attempt=%d", operationStatus, targetStatus, attempt)
	}
}

type completedProfileApplyFixture struct {
	operation        domain.Operation
	profile          domain.Profile
	skillTargetRowID string
	relayTargetRowID string
}

type waitingProfileApplyFixture struct {
	operation        domain.Operation
	profile          domain.Profile
	skillTargetRowID string
}

func createCompletedProfileApplyForRecovery(t *testing.T, st *store.Store, name string) completedProfileApplyFixture {
	t.Helper()
	waiting := createProfileApplyWaitingForRelay(t, st, name)
	ctx := context.Background()
	relayItem, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Manifest bridgeprotocol.DesiredManifest `json:"manifest"`
	}
	if err := json.Unmarshal(relayItem.OperationTarget.Request, &request); err != nil || request.Manifest.RelayGovernance == nil {
		t.Fatalf("decode relay recovery request: %v", err)
	}
	if err := st.FinishOperationTarget(ctx, relayItem.OperationTarget.ID, bridgeprotocol.OperationSucceeded, map[string]any{
		"routingReloaded": true,
		"routingHash":     request.Manifest.RelayGovernance.RoutingHash,
	}, nil); err != nil {
		t.Fatal(err)
	}

	return completedProfileApplyFixture{operation: waiting.operation, profile: waiting.profile, skillTargetRowID: waiting.skillTargetRowID, relayTargetRowID: relayItem.OperationTarget.ID}
}

func createProfileApplyWaitingForRelay(t *testing.T, st *store.Store, name string) waitingProfileApplyFixture {
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
	return waitingProfileApplyFixture{operation: operation, profile: profile, skillTargetRowID: skillItem.OperationTarget.ID}
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
