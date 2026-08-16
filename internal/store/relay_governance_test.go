package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/policy"
)

func succeededGovernanceOperation(t *testing.T, st *Store, kind, sourceID string, metadata map[string]any) string {
	t.Helper()
	ctx := context.Background()
	if err := st.BootstrapEnvironment(ctx, "governance-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	routingHash, _ := metadata["routingHash"].(string)
	if routingHash == "" {
		routingHash = strings.Repeat("f", 64)
		metadata["routingHash"] = routingHash
	}
	switch kind {
	case "relay_config_apply":
		var appliedRevisionID, defaultProfileID, mode string
		if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text,coalesce(default_profile_id::text,''),mode FROM relay_configuration_state WHERE singleton`).Scan(&appliedRevisionID, &defaultProfileID, &mode); err != nil {
			t.Fatal(err)
		}
		if metadata["expectedAppliedRelayConfigurationRevisionId"] == nil {
			metadata["expectedAppliedRelayConfigurationRevisionId"] = appliedRevisionID
		}
		if metadata["expectedDefaultProfileId"] == nil {
			metadata["expectedDefaultProfileId"] = defaultProfileID
		}
		if metadata["affectedProfileRevisions"] == nil {
			metadata["affectedProfileRevisions"] = map[string]string{}
		}
		if metadata["expectedPublishedProfileRevisions"] == nil {
			metadata["expectedPublishedProfileRevisions"] = map[string]string{}
		}
		if metadata["mode"] == nil {
			metadata["mode"] = mode
		}
		if metadata["expectedMode"] == nil {
			metadata["expectedMode"] = mode
		}
	case "policy_apply":
		var appliedRevisionID string
		if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM global_policy_state WHERE singleton`).Scan(&appliedRevisionID); err != nil {
			t.Fatal(err)
		}
		if metadata["expectedAppliedGlobalPolicyRevisionId"] == nil {
			metadata["expectedAppliedGlobalPolicyRevisionId"] = appliedRevisionID
		}
	case "apply":
		var publishedRevisionID string
		if err := st.pool.QueryRow(ctx, `SELECT coalesce((SELECT profile_revision_id::text FROM published_profiles WHERE profile_id=$1),'')`, sourceID).Scan(&publishedRevisionID); err != nil {
			t.Fatal(err)
		}
		if metadata["expectedPublishedProfileRevisionId"] == nil {
			metadata["expectedPublishedProfileRevisionId"] = publishedRevisionID
		}
		var defaultProfileID string
		if err := st.pool.QueryRow(ctx, `SELECT coalesce(default_profile_id::text,'') FROM relay_configuration_state WHERE singleton`).Scan(&defaultProfileID); err != nil {
			t.Fatal(err)
		}
		if metadata["expectedDefaultProfileId"] == nil {
			metadata["expectedDefaultProfileId"] = defaultProfileID
		}
	}
	candidate := RoutingBundleCandidate{}
	switch kind {
	case "relay_config_apply":
		if revisionID, _ := metadata["revisionId"].(string); revisionID != "" {
			candidate.Mode, _ = metadata["mode"].(string)
			candidate.RelayConfigurationRevisionID = revisionID
			candidate.PublishedProfileRevisions = testStringMap(metadata["affectedProfileRevisions"])
		} else if defaultProfileID, found := metadata["defaultProfileId"].(string); found {
			candidate.DefaultProfileID = &defaultProfileID
		}
	case "policy_apply":
		candidate.GlobalPolicyRevisionID, _ = metadata["revisionId"].(string)
	case "apply":
		candidate.PublishedProfileRevisions = map[string]string{sourceID: ""}
		if revisionID, _ := metadata["profileRevisionId"].(string); revisionID != "" {
			candidate.PublishedProfileRevisions[sourceID] = revisionID
		} else {
			var defaultProfileID string
			if err := st.pool.QueryRow(ctx, `SELECT coalesce(default_profile_id::text,'') FROM relay_configuration_state WHERE singleton`).Scan(&defaultProfileID); err != nil {
				t.Fatal(err)
			}
			if defaultProfileID == sourceID {
				emptyDefault := ""
				candidate.DefaultProfileID = &emptyDefault
			}
		}
	}
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	profileRevision := int64(0)
	if sourceID != "" {
		if err := st.pool.QueryRow(ctx, `SELECT revision FROM profiles WHERE id=$1`, sourceID).Scan(&profileRevision); err != nil {
			t.Fatal(err)
		}
	}
	var targetRequest any = map[string]any{}
	if manifest, err := st.resolveRelayManifestCandidate(ctx, relayTarget, sourceID, profileRevision, candidate); err == nil {
		metadata["routingHash"] = manifest.RelayGovernance.RoutingHash
		routingHash = manifest.RelayGovernance.RoutingHash
		targetRequest = map[string]any{"manifest": manifest}
		if kind == "relay_config_apply" {
			targetRequest = map[string]any{"manifest": manifest, "sourceKind": "relay_config_apply", "sourceId": metadata["revisionId"]}
		} else if kind == "policy_apply" {
			targetRequest = map[string]any{"manifest": manifest, "sourceKind": "relay_config_apply", "sourceId": manifest.RelayGovernance.RelayConfigurationRevisionID}
		}
	}
	id := uuid.NewString()
	body, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO operations(id,kind,status,source_id,metadata) VALUES($1,$2,'running',NULLIF($3,'')::uuid,$4)`, id, kind, sourceID, jsonText(body)); err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(map[string]any{"routingReloaded": true, "routingHash": routingHash})
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(targetRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,request,result,finished_at,governance_finalization_pending) VALUES($1,$2,$3,'succeeded',$4,$5,now(),true)`, uuid.NewString(), id, relayTarget.ID, jsonText(request), jsonText(result)); err != nil {
		t.Fatal(err)
	}
	return id
}

