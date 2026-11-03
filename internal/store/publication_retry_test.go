package store

import (
	"context"
	"errors"
	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/google/uuid"
	"testing"
)

func TestPublicationSkillsRetryIntegration(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "reject-stale"}[superseded], func(t *testing.T) {
			ctx := context.Background()
			st := newIntegrationStore(t, true)
			op, p := skillPublicationFixture(t, st)
			first, err := st.ClaimOperationTarget(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.FinishOperationTarget(ctx, first.OperationTarget.ID, "succeeded", bridgeprotocol.TargetResult{}, nil); err != nil {
				t.Fatal(err)
			}
			second, err := st.ClaimOperationTarget(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.FinishOperationTarget(ctx, second.OperationTarget.ID, "failed", bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "fixture"}); err != nil {
				t.Fatal(err)
			}
			if superseded {
				newer, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "restore", TargetIDs: []string{first.Target.ID}})
				if err != nil {
					t.Fatal(err)
				}
				item, err := st.ClaimOperationTarget(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if item.Operation.ID != newer.ID {
					t.Fatal("wrong operation")
				}
				if err := st.FinishOperationTarget(ctx, item.OperationTarget.ID, "succeeded", bridgeprotocol.TargetResult{}, nil); err != nil {
					t.Fatal(err)
				}
			}
			key := uuid.NewString()
			retry, err := st.RetryFailedTargets(ctx, op.ID, key)
			if superseded {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("stale evidence accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if retry.SourceID != p.ID {
				t.Fatal("retry lost Profile identity")
			}
			item, err := st.ClaimOperationTarget(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if item.Target.ID != second.Target.ID {
				t.Fatal("successful target re-executed")
			}
			if err := st.FinishOperationTarget(ctx, item.OperationTarget.ID, "succeeded", bridgeprotocol.TargetResult{}, nil); err != nil {
				t.Fatal(err)
			}
			if err := st.FinalizeProfileApply(ctx, retry.ID, p.ID, p.CurrentRevisionID); err != nil {
				t.Fatal(err)
			}
			if newerRetry, e := st.RetryFailedTargets(ctx, op.ID, key); e != nil || newerRetry.ID != retry.ID {
				t.Fatalf("retry idempotency after publish: %v", e)
			}
			var count int
			if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM desired_snapshots WHERE source_id=$1`, p.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 2 {
				t.Fatalf("retry published %d snapshots", count)
			}
		})
	}
}
