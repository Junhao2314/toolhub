package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeclient"
	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/market"
	"github.com/Junhao2314/toolhub/internal/store"
	"github.com/Junhao2314/toolhub/internal/worker"
	"github.com/google/uuid"
)

type cancelReplayAdapter struct {
	fakeAdapter
	calls atomic.Int32
	fail  bool
}

func (a *cancelReplayAdapter) Commit(context.Context, bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	return a.result()
}
func (a *cancelReplayAdapter) Restore(context.Context, bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	return a.result()
}
func (a *cancelReplayAdapter) result() (bridgeprotocol.TargetResult, error) {
	a.calls.Add(1)
	if a.fail {
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "test conflict"}
	}
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, BackupID: "replayed-backup"}, nil
}

func TestCancelledRecoveryRealBridgeIntegration(t *testing.T) {
	for _, kind := range []string{"apply", "restore"} {
		for _, saltRecovery := range []bool{false, true} {
			for _, fail := range []bool{false, true} {
				t.Run(kind+map[bool]string{false: "/local", true: "/salt"}[saltRecovery]+map[bool]string{false: "/success", true: "/failure"}[fail], func(t *testing.T) {
					ctx := context.Background()
					st := newBridgeWorkerIntegrationStore(t)
					if err := st.BootstrapEnvironment(ctx, "cancel-real-bridge", "runner", "UTC", 6276); err != nil {
						t.Fatal(err)
					}
					keyName := "local/claude"
					if saltRecovery {
						if err := st.UpsertDiscoveredNodes(ctx, []bridgeprotocol.NodeInfo{{NodeID: uuid.NewString(), Name: "test-salt", Kind: bridgeprotocol.NodeKindSalt, SaltMinionID: "test-salt", Status: "online", Version: "3008.0"}}); err != nil {
							t.Fatal(err)
						}
						keyName = "salt:test-salt/claude"
					}
					var targetID string
					if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key=$1`, keyName).Scan(&targetID); err != nil {
						t.Fatal(err)
					}
					dt, err := st.Target(ctx, targetID)
					if err != nil {
						t.Fatal(err)
					}
					target := bridgeprotocol.Target{ID: dt.ID, NodeID: dt.NodeID, NodeKind: dt.NodeKind, Runtime: dt.Runtime, ManagedUsername: dt.ManagedUsername, SaltMinionID: dt.SaltMinionID}
					profileID, revisionID := "", ""
					if kind == "apply" {
						profile, e := st.SaveProfile(ctx, uuid.NewString(), store.ProfileInput{Name: "recovery-apply", ClientKind: "claude", Category: "coding"})
						if e != nil {
							t.Fatal(e)
						}
						profileID, revisionID = profile.ID, profile.CurrentRevisionID
					}
					manifest := bridgeprotocol.DesiredManifest{ProfileID: profileID, ProfileRevision: map[bool]int64{true: 1, false: 0}[kind == "apply"], SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: target, Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{}}
					revision := strings.Repeat("a", 64)
					backupID := ""
					metadata := map[string]any{"manifest": manifest, "targetRevision": revision, "sourceKind": "profile_apply", "sourceId": profileID}
					if kind == "restore" {
						backupID = uuid.NewString()
						b, err := st.RecordBackup(ctx, bridgeprotocol.Backup{ID: backupID, TargetID: targetID, NodeKind: target.NodeKind, Runtime: target.Runtime, Revision: revision, CreatedAt: time.Now().UTC()}, "", &manifest)
						if err != nil {
							t.Fatal(err)
						}
						metadata["backupId"] = b.ID
						metadata["sourceKind"] = "restore"
						metadata["sourceId"] = b.ID
					}
					opID, otID := uuid.NewString(), uuid.NewString()
					body, err := json.Marshal(metadata)
					if err != nil {
						t.Fatal(err)
					}
					opMeta, _ := json.Marshal(map[string]any{"mode": "skills_only", "profileRevisionId": revisionID})
					if _, err := st.Pool().Exec(ctx, `INSERT INTO operations(id,kind,status,source_id,metadata) VALUES($1,$2,'running',nullif($3,'')::uuid,$4)`, opID, kind, profileID, string(opMeta)); err != nil {
						t.Fatal(err)
					}
					if _, err := st.Pool().Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,bridge_operation_id,request,governance_finalization_pending) VALUES($1,$2,$3,'running',$1,$4,$5)`, otID, opID, targetID, string(body), kind == "apply"); err != nil {
						t.Fatal(err)
					}
					journal, err := OpenJournal(filepath.Join(t.TempDir(), "bridge.db"))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = journal.Close() })
					adapter := &cancelReplayAdapter{fail: fail}
					key := []byte(strings.Repeat("k", 32))
					server, err := NewServer(key, journal, adapter, slog.New(slog.NewTextHandler(io.Discard, nil)))
					if err != nil {
						t.Fatal(err)
					}
					socket := filepath.Join(t.TempDir(), "b.sock")
					listener, err := net.Listen("unix", socket)
					if err != nil {
						t.Fatal(err)
					}
					httpServer := &http.Server{Handler: server.Router()}
					done := make(chan error, 1)
					go func() { done <- httpServer.Serve(listener) }()
					t.Cleanup(func() {
						_ = httpServer.Close()
						if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
							t.Error(err)
						}
					})
					client, err := bridgeclient.New(socket, key)
					if err != nil {
						t.Fatal(err)
					}
					req := bridgeprotocol.CommitRequest{OperationID: otID, OperationKind: kind, Target: target, ExpectedRevision: revision, Manifest: manifest, Artifacts: []bridgeprotocol.Artifact{}, SecretValues: map[string]string{}, BackupID: backupID}
					_, err = client.Commit(ctx, kind, otID, req)
					if fail == (err == nil) {
						t.Fatalf("initial commit err=%v fail=%v", err, fail)
					}
					if saltRecovery {
						operation, err := journal.Operation(otID)
						if err != nil {
							t.Fatal(err)
						}
						operation.Kind = "toolhub." + kind
						if err := journal.PutOperation(operation); err != nil {
							t.Fatal(err)
						}
					}
					if err := st.CancelOperation(ctx, opID); err != nil {
						t.Fatal(err)
					}
					w := worker.New(st, client, market.NewMulti(), slog.New(slog.NewTextHandler(io.Discard, nil)))
					if err := w.Recover(ctx); err != nil {
						t.Fatal(err)
					}
					// Repeated cancellation and a second startup must preserve replay work.
					if saltRecovery {
						operation, err := journal.Operation(otID)
						if err != nil {
							t.Fatal(err)
						}
						operation.Kind = "toolhub." + kind
						if err := journal.PutOperation(operation); err != nil {
							t.Fatal(err)
						}
					}
					if err := st.CancelOperation(ctx, opID); err != nil {
						t.Fatal(err)
					}
					if err := w.Recover(ctx); err != nil {
						t.Fatal(err)
					}
					runCtx, cancel := context.WithCancel(ctx)
					defer cancel()
					w.Run(runCtx, 1)
					deadline := time.Now().Add(5 * time.Second)
					var status, code string
					for {
						if err := st.Pool().QueryRow(ctx, `SELECT status,error_code FROM operation_targets WHERE id=$1`, otID).Scan(&status, &code); err != nil {
							t.Fatal(err)
						}
						if bridgeprotocol.IsTerminalOperationStatus(status) {
							op, e := st.Operation(ctx, opID)
							if e != nil {
								t.Fatal(e)
							}
							if bridgeprotocol.IsTerminalOperationStatus(op.Status) {
								break
							}
						}
						if time.Now().After(deadline) {
							t.Fatalf("replay stuck: %s", status)
						}
						time.Sleep(10 * time.Millisecond)
					}
					cancel()
					want := "succeeded"
					if fail {
						want = "failed"
					}
					if status != want {
						detail, _ := st.OperationDetail(ctx, opID)
						t.Fatalf("status=%s code=%s detail=%s", status, code, detail)
					}
					if fail && code != bridgeprotocol.ErrRevisionConflict {
						t.Fatalf("lost failure: %s", code)
					}
					if adapter.calls.Load() != 1 {
						t.Fatalf("adapter executed %d times", adapter.calls.Load())
					}
					var snapshots, backups int
					if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM desired_snapshots WHERE source_operation_target_id=$1`, otID).Scan(&snapshots); err != nil {
						t.Fatal(err)
					}
					expected := 1
					if fail {
						expected = 0
					}
					if snapshots != expected {
						t.Fatalf("snapshots=%d want=%d", snapshots, expected)
					}
					if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM backups WHERE source_operation_id=$1`, opID).Scan(&backups); err != nil {
						t.Fatal(err)
					}
					if backups != expected {
						t.Fatalf("backups=%d want=%d", backups, expected)
					}
					if err := w.Recover(ctx); err != nil {
						t.Fatal(err)
					}
					if adapter.calls.Load() != 1 {
						t.Fatal("repeat recovery re-executed adapter")
					}
					if _, err := st.ClaimOperationTarget(ctx); !errors.Is(err, store.ErrNotFound) {
						t.Fatalf("target still occupied: %v", err)
					}
				})
			}
		}
	}
}