func testStringMap(value any) map[string]string {
	result := map[string]string{}
	switch values := value.(type) {
	case map[string]string:
		for key, item := range values {
			result[key] = item
		}
	case map[string]any:
		for key, item := range values {
			if text, ok := item.(string); ok {
				result[key] = text
			}
		}
	}
	return result
}

func governanceRoutingHash(t *testing.T, st *Store, operationID string) string {
	t.Helper()
	var hash string
	if err := st.pool.QueryRow(context.Background(), `SELECT metadata->>'routingHash' FROM operations WHERE id=$1`, operationID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return hash
}

func failGovernanceOperation(t *testing.T, st *Store, operationID string) {
	t.Helper()
	if err := st.FailGovernanceFinalization(context.Background(), operationID, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "test finalization conflict"}); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeRelayDefaultProfileRequiresExpectedPredecessorAndIsOneShot(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	first, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "claude-default-first", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "codex-default-second", ClientKind: "codex", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []domain.Profile{first, second} {
		operationID := succeededGovernanceOperation(t, st, "apply", profile.ID, map[string]any{"profileRevisionId": profile.CurrentRevisionID})
		if err := st.FinalizeProfilePublish(ctx, operationID, profile.ID, profile.CurrentRevisionID, governanceRoutingHash(t, st, operationID)); err != nil {
			t.Fatal(err)
		}
	}

	staleOperation := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"defaultProfileId": first.ID})
	if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_state SET default_profile_id=$1 WHERE singleton`, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeRelayDefaultProfile(ctx, staleOperation, first.ID); err != ErrConflict {
		t.Fatalf("stale Default Profile finalization returned %v, want conflict", err)
	}
	failGovernanceOperation(t, st, staleOperation)
	var defaultProfileID string
	if err := st.pool.QueryRow(ctx, `SELECT default_profile_id::text FROM relay_configuration_state WHERE singleton`).Scan(&defaultProfileID); err != nil {
		t.Fatal(err)
	}
	if defaultProfileID != second.ID {
		t.Fatalf("stale finalization replaced Default Profile with %s", defaultProfileID)
	}

	freshOperation := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"defaultProfileId": first.ID})
	if err := st.FinalizeRelayDefaultProfile(ctx, freshOperation, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeRelayDefaultProfile(ctx, freshOperation, first.ID); err != ErrConflict {
		t.Fatalf("replayed Default Profile finalization returned %v, want conflict", err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT default_profile_id::text FROM relay_configuration_state WHERE singleton`).Scan(&defaultProfileID); err != nil {
		t.Fatal(err)
	}
	if defaultProfileID != first.ID {
		t.Fatalf("fresh finalization stored Default Profile %s, want %s", defaultProfileID, first.ID)
	}
}

func TestRelayConfigurationFinalizerRejectsASecondOperationWithStalePredecessor(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "relay-predecessor", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, Metadata: map[string]any{"candidate": "first"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{Revision: first.Revision, MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, Metadata: map[string]any{"candidate": "second"}})
	if err != nil {
		t.Fatal(err)
	}
	var predecessor string
	if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&predecessor); err != nil {
		t.Fatal(err)
	}
	firstOperation := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": first.ID, "routingHash": strings.Repeat("a", 64)})
	var routingValid, manifestValid bool
	var manifestJSON []byte
	if err := st.pool.QueryRow(ctx, `SELECT validate_routing_bundle_v1(ot.request->'manifest'->'relayGovernance'->'routingBundle'),validate_desired_manifest(ot.request->'manifest'),ot.request->'manifest' FROM operation_targets ot WHERE ot.operation_id=$1`, firstOperation).Scan(&routingValid, &manifestValid, &manifestJSON); err != nil {
		t.Fatal(err)
	}
	if !routingValid || !manifestValid {
		t.Fatalf("operation manifest routingValid=%v manifestValid=%v body=%s", routingValid, manifestValid, manifestJSON)
	}
	if err := st.FinalizeRelayConfigurationApply(ctx, firstOperation, first.ID, governanceRoutingHash(t, st, firstOperation)); err != nil {
		t.Fatal(err)
	}
	secondOperation := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": second.ID, "routingHash": strings.Repeat("b", 64), "expectedAppliedRelayConfigurationRevisionId": predecessor})
	if err := st.FinalizeRelayConfigurationApply(ctx, secondOperation, second.ID, governanceRoutingHash(t, st, secondOperation)); err != ErrConflict {
		t.Fatalf("stale Relay Configuration operation returned %v, want conflict", err)
	}
}

