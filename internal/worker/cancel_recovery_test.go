package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/store"
	"github.com/google/uuid"
)

type cancelledGuardBridge struct {
	governanceWorkerBridge
	lookupErr error
	commits   int
}

func (b *cancelledGuardBridge) Operation(context.Context, string) (bridgeprotocol.Operation, error) {
	return b.operation, b.lookupErr
}
func (b *cancelledGuardBridge) Commit(context.Context, string, string, bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	b.commits++
	return bridgeprotocol.TargetResult{}, errors.New("commit reached sentinel")
}

// Each negative case has a valid request and real isolated Store. A positive
// control reaches Commit with the identical fixture, so JSON/Store errors cannot
// make a deleted or bypassed guard appear safe.
func TestCancelledRecoveryRejectsUnprovenReplay(t *testing.T) {
	for _, kind := range []string{"apply", "restore"} {
		for _, scenario := range []string{"valid", "fresh-running", "salt-valid", "salt-failure", "local-salt-kind", "missing", "lookup-error", "running", "wrong-id", "wrong-kind", "wrong-target", "missing-result", "contradictory", "failure"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				st := newWorkerIntegrationStore(t)
				if err := st.BootstrapEnvironment(ctx, "guard-fixture", "runner", "UTC", 6276); err != nil {
					t.Fatal(err)
				}
				var targetID string
				if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/claude'`).Scan(&targetID); err != nil {
					t.Fatal(err)
				}
				dt, err := st.Target(ctx, targetID)
				if err != nil {
					t.Fatal(err)
				}
				target := toBridgeTarget(dt)
				if scenario == "salt-valid" || scenario == "salt-failure" {
					target.NodeKind = bridgeprotocol.NodeKindSalt
					target.SaltMinionID = "test-minion"
				}
				manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: target, Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{}}
				metadata := map[string]any{"manifest": manifest, "targetRevision": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "sourceKind": "profile_apply"}
				if kind == "restore" {
					backup, err := st.RecordBackup(ctx, bridgeprotocol.Backup{ID: uuid.NewString(), TargetID: targetID, NodeKind: target.NodeKind, Runtime: target.Runtime, CreatedAt: time.Now().UTC()}, "", &manifest)
					if err != nil {
						t.Fatal(err)
					}
					metadata["backupId"] = backup.ID
				}
				body, err := json.Marshal(metadata)
				if err != nil {
					t.Fatal(err)
				}
				id := uuid.NewString()
				result := bridgeprotocol.TargetResult{Status: "succeeded", Health: "healthy"}
				op := bridgeprotocol.Operation{ID: id, Kind: kind, Status: "succeeded", Targets: []bridgeprotocol.OperationTarget{{TargetID: target.ID, Status: "succeeded", Result: &result}}}
				b := &cancelledGuardBridge{}
				switch scenario {
				case "salt-valid", "salt-failure", "local-salt-kind":
					op.Kind = "toolhub." + kind
				case "missing":
					op = bridgeprotocol.Operation{}
				case "lookup-error":
					b.lookupErr = errors.New("unavailable")
				case "running":
					op.Status = "running"
				case "wrong-id":
					op.ID = "different"
				case "wrong-kind":
					op.Kind = "edit"
				case "wrong-target":
					op.Targets[0].TargetID = "other"
				case "missing-result":
					op.Targets[0].Result = nil
				case "contradictory":
					op.Targets[0].Status = "failed"
				}
				if scenario == "failure" || scenario == "salt-failure" {
					op.Status = "failed"
					op.Targets[0].Status = "failed"
					op.Targets[0].Error = &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "bounded conflict"}
				}
				b.operation = op
				w := &Worker{store: st, bridge: b}
				marker := id
				if scenario == "fresh-running" {
					marker = ""
				}
				item := store.WorkItem{Operation: domain.Operation{Kind: kind, CancelRequested: true}, OperationTarget: domain.OperationTarget{ID: id, BridgeOperationID: marker, Request: body}}
				_, apiErr := w.executeCommit(ctx, item, target)
				if apiErr == nil {
					t.Fatal("expected guard or commit sentinel error")
				}
				if (scenario == "failure" || scenario == "salt-failure") && apiErr.Code != bridgeprotocol.ErrRevisionConflict {
					t.Fatalf("lost failure: %+v", apiErr)
				}
				want := 0
				if scenario == "valid" || scenario == "fresh-running" || scenario == "salt-valid" {
					want = 1
				}
				if b.commits != want {
					t.Fatalf("commits=%d want=%d error=%+v", b.commits, want, apiErr)
				}
			})
		}
	}
}
