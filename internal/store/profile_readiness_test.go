package store

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
)

func TestProfileReadinessGeneratesCommandOnlyForAppliedPublishedState(t *testing.T) {
	st, profile, _, _ := readyLaunchProfile(t, "claude")
	inspection := bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true}

	result, err := st.ProfileReadiness(context.Background(), profile.ID, inspection)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.ReasonCode != "" || result.ProfileRevisionID != profile.CurrentRevisionID || result.Command == nil {
		t.Fatalf("readiness=%+v", result)
	}
	want := "profile=" + profile.Name
	if !strings.Contains(result.Command.Display, want) {
		t.Fatalf("display=%q does not contain %q", result.Command.Display, want)
	}
}

func TestProfileReadinessFailsClosedWithoutAuthoritativeState(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*testing.T, *Store, domain.Profile, domain.Target, domain.Target)
		inspection bridgeprotocol.NativeClientInspectionResponse
		wantReason string
	}{
		{
			name: "current revision is not published",
			mutate: func(t *testing.T, st *Store, profile domain.Profile, _, _ domain.Target) {
				_, err := st.SaveProfile(context.Background(), profile.ID, ProfileInput{Name: profile.Name, ClientKind: profile.ClientKind, Category: profile.Category, Revision: profile.Revision})
				if err != nil {
					t.Fatal(err)
				}
			},
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true},
			wantReason: "profile_revision_not_published",
		},
		{
			name: "skill target source is no longer the profile apply",
			mutate: func(t *testing.T, st *Store, profile domain.Profile, skillTarget, _ domain.Target) {
				manifest, err := st.ResolveProfileManifest(context.Background(), profile.ID, skillTarget.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.PinDesiredSnapshot(context.Background(), skillTarget.ID, "target_edit", "", "", manifest); err != nil {
					t.Fatal(err)
				}
			},
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true},
			wantReason: "profile_target_not_applied",
		},
		{
			name: "skill target is unhealthy",
			mutate: func(t *testing.T, st *Store, _ domain.Profile, skillTarget, _ domain.Target) {
				if _, err := st.UpdateTargetHealth(context.Background(), skillTarget.ID, bridgeprotocol.HealthDrifted, "target_drifted", "fixture drift", map[string]any{}, false); err != nil {
					t.Fatal(err)
				}
			},
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true},
			wantReason: "profile_target_unhealthy",
		},
		{
			name: "relay is intentionally paused",
			mutate: func(t *testing.T, st *Store, _ domain.Profile, _, _ domain.Target) {
				if err := st.SetRelayIntentionalPaused(context.Background(), true); err != nil {
					t.Fatal(err)
				}
			},
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true},
			wantReason: "relay_paused",
		},
		{
			name: "relay target is unhealthy",
			mutate: func(t *testing.T, st *Store, _ domain.Profile, _, relayTarget domain.Target) {
				if _, err := st.UpdateTargetHealth(context.Background(), relayTarget.ID, bridgeprotocol.HealthBlocked, bridgeprotocol.ErrRelayUnhealthy, "fixture blocked", map[string]any{}, false); err != nil {
					t.Fatal(err)
				}
			},
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true},
			wantReason: "relay_unhealthy",
		},
		{
			name:       "native client kind differs",
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "codex", Version: "0.147.0", Supported: true},
			wantReason: "native_client_kind_mismatch",
		},
		{
			name:       "native client version is unsupported",
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.231", Supported: false, ErrorCode: "native_client_version_unsupported"},
			wantReason: "native_client_version_unsupported",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, profile, skillTarget, relayTarget := readyLaunchProfile(t, "claude")
			if test.mutate != nil {
				test.mutate(t, st, profile, skillTarget, relayTarget)
			}
			result, err := st.ProfileReadiness(context.Background(), profile.ID, test.inspection)
			if err != nil {
				t.Fatal(err)
			}
			if result.Ready || result.ReasonCode != test.wantReason || result.Command != nil {
				t.Fatalf("readiness=%+v want reason %q with no command", result, test.wantReason)
			}
		})
	}
}