func TestGovernanceApplyOperationPreparationIsAtomicWithFinalization(t *testing.T) {
	t.Run("relay configuration", func(t *testing.T) {
		ctx := context.Background()
		st := newIntegrationStore(t, true)
		if err := st.BootstrapEnvironment(ctx, "atomic-relay-host", "runner", "UTC", 6276); err != nil {
			t.Fatal(err)
		}
		server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "atomic-relay-preparation", Transport: "http", URL: "https://example.invalid/mcp"})
		if err != nil {
			t.Fatal(err)
		}
		first, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, Metadata: map[string]any{"candidate": "first"}})
		if err != nil {
			t.Fatal(err)
		}
		second, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{Revision: first.Revision, MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, Metadata: map[string]any{"candidate": "second"}})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := st.PrepareRelayConfigurationApply(ctx, second.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		firstOperation := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": first.ID})

		operation := createGovernanceOperationAcrossFinalization(t, st, func(createStore *Store) (domain.Operation, error) {
			return createStore.CreateRelayConfigurationApplyOperation(ctx, second.ID, nil, strings.Repeat("a", 64), prepared.RoutingHash, "atomic-relay-preparation")
		}, func() error {
			return st.FinalizeRelayConfigurationApply(ctx, firstOperation, first.ID, governanceRoutingHash(t, st, firstOperation))
		})
		if got := stringMetadata(operation.Metadata, "expectedAppliedRelayConfigurationRevisionId"); got != first.ID {
			t.Fatalf("operation predecessor=%s, want finalized revision %s", got, first.ID)
		}
	})

	t.Run("global policy", func(t *testing.T) {
		ctx := context.Background()
		st := newIntegrationStore(t, true)
		first, err := st.SaveGlobalPolicy(ctx, GlobalPolicyInput{Revision: 1, CatalogVersion: 1, ExplicitOverrides: map[string]string{}, UnclassifiedMutating: "deny", ReviewedReadOnly: "allow"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := st.SaveGlobalPolicy(ctx, GlobalPolicyInput{Revision: first.Revision, CatalogVersion: 1, ExplicitOverrides: map[string]string{}, UnclassifiedMutating: "deny", ReviewedReadOnly: "confirm"})
		if err != nil {
			t.Fatal(err)
		}
		firstOperation := succeededGovernanceOperation(t, st, "policy_apply", "", map[string]any{"revisionId": first.ID})

		operation := createGovernanceOperationAcrossFinalization(t, st, func(createStore *Store) (domain.Operation, error) {
			return createStore.CreateGlobalPolicyApplyOperation(ctx, second.ID, strings.Repeat("a", 64), "atomic-policy-preparation")
		}, func() error {
			return st.FinalizeGlobalPolicyApply(ctx, firstOperation, first.ID)
		})
		if got := stringMetadata(operation.Metadata, "expectedAppliedGlobalPolicyRevisionId"); got != first.ID {
			t.Fatalf("operation predecessor=%s, want finalized revision %s", got, first.ID)
		}
	})
}

func TestGovernanceApplyOperationIdempotencyReplaysActiveRequest(t *testing.T) {
	t.Run("relay configuration", func(t *testing.T) {
		ctx := context.Background()
		st := newIntegrationStore(t, true)
		if err := st.BootstrapEnvironment(ctx, "relay-idempotency-host", "runner", "UTC", 6276); err != nil {
			t.Fatal(err)
		}
		draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{Metadata: map[string]any{"candidate": "idempotent"}})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := st.PrepareRelayConfigurationApply(ctx, draft.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		first, err := st.CreateRelayConfigurationApplyOperation(ctx, draft.ID, nil, strings.Repeat("a", 64), prepared.RoutingHash, "relay-idempotency")
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := st.CreateRelayConfigurationApplyOperation(ctx, draft.ID, nil, strings.Repeat("a", 64), prepared.RoutingHash, "relay-idempotency")
		if err != nil {
			t.Fatal(err)
		}
		if replayed.ID != first.ID {
			t.Fatalf("relay replay operation=%s, want %s", replayed.ID, first.ID)
		}
		if _, err := st.CreateRelayConfigurationApplyOperation(ctx, draft.ID, nil, strings.Repeat("b", 64), prepared.RoutingHash, "relay-idempotency"); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("changed Relay request returned %v, want idempotency conflict", err)
		}
	})

	t.Run("global policy", func(t *testing.T) {
		ctx := context.Background()
		st := newIntegrationStore(t, true)
		if err := st.BootstrapEnvironment(ctx, "policy-idempotency-host", "runner", "UTC", 6276); err != nil {
			t.Fatal(err)
		}
		draft, err := st.SaveGlobalPolicy(ctx, GlobalPolicyInput{Revision: 1, CatalogVersion: 1, ExplicitOverrides: map[string]string{}, UnclassifiedMutating: "deny", ReviewedReadOnly: "allow"})
		if err != nil {
			t.Fatal(err)
		}
		first, err := st.CreateGlobalPolicyApplyOperation(ctx, draft.ID, strings.Repeat("a", 64), "policy-idempotency")
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := st.CreateGlobalPolicyApplyOperation(ctx, draft.ID, strings.Repeat("a", 64), "policy-idempotency")
		if err != nil {
			t.Fatal(err)
		}
		if replayed.ID != first.ID {
			t.Fatalf("policy replay operation=%s, want %s", replayed.ID, first.ID)
		}
		if _, err := st.CreateGlobalPolicyApplyOperation(ctx, draft.ID, strings.Repeat("b", 64), "policy-idempotency"); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("changed Policy request returned %v, want idempotency conflict", err)
		}
	})
}

