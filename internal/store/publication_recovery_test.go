package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/google/uuid"
)

func skillPublicationFixture(t *testing.T, st *Store) (domain.Operation, domain.Profile) {
	t.Helper()
	ctx := context.Background()
	if err := st.BootstrapEnvironment(ctx, "atomic-skills", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{{NodeID: uuid.NewString(), Name: "fixture", Kind: "salt", SaltMinionID: "fixture", Status: "online", Version: "3008.0"}}); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "atomic", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	tokens := []string{}
	for _, key := range []string{"local/claude", "salt:fixture/claude"} {
		target := integrationTarget(t, st, key)
		manifest, err := st.ResolveProfileManifest(ctx, profile.ID, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := st.CreatePreflightConfirmation(ctx, profile.ID, target.ID, strings.Repeat("a", 64), manifest, bridgeprotocol.Diff{}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	op, err := st.CreateProfileSkillApplyOperation(ctx, profile.ID, tokens, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return op, profile
}

func TestPublicationSkillsAtomicIntegration(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
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
			_, err = st.CreateOperation(ctx, CreateOperationInput{Kind: "restore", TargetIDs: []string{first.Target.ID}})
			if !errors.Is(err, ErrOperationActive) {
				t.Fatalf("publication released target early: %v", err)
			}
			if outcome == "cancelled" {
				if err := st.CancelOperation(ctx, op.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				second, err := st.ClaimOperationTarget(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var apiErr *bridgeprotocol.APIError
				if outcome == "failed" {
					apiErr = &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "test unavailable"}
				}
				if err := st.FinishOperationTarget(ctx, second.OperationTarget.ID, outcome, bridgeprotocol.TargetResult{}, apiErr); err != nil {
					t.Fatal(err)
				}
			}
			pending, err := st.PendingGovernanceFinalizations(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "succeeded" {
				if len(pending) != 1 {
					t.Fatalf("missing startup publication: %d", len(pending))
				}
				if err := st.FinalizeProfileApply(ctx, op.ID, p.ID, p.CurrentRevisionID); err != nil {
					t.Fatal(err)
				}
				if err := st.FinalizeProfileApply(ctx, op.ID, p.ID, p.CurrentRevisionID); !errors.Is(err, ErrConflict) {
					t.Fatalf("replayed publish: %v", err)
				}
			} else {
				if len(pending) != 0 {
					t.Fatal("failed fleet eligible for publication")
				}
				if err := st.FinalizeProfileApply(ctx, op.ID, p.ID, p.CurrentRevisionID); !errors.Is(err, ErrConflict) {
					t.Fatalf("failed fleet published: %v", err)
				}
			}
			var count int
			if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM desired_snapshots WHERE source_id=$1`, p.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if outcome == "succeeded" {
				want = 2
			}
			if count != want {
				t.Fatalf("snapshots=%d want=%d", count, want)
			}
		})
	}
}

func TestPublicationRelaxedOwnsTargetsAndRecoversIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	op, client, _ := relaxRelayApplyFixture(t, st, "relaxed-owned", bridgeprotocol.ErrMCPMIncompatible, "")
	_, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "restore", TargetIDs: []string{client.Target.ID}})
	if !errors.Is(err, ErrOperationActive) {
		t.Fatalf("old Apply released target before publication: %v", err)
	}
	pending, err := st.PendingGovernanceFinalizations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != op.ID {
		t.Fatalf("relaxed publication missing from recovery: %+v", pending)
	}
	if err := st.FinalizeProfileApply(ctx, op.ID, op.SourceID, operationRevisionID(t, st, op)); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateOperation(ctx, CreateOperationInput{Kind: "restore", TargetIDs: []string{client.Target.ID}})
	if err != nil {
		t.Fatalf("finalized Apply retained occupancy: %v", err)
	}
	if err := st.FinalizeProfileApply(ctx, op.ID, op.SourceID, operationRevisionID(t, st, op)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale finalize accepted: %v", err)
	}
}