func TestProfileReadinessRejectsStaleRelayRoutingHash(t *testing.T) {
	st, profile, _, _ := readyLaunchProfile(t, "claude")
	second, err := st.SaveProfile(context.Background(), uuid.NewString(), ProfileInput{Name: "claude-second", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "apply", second.ID, map[string]any{"profileRevisionId": second.CurrentRevisionID})
	if err := st.FinalizeProfilePublish(context.Background(), opID, second.ID, second.CurrentRevisionID, governanceRoutingHash(t, st, opID)); err != nil {
		t.Fatal(err)
	}

	result, err := st.ProfileReadiness(context.Background(), profile.ID, bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.ReasonCode != "relay_routing_mismatch" || result.Command != nil {
		t.Fatalf("readiness=%+v", result)
	}
}

func TestProfileReadinessRejectsSuccessfulRuntimeMutationWithoutFinalization(t *testing.T) {
	tests := []struct {
		name     string
		complete func(*testing.T, *Store, domain.Operation, bridgeprotocol.DesiredManifest)
	}{
		{
			name: "partial apply",
			complete: func(t *testing.T, st *Store, _ domain.Operation, _ bridgeprotocol.DesiredManifest) {
				relay, err := st.ClaimOperationTarget(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err := st.FinishOperationTarget(context.Background(), relay.OperationTarget.ID, bridgeprotocol.OperationFailed, map[string]any{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "fixture relay failure"}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "cancelled apply",
			complete: func(t *testing.T, st *Store, operation domain.Operation, _ bridgeprotocol.DesiredManifest) {
				if err := st.CancelOperation(context.Background(), operation.ID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "finalization failure",
			complete: func(t *testing.T, st *Store, operation domain.Operation, relayManifest bridgeprotocol.DesiredManifest) {
				relay, err := st.ClaimOperationTarget(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err := st.FinishOperationTarget(context.Background(), relay.OperationTarget.ID, bridgeprotocol.OperationSucceeded, map[string]any{"routingReloaded": true, "routingHash": relayManifest.RelayGovernance.RoutingHash}, nil); err != nil {
					t.Fatal(err)
				}
				if err := st.FailGovernanceFinalization(context.Background(), operation.ID, &bridgeprotocol.APIError{Code: "governance_finalization_failed", Message: "fixture finalization failure"}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, published, skillTarget, relayTarget := readyLaunchProfile(t, "claude")
			candidate, err := st.SaveProfile(context.Background(), uuid.NewString(), ProfileInput{Name: "claude-candidate", ClientKind: "claude", Category: "coding"})
			if err != nil {
				t.Fatal(err)
			}
			operation, relayManifest := readinessApplyOperation(t, st, candidate, skillTarget, relayTarget)
			skill, err := st.ClaimOperationTarget(context.Background())
			if err != nil || skill.Target.ID != skillTarget.ID {
				t.Fatalf("skill target=%+v err=%v", skill.Target, err)
			}
			if err := st.FinishOperationTarget(context.Background(), skill.OperationTarget.ID, bridgeprotocol.OperationSucceeded, map[string]any{}, nil); err != nil {
				t.Fatal(err)
			}
			test.complete(t, st, operation, relayManifest)

			result, err := st.ProfileReadiness(context.Background(), published.ID, bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true})
			if err != nil {
				t.Fatal(err)
			}
			if result.Ready || result.ReasonCode != "profile_apply_unfinalized" || result.Command != nil {
				t.Fatalf("readiness trusted stale projection after %s: %+v", test.name, result)
			}
		})
	}
}

func TestProfileReadinessUsesOneRepeatableSnapshot(t *testing.T) {
	st, profile, skillTarget, _ := readyLaunchProfile(t, "claude")
	ctx := context.Background()
	var appliedSnapshotID string
	var appliedDesiredRevision int64
	if err := st.pool.QueryRow(ctx, `SELECT snapshot_id::text,desired_revision FROM target_desired_snapshots WHERE target_id=$1`, skillTarget.ID).Scan(&appliedSnapshotID, &appliedDesiredRevision); err != nil {
		t.Fatal(err)
	}
	updated, err := st.SaveProfile(ctx, profile.ID, ProfileInput{Name: profile.Name + " updated", ClientKind: profile.ClientKind, Category: profile.Category, Revision: profile.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE profiles SET current_revision_id=$2,revision=$3 WHERE id=$1`, profile.ID, profile.CurrentRevisionID, profile.Revision); err != nil {
		t.Fatal(err)
	}
	manifest, err := st.ResolveProfileManifest(ctx, profile.ID, skillTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PinDesiredSnapshot(ctx, skillTarget.ID, "target_edit", "", "", manifest); err != nil {
		t.Fatal(err)
	}

	barrier := &readinessQueryBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	config := st.pool.Config()
	config.MaxConns = 1
	config.MinConns = 1
	config.ConnConfig.Tracer = barrier
	readPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readPool.Close)
	readStore := &Store{pool: readPool, cipher: st.cipher}
	type readinessResult struct {
		value ProfileLaunchReadiness
		err   error
	}
	resultCh := make(chan readinessResult, 1)
	go func() {
		value, readinessErr := readStore.ProfileReadiness(ctx, profile.ID, bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true})
		resultCh <- readinessResult{value: value, err: readinessErr}
	}()
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness did not reach the transaction barrier")
	}

	tx, err := st.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE profiles SET current_revision_id=$2,revision=$3 WHERE id=$1`, profile.ID, updated.CurrentRevisionID, updated.Revision); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE target_desired_snapshots SET snapshot_id=$2,desired_revision=$3,health='healthy',updated_at=now() WHERE target_id=$1`, skillTarget.ID, appliedSnapshotID, appliedDesiredRevision); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	close(barrier.release)

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.value.Ready || result.value.ReasonCode != "profile_target_not_applied" {
			t.Fatalf("readiness combined states from different commits: %+v", result.value)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readiness did not finish after transaction barrier release")
	}
}

type readinessQueryBarrier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *readinessQueryBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT ds.source_kind") {
		b.once.Do(func() {
			close(b.entered)
			<-b.release
		})
	}
	return ctx
}

func (*readinessQueryBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func readyLaunchProfile(t *testing.T, clientKind string) (*Store, domain.Profile, domain.Target, domain.Target) {
	t.Helper()
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "launch-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	skillTarget := integrationTarget(t, st, "local/"+clientKind)
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: clientKind + "-launch", ClientKind: clientKind, Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	operation, relayManifest := readinessApplyOperation(t, st, profile, skillTarget, relayTarget)
	first, err := st.ClaimOperationTarget(ctx)
	if err != nil || first.Target.ID != skillTarget.ID {
		t.Fatalf("first target=%+v err=%v", first.Target, err)
	}
	if err := st.FinishOperationTarget(ctx, first.OperationTarget.ID, bridgeprotocol.OperationSucceeded, map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	second, err := st.ClaimOperationTarget(ctx)
	if err != nil || second.Target.ID != relayTarget.ID {
		t.Fatalf("second target=%+v err=%v", second.Target, err)
	}
	if err := st.FinishOperationTarget(ctx, second.OperationTarget.ID, bridgeprotocol.OperationSucceeded, map[string]any{"routingReloaded": true, "routingHash": relayManifest.RelayGovernance.RoutingHash}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeProfileApply(ctx, operation.ID, profile.ID, profile.CurrentRevisionID); err != nil {
		t.Fatal(err)
	}
	return st, profile, skillTarget, relayTarget
}

func readinessApplyOperation(t *testing.T, st *Store, profile domain.Profile, skillTarget, relayTarget domain.Target) (domain.Operation, bridgeprotocol.DesiredManifest) {
	t.Helper()
	ctx := context.Background()
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
	operation, err := st.CreateProfileApplyOperation(ctx, profile.ID, []string{skillToken, relayToken}, "launch-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return operation, relayManifest
}