func TestGovernanceApplyOperationIdempotencyReplaysConcurrentRequest(t *testing.T) {
	t.Run("relay configuration", func(t *testing.T) {
		ctx := context.Background()
		st := newIntegrationStore(t, true)
		if err := st.BootstrapEnvironment(ctx, "relay-concurrent-idempotency-host", "runner", "UTC", 6276); err != nil {
			t.Fatal(err)
		}
		draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{Metadata: map[string]any{"candidate": "concurrent-idempotent"}})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := st.PrepareRelayConfigurationApply(ctx, draft.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		operations := createConcurrentGovernanceOperations(t, st, func(createStore *Store) (domain.Operation, error) {
			return createStore.CreateRelayConfigurationApplyOperation(ctx, draft.ID, nil, strings.Repeat("a", 64), prepared.RoutingHash, "relay-concurrent-idempotency")
		})
		if operations[0].ID != operations[1].ID {
			t.Fatalf("concurrent Relay operations=%s,%s, want one replayed operation", operations[0].ID, operations[1].ID)
		}
	})

	t.Run("global policy", func(t *testing.T) {
		ctx := context.Background()
		st := newIntegrationStore(t, true)
		if err := st.BootstrapEnvironment(ctx, "policy-concurrent-idempotency-host", "runner", "UTC", 6276); err != nil {
			t.Fatal(err)
		}
		draft, err := st.SaveGlobalPolicy(ctx, GlobalPolicyInput{Revision: 1, CatalogVersion: 1, ExplicitOverrides: map[string]string{}, UnclassifiedMutating: "deny", ReviewedReadOnly: "allow"})
		if err != nil {
			t.Fatal(err)
		}
		operations := createConcurrentGovernanceOperations(t, st, func(createStore *Store) (domain.Operation, error) {
			return createStore.CreateGlobalPolicyApplyOperation(ctx, draft.ID, strings.Repeat("a", 64), "policy-concurrent-idempotency")
		})
		if operations[0].ID != operations[1].ID {
			t.Fatalf("concurrent Policy operations=%s,%s, want one replayed operation", operations[0].ID, operations[1].ID)
		}
	})
}

func createConcurrentGovernanceOperations(t *testing.T, st *Store, create func(*Store) (domain.Operation, error)) [2]domain.Operation {
	t.Helper()
	ctx := context.Background()
	barrierPool, firstStore, secondStore, closePools := integrationLockOrderStores(t, st)
	defer closePools()
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	barrier, err := barrierPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = barrier.Rollback(ctx) }()
	var nodeID string
	if err := barrier.QueryRow(ctx, `SELECT id::text FROM nodes WHERE id=$1 FOR UPDATE`, relayTarget.NodeID).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		operation domain.Operation
		err       error
	}
	results := make(chan result, 2)
	for _, createStore := range []*Store{firstStore, secondStore} {
		go func(createStore *Store) {
			operation, err := create(createStore)
			results <- result{operation: operation, err: err}
		}(createStore)
	}
	waitForBackendLock(t, st, "toolhub-finish-lock-order")
	waitForBackendLock(t, st, "toolhub-archive-lock-order")
	if err := barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var operations [2]domain.Operation
	for index := range operations {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			operations[index] = result.operation
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent governance operation creation did not resume")
		}
	}
	return operations
}

func createGovernanceOperationAcrossFinalization(t *testing.T, st *Store, create func(*Store) (domain.Operation, error), finalize func() error) domain.Operation {
	t.Helper()
	ctx := context.Background()
	barrierPool, createStore, _, closePools := integrationLockOrderStores(t, st)
	defer closePools()
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	barrier, err := barrierPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = barrier.Rollback(ctx) }()
	var nodeID string
	if err := barrier.QueryRow(ctx, `SELECT id::text FROM nodes WHERE id=$1 FOR UPDATE`, relayTarget.NodeID).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		operation domain.Operation
		err       error
	}
	created := make(chan result, 1)
	go func() {
		operation, err := create(createStore)
		created <- result{operation: operation, err: err}
	}()
	waitForBackendLock(t, st, "toolhub-finish-lock-order")
	if err := finalize(); err != nil {
		t.Fatal(err)
	}
	if err := barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-created:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result.operation
	case <-time.After(5 * time.Second):
		t.Fatal("governance operation creation did not resume")
		return domain.Operation{}
	}
}

func TestGovernanceReloadEvidenceCanOnlyBeFinalizedOnce(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	var appliedRevisionID string
	if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&appliedRevisionID); err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": appliedRevisionID, "routingHash": strings.Repeat("a", 64)})
	routingHash := governanceRoutingHash(t, st, opID)
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, appliedRevisionID, routingHash); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, appliedRevisionID, routingHash); err != ErrConflict {
		t.Fatalf("reused governance reload evidence returned %v, want conflict", err)
	}
}

