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
