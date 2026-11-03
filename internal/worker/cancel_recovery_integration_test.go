package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/store"
	"github.com/google/uuid"
)

// Exercise cancellation and recovery together, including a second cancellation
// while a completed Bridge operation is queued for PostgreSQL projection.
func TestCancelledRecoveryClaimIntegration(t *testing.T) {
	for _, kind := range []string{"apply", "restore"} {
		for _, marked := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/unmarked", true: "/marked"}[marked], func(t *testing.T) {
				ctx := context.Background()
				st := newWorkerIntegrationStore(t)
				if err := st.BootstrapEnvironment(ctx, "cancel-recovery", "runner", "UTC", 6276); err != nil {
					t.Fatal(err)
				}
				var targetID, siblingID string
				if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/claude'`).Scan(&targetID); err != nil {
					t.Fatal(err)
				}
				if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/codex'`).Scan(&siblingID); err != nil {
					t.Fatal(err)
				}
				opID, otID, sibID := uuid.NewString(), uuid.NewString(), uuid.NewString()
				if _, err := st.Pool().Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,$2,'running','{"mode":"skills_only"}')`, opID, kind); err != nil {
					t.Fatal(err)
				}
				marker := ""
				if marked {
					marker = otID
				}
				if _, err := st.Pool().Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,bridge_operation_id) VALUES($1,$2,$3,'running',$4),($5,$2,$6,'queued','')`, otID, opID, targetID, marker, sibID, siblingID); err != nil {
					t.Fatal(err)
				}
				if err := st.CancelOperation(ctx, opID); err != nil {
					t.Fatal(err)
				}
				fake := &governanceWorkerBridge{operation: bridgeprotocol.Operation{ID: otID, Status: bridgeprotocol.OperationSucceeded}}
				w := &Worker{store: st, bridge: fake, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
				if err := w.Recover(ctx); err != nil {
					t.Fatal(err)
				}
				var status string
				if err := st.Pool().QueryRow(ctx, `SELECT status FROM operation_targets WHERE id=$1`, otID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if !marked {
					if status != "cancelled" {
						t.Fatalf("unmarked cancelled work left %s, want cancelled", status)
					}
				} else {
					item, err := st.ClaimOperationTarget(ctx)
					if err != nil {
						t.Fatalf("cancelled Bridge recovery must be claimable: %v", err)
					}
					if item.OperationTarget.ID != otID || !item.Operation.CancelRequested {
						t.Fatalf("wrong recovered work: %+v", item)
					}
					if err := st.RequeueRunningOperationTarget(ctx, otID); err != nil {
						t.Fatal(err)
					}
					if err := st.CancelOperation(ctx, opID); err != nil {
						t.Fatal(err)
					}
					if err := w.Recover(ctx); err != nil {
						t.Fatal(err)
					}
					item, err = st.ClaimOperationTarget(ctx)
					if err != nil {
						t.Fatalf("repeat cancellation swallowed recovery: %v", err)
					}
					if item.OperationTarget.ID != otID {
						t.Fatal("claimed sibling")
					}
					if err := st.FinishOperationTarget(ctx, otID, bridgeprotocol.OperationSucceeded, bridgeprotocol.TargetResult{}, nil); err != nil {
						t.Fatal(err)
					}
				}
				if err := st.Pool().QueryRow(ctx, `SELECT status FROM operation_targets WHERE id=$1`, sibID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status != "cancelled" {
					t.Fatalf("sibling revived: %s", status)
				}
				if _, err := st.ClaimOperationTarget(ctx); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("unexpected queued work: %v", err)
				}
			})
		}
	}
}