func TestGovernancePointerFinalizerRequiresMatchingRelayReload(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "reload-proof", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	routingHash := strings.Repeat("a", 64)
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": draft.ID, "routingHash": routingHash})
	routingHash = governanceRoutingHash(t, st, opID)
	wrongResult, err := json.Marshal(map[string]any{"routingReloaded": true, "routingHash": strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE operation_targets SET result=$2 WHERE operation_id=$1`, opID, jsonText(wrongResult)); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, draft.ID, routingHash); err != ErrConflict {
		t.Fatalf("finalizer accepted mismatched reload evidence: %v", err)
	}
	var applied string
	if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied == draft.ID {
		t.Fatal("relay pointer advanced after mismatched reload evidence")
	}
}

func TestGovernanceFinalizerRejectsMismatchedCandidateManifest(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "candidate-proof", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": draft.ID})
	if _, err := st.pool.Exec(ctx, `UPDATE operation_targets SET request=jsonb_set(request,'{manifest,relayGovernance,routingHash}',to_jsonb($2::text),false) WHERE operation_id=$1`, opID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, draft.ID, governanceRoutingHash(t, st, opID)); err != ErrConflict {
		t.Fatalf("finalizer accepted a tampered candidate manifest: %v", err)
	}
}

func TestRelayConfigurationDraftAndAppliedPointers(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "relay-config", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, Metadata: map[string]any{"source": "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Revision != 2 || len(draft.MCPServers) != 1 {
		t.Fatalf("draft=%+v", draft)
	}
	var current, applied int64
	if err := st.pool.QueryRow(ctx, `SELECT r.revision,ar.revision FROM relay_configuration_state s JOIN relay_configuration_revisions r ON r.id=s.current_revision_id JOIN relay_configuration_revisions ar ON ar.id=s.applied_revision_id WHERE s.singleton`).Scan(&current, &applied); err != nil {
		t.Fatal(err)
	}
	if current != 2 || applied != 1 {
		t.Fatalf("pointers=%d/%d", current, applied)
	}
	same, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{
		Revision: draft.Revision, MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, Metadata: map[string]any{"source": "test"},
	})
	if err != nil || same.ID != draft.ID {
		t.Fatalf("same draft=%+v err=%v", same, err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": draft.ID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, draft.ID, governanceRoutingHash(t, st, opID)); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT r.revision,ar.revision FROM relay_configuration_state s JOIN relay_configuration_revisions r ON r.id=s.current_revision_id JOIN relay_configuration_revisions ar ON ar.id=s.applied_revision_id WHERE s.singleton`).Scan(&current, &applied); err != nil {
		t.Fatal(err)
	}
	if current != 2 || applied != 2 {
		t.Fatalf("applied pointers=%d/%d", current, applied)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_revisions SET metadata='{}' WHERE id=$1`, draft.ID); err == nil {
		t.Fatal("relay configuration revision accepted mutation")
	}
}

func TestPrepareAffectedProfileUpdatesCreatesMCPRevisionCandidate(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, contract, profile, _ := setupPublishedRelayProfile(t, st, "candidate-update")
	publishedRevisionID := profile.CurrentRevisionID

	server, err := st.SaveMCPServer(ctx, server.ID, MCPInput{Name: server.Name, Description: "revision two", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	affected, err := st.PrepareAffectedProfileUpdates(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 1 || affected[0] != profile.ID {
		t.Fatalf("affected Profiles=%v", affected)
	}
	profile, err = st.Profile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.CurrentRevisionID == publishedRevisionID {
		t.Fatal("MCP update did not create a candidate Profile revision")
	}
	var pinnedMCPRevisionID, acceptedContractRevisionID string
	if err := st.pool.QueryRow(ctx, `SELECT mcp_revision_id::text,accepted_contract_revision_id::text FROM profile_revision_mcp_governance WHERE profile_revision_id=$1 AND server_id=$2`, profile.CurrentRevisionID, server.ID).Scan(&pinnedMCPRevisionID, &acceptedContractRevisionID); err != nil {
		t.Fatal(err)
	}
	if pinnedMCPRevisionID != server.CurrentRevisionID || acceptedContractRevisionID != contract.Revision.ID {
		t.Fatalf("candidate pins MCP=%s Contract=%s", pinnedMCPRevisionID, acceptedContractRevisionID)
	}
	var stillPublished string
	if err := st.pool.QueryRow(ctx, `SELECT profile_revision_id::text FROM published_profiles WHERE profile_id=$1`, profile.ID).Scan(&stillPublished); err != nil {
		t.Fatal(err)
	}
	if stillPublished != publishedRevisionID {
		t.Fatalf("prepare advanced Published pointer to %s", stillPublished)
	}
	bundle, _, err := st.RenderCandidateRoutingBundle(ctx, RoutingBundleCandidate{
		RelayConfigurationRevisionID: draft.ID,
		PublishedProfileRevisions:    map[string]string{profile.ID: profile.CurrentRevisionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Profiles) != 1 || bundle.Profiles[0].ProfileRevisionID != profile.CurrentRevisionID || bundle.Profiles[0].Servers[0].MCPConfigRevisionID != server.CurrentRevisionID {
		t.Fatalf("combined candidate bundle=%+v", bundle.Profiles)
	}
}

func TestPrepareAffectedProfileUpdatesIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, _, profile, _ := setupPublishedRelayProfile(t, st, "candidate-idempotent")
	server, err := st.SaveMCPServer(ctx, server.ID, MCPInput{Name: server.Name, Description: "revision two", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	firstAffected, err := st.PrepareAffectedProfileUpdates(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.Profile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondAffected, err := st.PrepareAffectedProfileUpdates(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.Profile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstAffected) != 1 || len(secondAffected) != 1 || firstAffected[0] != profile.ID || secondAffected[0] != profile.ID {
		t.Fatalf("affected Profiles first=%v second=%v", firstAffected, secondAffected)
	}
	if second.CurrentRevisionID != first.CurrentRevisionID || second.Revision != first.Revision {
		t.Fatalf("retry created another candidate first=%s/r%d second=%s/r%d", first.CurrentRevisionID, first.Revision, second.CurrentRevisionID, second.Revision)
	}
}

func TestPrepareAffectedProfileUpdatesRejectsCurrentDraft(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, _, profile, _ := setupPublishedRelayProfile(t, st, "published-baseline")
	publishedRevisionID := profile.CurrentRevisionID

	draftProfile, err := st.SaveProfile(ctx, profile.ID, ProfileInput{
		Name: profile.Name, Description: "unpublished unrelated draft", Revision: profile.Revision,
		ClientKind: profile.ClientKind, Category: profile.Category, Variant: profile.Variant, MigrationState: profile.MigrationState,
		MCPServerIDs: profile.MCPServerIDs, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: acceptedContractRevisionID(t, st, server.ID), VisibilityMode: "all_accepted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if draftProfile.CurrentRevisionID == publishedRevisionID {
		t.Fatal("unpublished draft did not create a new Profile revision")
	}
	server, err = st.SaveMCPServer(ctx, server.ID, MCPInput{Name: server.Name, Description: "relay revision two", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	relayDraft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PrepareAffectedProfileUpdates(ctx, relayDraft.ID); err != ErrConflict {
		t.Fatalf("prepare with current draft returned %v, want conflict", err)
	}
	current, err := st.Profile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentRevisionID != draftProfile.CurrentRevisionID || current.Description != draftProfile.Description {
		t.Fatalf("prepare replaced current draft: got=%+v want revision=%s description=%q", current, draftProfile.CurrentRevisionID, draftProfile.Description)
	}
	var stillPublished string
	if err := st.pool.QueryRow(ctx, `SELECT profile_revision_id::text FROM published_profiles WHERE profile_id=$1`, profile.ID).Scan(&stillPublished); err != nil {
		t.Fatal(err)
	}
	if stillPublished != publishedRevisionID {
		t.Fatalf("prepare advanced Published pointer to %s", stillPublished)
	}
}

func acceptedContractRevisionID(t *testing.T, st *Store, serverID string) string {
	t.Helper()
	var revisionID string
	if err := st.pool.QueryRow(context.Background(), `SELECT accepted_revision_id::text FROM mcp_contract_state WHERE server_id=$1`, serverID).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	return revisionID
}

func TestPrepareAffectedProfileUpdatesRejectsPublishedServerRemoval(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	_, _, profile, _ := setupPublishedRelayProfile(t, st, "server-removal")
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PrepareAffectedProfileUpdates(ctx, draft.ID); err != ErrConflict {
		t.Fatalf("published server removal returned %v, want conflict", err)
	}
	current, err := st.Profile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentRevisionID != profile.CurrentRevisionID {
		t.Fatalf("rejected server removal created Profile revision %s", current.CurrentRevisionID)
	}
}

func TestFinalizeRelayConfigurationApplyAtomicallyAdvancesAffectedProfilesAndSnapshot(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, _, profile, appliedRelayID := setupPublishedRelayProfile(t, st, "atomic-success")
	publishedRevisionID := profile.CurrentRevisionID
	server, err := st.SaveMCPServer(ctx, server.ID, MCPInput{Name: server.Name, Description: "revision two", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PrepareAffectedProfileUpdates(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	profile, err = st.Profile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{
		"revisionId":                        draft.ID,
		"affectedProfileRevisions":          map[string]string{profile.ID: profile.CurrentRevisionID},
		"expectedPublishedProfileRevisions": map[string]string{profile.ID: publishedRevisionID},
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", metadata)
	routingHash := governanceRoutingHash(t, st, opID)
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, draft.ID, routingHash); err != nil {
		t.Fatal(err)
	}
	var applied, published string
	if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT profile_revision_id::text FROM published_profiles WHERE profile_id=$1`, profile.ID).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if applied != draft.ID || published != profile.CurrentRevisionID || applied == appliedRelayID {
		t.Fatalf("final pointers relay=%s profile=%s", applied, published)
	}
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	snapshot, manifest, err := st.ActiveDesiredManifest(ctx, relayTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SourceKind != "relay_config_apply" || snapshot.SourceID != draft.ID || manifest.RelayGovernance == nil || manifest.RelayGovernance.RoutingHash != routingHash {
		t.Fatalf("active relay snapshot=%+v manifest=%+v", snapshot, manifest.RelayGovernance)
	}
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, draft.ID, routingHash); err != ErrConflict {
		t.Fatalf("replayed Relay Apply returned %v, want conflict", err)
	}
}

func TestFinalizeRelayConfigurationApplyStaleProfilePredecessorChangesNothing(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, _, profile, appliedRelayID := setupPublishedRelayProfile(t, st, "atomic-stale")
	publishedRevisionID := profile.CurrentRevisionID
	server, err := st.SaveMCPServer(ctx, server.ID, MCPInput{Name: server.Name, Description: "revision two", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PrepareAffectedProfileUpdates(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	profile, err = st.Profile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{
		"revisionId":                        draft.ID,
		"affectedProfileRevisions":          map[string]string{profile.ID: profile.CurrentRevisionID},
		"expectedPublishedProfileRevisions": map[string]string{profile.ID: publishedRevisionID},
	})
	if _, err := st.pool.Exec(ctx, `UPDATE published_profiles SET profile_revision_id=$2 WHERE profile_id=$1`, profile.ID, profile.CurrentRevisionID); err != nil {
		t.Fatal(err)
	}
	var snapshotsBefore int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM desired_snapshots`).Scan(&snapshotsBefore); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, draft.ID, governanceRoutingHash(t, st, opID)); err != ErrConflict {
		t.Fatalf("stale Published predecessor returned %v, want conflict", err)
	}
	var applied string
	var snapshotsAfter int
	if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM desired_snapshots`).Scan(&snapshotsAfter); err != nil {
		t.Fatal(err)
	}
	if applied != appliedRelayID || snapshotsAfter != snapshotsBefore {
		t.Fatalf("failed finalization changed relay=%s snapshots=%d/%d", applied, snapshotsBefore, snapshotsAfter)
	}
}

func setupPublishedRelayProfile(t *testing.T, st *Store, suffix string) (domain.MCPServer, ContractObservationResult, domain.Profile, string) {
	t.Helper()
	ctx := context.Background()
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "relay-" + suffix, Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, contract.Revision.ID); err != nil {
		t.Fatal(err)
	}
	relay, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": relay.ID})
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, relay.ID, governanceRoutingHash(t, st, opID)); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-" + suffix, ClientKind: "claude", Category: "coding",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: contract.Revision.ID, VisibilityMode: "all_accepted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	opID = succeededGovernanceOperation(t, st, "apply", profile.ID, map[string]any{"profileRevisionId": profile.CurrentRevisionID})
	if err := st.FinalizeProfilePublish(ctx, opID, profile.ID, profile.CurrentRevisionID, governanceRoutingHash(t, st, opID)); err != nil {
		t.Fatal(err)
	}
	return server, contract, profile, relay.ID
}

