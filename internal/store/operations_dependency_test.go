package store

import (
	"context"
	"testing"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func TestOperationTargetDependencyGatesClaimAndPropagatesFailure(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "dependency-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	skillTarget := integrationTarget(t, st, "local/claude")
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	operation, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "apply", Request: map[string]any{"profile": "candidate"}, TargetIDs: []string{skillTarget.ID, relayTarget.ID}, TargetRequests: map[string]any{skillTarget.ID: map[string]any{"step": "skills"}, relayTarget.ID: map[string]any{"step": "relay"}}, TargetDependencies: map[string]string{relayTarget.ID: skillTarget.ID}})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimOperationTarget(ctx)
	if err != nil || claimed.Target.ID != skillTarget.ID {
		t.Fatalf("first claim=%+v err=%v", claimed, err)
	}
	if err := st.FinishOperationTarget(ctx, claimed.OperationTarget.ID, bridgeprotocol.OperationFailed, bridgeprotocol.TargetResult{}, nil); err != nil {
		t.Fatal(err)
	}
	var relayStatus, operationStatus string
	if err := st.pool.QueryRow(ctx, `SELECT ot.status,o.status FROM operation_targets ot JOIN operations o ON o.id=ot.operation_id WHERE ot.operation_id=$1 AND ot.target_id=$2`, operation.ID, relayTarget.ID).Scan(&relayStatus, &operationStatus); err != nil {
		t.Fatal(err)
	}
	if relayStatus != bridgeprotocol.OperationFailed || operationStatus != bridgeprotocol.OperationFailed {
		t.Fatalf("dependency statuses=%s/%s", relayStatus, operationStatus)
	}
	if _, err := st.ClaimOperationTarget(ctx); err == nil {
		t.Fatal("dependency failure left a dispatchable target")
	}
}

func TestFailedGovernanceTargetReleasesFinalizationOwnership(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "ownership-failure-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	target := integrationTarget(t, st, "local/claude")
	operation, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "apply", TargetIDs: []string{target.ID}, TargetRequests: map[string]any{target.ID: map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE operation_targets SET governance_finalization_pending=true WHERE operation_id=$1`, operation.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishOperationTarget(ctx, claimed.OperationTarget.ID, bridgeprotocol.OperationFailed, bridgeprotocol.TargetResult{}, nil); err != nil {
		t.Fatal(err)
	}
	var pending bool
	if err := st.pool.QueryRow(ctx, `SELECT governance_finalization_pending FROM operation_targets WHERE id=$1`, claimed.OperationTarget.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("failed governance target retained finalization ownership")
	}
	if _, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "scan", TargetIDs: []string{target.ID}, TargetRequests: map[string]any{target.ID: map[string]any{}}}); err != nil {
		t.Fatalf("failed governance target kept target blocked: %v", err)
	}
}

func TestFailGovernanceFinalizationMarksOperationFailedAndReleasesOwnership(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "finalization-failure-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	target := integrationTarget(t, st, "local/shared-relay")
	operation, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "apply", TargetIDs: []string{target.ID}, TargetRequests: map[string]any{target.ID: map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE operation_targets SET governance_finalization_pending=true WHERE operation_id=$1`, operation.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishOperationTarget(ctx, claimed.OperationTarget.ID, bridgeprotocol.OperationSucceeded, bridgeprotocol.TargetResult{}, nil); err != nil {
		t.Fatal(err)
	}
	finalizationError := &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Profile Apply finalization failed"}
	if err := st.FailGovernanceFinalization(ctx, operation.ID, finalizationError); err != nil {
		t.Fatal(err)
	}
	var status, errorCode, errorReason string
	var pending bool
	if err := st.pool.QueryRow(ctx, `SELECT o.status,o.error_code,o.error_reason,bool_or(ot.governance_finalization_pending) FROM operations o JOIN operation_targets ot ON ot.operation_id=o.id WHERE o.id=$1 GROUP BY o.id`, operation.ID).Scan(&status, &errorCode, &errorReason, &pending); err != nil {
		t.Fatal(err)
	}
	if status != bridgeprotocol.OperationFailed || errorCode != bridgeprotocol.ErrRevisionConflict || errorReason != finalizationError.Message || pending {
		t.Fatalf("finalization failure status=%s code=%s reason=%q pending=%v", status, errorCode, errorReason, pending)
	}
	if _, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "scan", TargetIDs: []string{target.ID}, TargetRequests: map[string]any{target.ID: map[string]any{}}}); err != nil {
		t.Fatalf("finalization failure retained target ownership: %v", err)
	}
}
