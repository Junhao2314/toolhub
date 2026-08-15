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
	if status != bridgeprotocol.OperationFailed || errorCode != bridgeprotocol.ErrRevisionConflict || errorReason != "Runtime revision conflicts with the request" || pending {
		t.Fatalf("finalization failure status=%s code=%s reason=%q pending=%v", status, errorCode, errorReason, pending)
	}
	if _, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "scan", TargetIDs: []string{target.ID}, TargetRequests: map[string]any{target.ID: map[string]any{}}}); err != nil {
		t.Fatalf("finalization failure retained target ownership: %v", err)
	}
}

func TestCancelProfileApplyReleasesFinalizationOwnership(t *testing.T) {
	for _, test := range []struct {
		name              string
		finishSkillTarget bool
		wantStatus        string
	}{
		{name: "fully queued", wantStatus: bridgeprotocol.OperationCancelled},
		{name: "skill target succeeded", finishSkillTarget: true, wantStatus: bridgeprotocol.OperationPartial},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			st := newIntegrationStore(t, true)
			if err := st.BootstrapEnvironment(ctx, "cancel-profile-apply-host", "runner", "UTC", 6276); err != nil {
				t.Fatal(err)
			}
			operation, skillTarget, relayTarget, _ := createProfileApplyOperationForLifecycleTest(t, st, "claude-cancel-"+strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
			if test.finishSkillTarget {
				claimed, err := st.ClaimOperationTarget(ctx)
				if err != nil || claimed.Target.ID != skillTarget.ID {
					t.Fatalf("claim Skill target=%+v err=%v", claimed, err)
				}
				if err := st.FinishOperationTarget(ctx, claimed.OperationTarget.ID, bridgeprotocol.OperationSucceeded, bridgeprotocol.TargetResult{}, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.CancelOperation(ctx, operation.ID); err != nil {
				t.Fatal(err)
			}
			var status string
			var pending int
			if err := st.pool.QueryRow(ctx, `SELECT status FROM operations WHERE id=$1`, operation.ID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM operation_targets WHERE operation_id=$1 AND governance_finalization_pending`, operation.ID).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if status != test.wantStatus || pending != 0 {
				t.Fatalf("cancelled Profile Apply status=%s pending=%d, want %s/0", status, pending, test.wantStatus)
			}
			if _, err := st.CreateOperation(ctx, CreateOperationInput{
				Kind:      "scan",
				TargetIDs: []string{skillTarget.ID, relayTarget.ID},
				TargetRequests: map[string]any{
					skillTarget.ID: map[string]any{},
					relayTarget.ID: map[string]any{},
				},
			}); err != nil {
				t.Fatalf("cancelled Profile Apply retained target ownership: %v", err)
			}
		})
	}
}

func TestRetryProfileApplyPreservesSuccessfulSkillEvidenceAndFinalization(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "retry-profile-apply-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	operation, skillTarget, relayTarget, routingHash := createProfileApplyOperationForLifecycleTest(t, st, "claude-retry-"+strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
	skillItem, err := st.ClaimOperationTarget(ctx)
	if err != nil || skillItem.Target.ID != skillTarget.ID {
		t.Fatalf("claim Skill target=%+v err=%v", skillItem, err)
	}
	if err := st.FinishOperationTarget(ctx, skillItem.OperationTarget.ID, bridgeprotocol.OperationSucceeded, bridgeprotocol.TargetResult{}, nil); err != nil {
		t.Fatal(err)
	}
	relayItem, err := st.ClaimOperationTarget(ctx)
	if err != nil || relayItem.Target.ID != relayTarget.ID {
		t.Fatalf("claim relay target=%+v err=%v", relayItem, err)
	}
	if err := st.FinishOperationTarget(ctx, relayItem.OperationTarget.ID, bridgeprotocol.OperationFailed, bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "relay reload failed"}); err != nil {
		t.Fatal(err)
	}

	retry, err := st.RetryFailedTargets(ctx, operation.ID, "profile-aware-retry")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := st.RetryFailedTargets(ctx, operation.ID, "profile-aware-retry")
	if err != nil || replayed.ID != retry.ID {
		t.Fatalf("idempotent retry=%s err=%v, want %s", replayed.ID, err, retry.ID)
	}
	var originalMetadata, retryMetadata map[string]any
	if err := json.Unmarshal(operation.Metadata, &originalMetadata); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(retry.Metadata, &retryMetadata); err != nil {
		t.Fatal(err)
	}
	if retry.SourceID != operation.SourceID || retryMetadata["profileRevisionId"] != originalMetadata["profileRevisionId"] || retryMetadata["routingHash"] != originalMetadata["routingHash"] || retryMetadata["expectedPublishedProfileRevisionId"] != originalMetadata["expectedPublishedProfileRevisionId"] || retryMetadata["retryOf"] != operation.ID {
		t.Fatalf("retry lost governance identity: source=%s metadata=%v", retry.SourceID, retryMetadata)
	}
	type retryTargetState struct {
		ID         string
		Status     string
		Dependency string
		Pending    bool
	}
	states := map[string]retryTargetState{}
	rows, err := st.pool.Query(ctx, `SELECT id::text,target_id::text,status,coalesce(depends_on_target_id::text,''),governance_finalization_pending FROM operation_targets WHERE operation_id=$1`, retry.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var state retryTargetState
		var targetID string
		if err := rows.Scan(&state.ID, &targetID, &state.Status, &state.Dependency, &state.Pending); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		states[targetID] = state
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(states) != 2 || states[skillTarget.ID].Status != bridgeprotocol.OperationSucceeded || !states[skillTarget.ID].Pending || states[relayTarget.ID].Status != bridgeprotocol.OperationQueued || !states[relayTarget.ID].Pending || states[relayTarget.ID].Dependency != states[skillTarget.ID].ID {
		t.Fatalf("retry target evidence=%+v", states)
	}
	claimedRetry, err := st.ClaimOperationTarget(ctx)
	if err != nil || claimedRetry.Target.ID != relayTarget.ID {
		t.Fatalf("retry dispatched target=%+v err=%v", claimedRetry, err)
	}
	if err := st.FinishOperationTarget(ctx, claimedRetry.OperationTarget.ID, bridgeprotocol.OperationSucceeded, map[string]any{"routingReloaded": true, "routingHash": routingHash}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT status FROM operations WHERE id=$1`, retry.ID).Scan(&retry.Status); err != nil {
		t.Fatal(err)
	}
	if retry.Status != bridgeprotocol.OperationRunning {
		t.Fatalf("retry aggregated before finalization: %s", retry.Status)
	}
	profileRevisionID, _ := retryMetadata["profileRevisionId"].(string)
	if err := st.FinalizeProfileApply(ctx, retry.ID, retry.SourceID, profileRevisionID); err != nil {
		t.Fatal(err)
	}
	var publishedRevision string
	var snapshots int
	if err := st.pool.QueryRow(ctx, `SELECT profile_revision_id::text FROM published_profiles WHERE profile_id=$1`, retry.SourceID).Scan(&publishedRevision); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM desired_snapshots WHERE source_operation_target_id IN ($1,$2)`, states[skillTarget.ID].ID, states[relayTarget.ID].ID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if publishedRevision != profileRevisionID || snapshots != 2 {
		t.Fatalf("retry finalization published=%s snapshots=%d", publishedRevision, snapshots)
	}
	replayedAfterFinalization, err := st.RetryFailedTargets(ctx, operation.ID, "profile-aware-retry")
	if err != nil || replayedAfterFinalization.ID != retry.ID {
		t.Fatalf("post-finalization idempotent retry=%s err=%v, want %s", replayedAfterFinalization.ID, err, retry.ID)
	}
	if _, err := st.RetryFailedTargets(ctx, operation.ID, "stale-profile-aware-retry"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Profile Apply retry returned %v, want conflict before dispatch", err)
	}
}

func createProfileApplyOperationForLifecycleTest(t *testing.T, st *Store, name string) (domain.Operation, domain.Target, domain.Target, string) {
	t.Helper()
	ctx := context.Background()
	skillTarget := integrationTarget(t, st, "local/claude")
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: name, ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	skillManifest, err := st.ResolveProfileManifest(ctx, profile.ID, skillTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	relayManifest, err := st.ResolveProfileManifest(ctx, profile.ID, relayTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	skillToken, _, err := st.CreatePreflightConfirmation(ctx, profile.ID, skillTarget.ID, strings.Repeat("a", 64), skillManifest, bridgeprotocol.Diff{}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	relayToken, _, err := st.CreatePreflightConfirmation(ctx, profile.ID, relayTarget.ID, strings.Repeat("b", 64), relayManifest, bridgeprotocol.Diff{}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := st.CreateProfileApplyOperation(ctx, profile.ID, []string{skillToken, relayToken}, "lifecycle-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return operation, skillTarget, relayTarget, relayManifest.RelayGovernance.RoutingHash
}