func TestDirectRelayConfigurationApplyCannotAdvanceTheAppliedPointer(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "relay-guard", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyRelayConfiguration(ctx, draft.ID); err == nil {
		t.Fatal("direct relay apply advanced a pointer without a successful reload operation")
	}
	var applied string
	if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied == draft.ID {
		t.Fatal("applied relay pointer advanced without an operation")
	}
}

func TestRoutingBundlePreservesPublishedProfilesAndIsCanonical(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "routing-server", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, contract.Revision.ID); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "claude-routing", ClientKind: "claude", Category: "coding", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: contract.Revision.ID, VisibilityMode: "all_accepted"}}})
	if err != nil {
		t.Fatal(err)
	}
	relayDraft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": relayDraft.ID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, relayDraft.ID, governanceRoutingHash(t, st, opID)); err != nil {
		t.Fatal(err)
	}
	opID = succeededGovernanceOperation(t, st, "apply", profile.ID, map[string]any{"profileRevisionId": profile.CurrentRevisionID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeProfilePublish(ctx, opID, profile.ID, profile.CurrentRevisionID, governanceRoutingHash(t, st, opID)); err != nil {
		t.Fatal(err)
	}
	bundle, hash, err := st.RenderRoutingBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Profiles) != 1 || len(bundle.Servers) != 1 || hash == "" {
		t.Fatalf("bundle=%+v hash=%q", bundle, hash)
	}
	if bundle.Profiles[0].ProfileRevisionID != profile.CurrentRevisionID || bundle.Profiles[0].ClientKind != "claude" {
		t.Fatalf("published profile=%+v", bundle.Profiles[0])
	}
	body, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var strict bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(body, &strict); err != nil {
		t.Fatalf("rendered routing bundle is not consumable by Bridge DTO: %v\n%s", err, body)
	}
	_, secondHash, err := st.RenderRoutingBundle(ctx)
	if err != nil || secondHash != hash {
		t.Fatalf("bundle hash changed: %q/%q err=%v", hash, secondHash, err)
	}
	if _, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true, Annotations: map[string]any{"destructiveHint": true}}}}); err != nil {
		t.Fatal(err)
	}
	bundle, _, err = st.RenderRoutingBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Servers[0].Tools) != 1 || !bundle.Servers[0].Tools[0].Paused {
		t.Fatalf("later incompatible observation did not pause accepted tool: %+v", bundle.Servers[0].Tools)
	}
}

func TestProfileRelayManifestUsesCandidateProfileAndCompleteRelayConfiguration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	servers := make([]domain.MCPServer, 0, 2)
	contracts := make([]ContractObservationResult, 0, 2)
	for _, name := range []string{"candidate-one", "candidate-two"} {
		server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: name, Transport: "http", URL: "https://example.invalid/" + name})
		if err != nil {
			t.Fatal(err)
		}
		contract, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AcceptContract(ctx, server.ID, contract.Revision.ID); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, server)
		contracts = append(contracts, contract)
	}
	relay, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{
		MCPServerIDs: []string{servers[0].ID, servers[1].ID},
		MCPRevisionIDs: map[string]string{
			servers[0].ID: servers[0].CurrentRevisionID,
			servers[1].ID: servers[1].CurrentRevisionID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": relay.ID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, relay.ID, governanceRoutingHash(t, st, opID)); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-candidate", ClientKind: "claude", Category: "coding",
		MCPServerIDs: []string{servers[0].ID}, MCPRevisionIDs: map[string]string{servers[0].ID: servers[0].CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: servers[0].ID, MCPRevisionID: servers[0].CurrentRevisionID, AcceptedContractRevisionID: contracts[0].Revision.ID, VisibilityMode: "all_accepted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	manifest, err := st.ResolveProfileManifest(ctx, profile.ID, relayTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.MCPServers) != 2 {
		t.Fatalf("shared relay manifest has %d servers, want complete Relay Configuration of 2", len(manifest.MCPServers))
	}
	if manifest.RelayGovernance == nil {
		t.Fatal("shared relay manifest is missing routing governance")
	}
	var bundle bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(manifest.RelayGovernance.RoutingBundle, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Profiles) != 1 || bundle.Profiles[0].ProfileID != profile.ID || bundle.Profiles[0].ProfileRevisionID != profile.CurrentRevisionID {
		t.Fatalf("candidate Profile is not pinned in preflight routing bundle: %+v", bundle.Profiles)
	}
}

func TestRoutingBundleHidesNewlyAcceptedToolsWithoutExplicitProfileOverride(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "new-hidden", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, first.Revision.ID); err != nil {
		t.Fatal(err)
	}
	second, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}, {Name: "write_item", Mutating: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, second.Revision.ID); err != nil {
		t.Fatal(err)
	}
	relay, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": relay.ID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, relay.ID, governanceRoutingHash(t, st, opID)); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-new-hidden", ClientKind: "claude", Category: "coding",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: second.Revision.ID, VisibilityMode: "all_accepted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	opID = succeededGovernanceOperation(t, st, "apply", profile.ID, map[string]any{"profileRevisionId": profile.CurrentRevisionID, "routingHash": strings.Repeat("b", 64)})
	if err := st.FinalizeProfilePublish(ctx, opID, profile.ID, profile.CurrentRevisionID, governanceRoutingHash(t, st, opID)); err != nil {
		t.Fatal(err)
	}
	bundle, _, err := st.RenderRoutingBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var newToolID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM mcp_tools WHERE server_id=$1 AND name='write_item'`, server.ID).Scan(&newToolID); err != nil {
		t.Fatal(err)
	}
	overrides := bundle.Profiles[0].Servers[0].ToolOverrides
	if len(overrides) != 1 || overrides[0].ToolID != newToolID || overrides[0].Visible {
		t.Fatalf("new hidden overrides=%+v", overrides)
	}
}

func TestClassifyRoutingToolUsesConfiguredGlobalFallbacks(t *testing.T) {
	explicitReadOnlyToolID := uuid.NewString()
	global := domain.GlobalPolicyRevision{
		ExplicitOverrides:    map[string]string{explicitReadOnlyToolID: policy.DecisionAllow},
		UnclassifiedMutating: policy.DecisionDeny,
		ReviewedReadOnly:     policy.DecisionDeny,
	}
	tests := []struct {
		name string
		tool RoutingTool
		want string
	}{
		{name: "reviewed read-only", tool: RoutingTool{ToolID: uuid.NewString(), Name: "read_item", InputSchema: map[string]any{}, OutputSchema: map[string]any{}, Annotations: map[string]any{"readOnlyHint": true}}, want: policy.DecisionDeny},
		{name: "explicit read-only override", tool: RoutingTool{ToolID: explicitReadOnlyToolID, Name: "read_public_item", InputSchema: map[string]any{}, OutputSchema: map[string]any{}, Annotations: map[string]any{"readOnlyHint": true}}, want: policy.DecisionAllow},
		{name: "unclassified mutating", tool: RoutingTool{ToolID: uuid.NewString(), Name: "sync_records", InputSchema: map[string]any{}, OutputSchema: map[string]any{}, Annotations: map[string]any{"mutatingHint": true}}, want: policy.DecisionDeny},
		{name: "classified destructive", tool: RoutingTool{ToolID: uuid.NewString(), Name: "delete_record", InputSchema: map[string]any{}, OutputSchema: map[string]any{}, Annotations: map[string]any{"mutatingHint": true}}, want: policy.DecisionConfirm},
		{name: "unknown", tool: RoutingTool{ToolID: uuid.NewString(), Name: "inspect_record", InputSchema: map[string]any{}, OutputSchema: map[string]any{}, Annotations: map[string]any{}}, want: policy.DecisionConfirm},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, _ := classifyRoutingTool(global, test.tool)
			if decision != test.want {
				t.Fatalf("decision=%q want %q", decision, test.want)
			}
		})
	}
}

func TestRoutingBundlePauseTracksLatestContractAgainstAcceptedRevision(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "accepted-pause", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{
		Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`), ReadOnlyHint: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, accepted.Revision.ID); err != nil {
		t.Fatal(err)
	}
	relay, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": relay.ID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, relay.ID, governanceRoutingHash(t, st, opID)); err != nil {
		t.Fatal(err)
	}
	latest, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{
		Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}}}`), ReadOnlyHint: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	bundle, _, err := st.RenderRoutingBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Servers) != 1 || len(bundle.Servers[0].Tools) != 1 || !bundle.Servers[0].Tools[0].Paused {
		t.Fatalf("incompatible latest contract was not paused: %+v", bundle.Servers)
	}
	if err := st.AcceptContract(ctx, server.ID, latest.Revision.ID); err != nil {
		t.Fatal(err)
	}
	bundle, _, err = st.RenderRoutingBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Servers[0].Tools[0].Paused {
		t.Fatalf("accepted latest contract remained paused: %+v", bundle.Servers[0].Tools[0])
	}
	if _, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{}}); err != nil {
		t.Fatal(err)
	}
	bundle, _, err = st.RenderRoutingBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.Servers[0].Tools[0].Paused {
		t.Fatalf("tool missing from latest contract was not paused: %+v", bundle.Servers[0].Tools[0])
	}
}

func TestRelaySnapshotAcceptsPayloadLikeJSONSchemaPropertyNames(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "schema-properties", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{
		Name: "submit", InputSchema: json.RawMessage(`{"type":"object","properties":{"arguments":{"type":"string"},"result":{"type":"string"},"prompt":{"type":"string"}}}`), ReadOnlyHint: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, contract.Revision.ID); err != nil {
		t.Fatal(err)
	}
	relay, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	operationID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": relay.ID})
	if err := st.FinalizeRelayConfigurationApply(ctx, operationID, relay.ID, governanceRoutingHash(t, st, operationID)); err != nil {
		t.Fatal(err)
	}
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	_, manifest, err := st.ActiveDesiredManifest(ctx, relayTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RelayGovernance == nil {
		t.Fatal("relay snapshot omitted governance after JSON Schema property validation")
	}
}
