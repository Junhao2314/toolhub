package bridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/runtime"
	"github.com/Junhao2314/toolhub/internal/saltdriver"
)

type relayStatusController struct{}

func (relayStatusController) Action(_ context.Context, action string) (string, error) {
	if action == "status" {
		return "inactive", errors.New("inactive")
	}
	return "", errors.New("unexpected relay action")
}

func TestRelayHealthPreservesIntentionalPause(t *testing.T) {
	adapter := &CompositeAdapter{RelayManager: runtime.NewRelayManager(relayStatusController{}, t.TempDir())}
	status, err := adapter.Relay(context.Background(), "health", bridgeprotocol.RelayActionRequest{Port: 6276, IntentionalPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	if !status.IntentionalPaused || status.Healthy || status.ErrorCode != "" {
		t.Fatalf("paused relay status=%+v", status)
	}
}

func TestRelayDiffAcceptsProjectedManagedContentHash(t *testing.T) {
	contentHash := strings.Repeat("a", 64)
	server := bridgeprotocol.MCPMember{MemberID: uuid.NewString(), Name: "server", ContentHash: contentHash}
	diff := relayDiff([]bridgeprotocol.InventoryMember{{ID: "mcp:server", Kind: "mcp", Name: server.Name, ContentHash: contentHash}}, bridgeprotocol.DesiredManifest{MCPServers: []bridgeprotocol.MCPMember{server}})
	if len(diff.Add) != 0 || len(diff.Replace) != 0 || len(diff.Delete) != 0 {
		t.Fatalf("relay diff=%+v", diff)
	}
}

type blockingSaltRunner struct {
	pollStarted chan struct{}
	releasePoll chan struct{}
	once        sync.Once
}

func (runner *blockingSaltRunner) CombinedOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case name == "salt" && strings.Contains(joined, "test.version"):
		return []byte(`{"minion":"3008.1"}`), nil
	case name == "salt" && (strings.Contains(joined, "saltutil.sync_modules") || strings.Contains(joined, "saltutil.sync_states")):
		return []byte(`{"minion":[]}`), nil
	case name == "salt" && strings.Contains(joined, "user.info"):
		return []byte(`{"minion":{"name":"managed","home":"/home/managed"}}`), nil
	case name == "salt-cp":
		return []byte(`{"minion":true}`), nil
	case name == "salt" && strings.Contains(joined, "--async"):
		return []byte(`{"minion":"20260730120000000000"}`), nil
	case name == "salt-run" && strings.Contains(joined, "jobs.lookup_jid"):
		runner.once.Do(func() { close(runner.pollStarted) })
		<-runner.releasePoll
		return []byte(`{"minion":{"ok":true,"status":"succeeded","health":"healthy","targetRevision":"revision"}}`), nil
	case name == "salt" && strings.Contains(joined, "toolhub.cleanup_bundle"):
		return []byte(`{"minion":{"ok":true}}`), nil
	default:
		return []byte(`{"minion":true}`), nil
	}
}

func TestSaltMutationPersistsJIDBeforePolling(t *testing.T) {
	journal, err := OpenJournal(t.TempDir() + "/journal.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	runner := &blockingSaltRunner{pollStarted: make(chan struct{}), releasePoll: make(chan struct{})}
	driver := saltdriver.New(runner)
	driver.StateRoot = t.TempDir()
	driver.StagingRoot = t.TempDir()
	driver.PollEvery = time.Millisecond
	adapter := &CompositeAdapter{Journal: journal, Salt: driver}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindSalt, SaltMinionID: "minion", Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: "managed"}
	operationID := uuid.NewString()
	done := make(chan error, 1)
	go func() {
		_, mutationErr := adapter.saltMutation(context.Background(), target, operationID, "toolhub.apply", map[string]any{"target": target})
		done <- mutationErr
	}()

	select {
	case <-runner.pollStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("Salt Poll was not reached")
	}
	operation, err := journal.Operation(operationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != bridgeprotocol.OperationRunning || len(operation.Targets) != 1 || operation.Targets[0].SaltJID != "20260730120000000000" {
		t.Fatalf("persisted operation=%+v", operation)
	}
	recoveries, err := journal.SaltRecoveries()
	if err != nil || len(recoveries) != 1 {
		t.Fatalf("recoveries=%+v err=%v", recoveries, err)
	}
	if recoveries[0].Target.ManagedHome != "" {
		t.Fatalf("managed home leaked into durable recovery record: %+v", recoveries[0].Target)
	}
	close(runner.releasePoll)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	operation, err = journal.Operation(operationID)
	if err != nil || operation.Targets[0].Result == nil || operation.Targets[0].Result.Status != bridgeprotocol.OperationSucceeded {
		t.Fatalf("terminal safe result was not persisted: %+v err=%v", operation, err)
	}
}

func TestRecoverOperationsResumesPersistedSaltJID(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	runner := &blockingSaltRunner{pollStarted: make(chan struct{}), releasePoll: make(chan struct{})}
	close(runner.releasePoll)
	driver := saltdriver.New(runner)
	driver.StagingRoot = t.TempDir()
	driver.PollEvery = time.Millisecond
	localBundle := filepath.Join(driver.StagingRoot, "recovery.json")
	if err := os.WriteFile(localBundle, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindSalt, SaltMinionID: "minion", Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: "managed"}
	now := time.Now().UTC()
	operation := bridgeprotocol.Operation{ID: uuid.NewString(), Kind: "toolhub.apply", Status: bridgeprotocol.OperationRunning, Targets: []bridgeprotocol.OperationTarget{{TargetID: target.ID, Status: bridgeprotocol.OperationRunning, SaltJID: "20260730120000000000"}}, CreatedAt: now, UpdatedAt: now}
	recovery := saltRecoveryRecord{OperationID: operation.ID, Function: operation.Kind, Target: target, SaltJID: operation.Targets[0].SaltJID, LocalBundle: localBundle, RemoteBundle: "/var/cache/salt/minion/toolhub-staging/recovery.json", CanVerifySnapshot: true, CreatedAt: now}
	if err := journal.PutRunningSaltOperation(operation, recovery); err != nil {
		t.Fatal(err)
	}
	adapter := &CompositeAdapter{Journal: journal, Salt: driver}
	if err := adapter.RecoverOperations(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	recovered, err := journal.Operation(operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != bridgeprotocol.OperationSucceeded || len(recovered.Targets) != 1 || recovered.Targets[0].SaltJID != recovery.SaltJID || recovered.Targets[0].Result == nil || recovered.Targets[0].Result.Status != bridgeprotocol.OperationSucceeded {
		t.Fatalf("recovered operation=%+v", recovered)
	}
	items, err := journal.SaltRecoveries()
	if err != nil || len(items) != 0 {
		t.Fatalf("recovery records=%+v err=%v", items, err)
	}
	if _, err := os.Stat(localBundle); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged local bundle was not removed: %v", err)
	}
}

func TestSaltRecoveryFingerprintMatchesPythonCanonicalJSON(t *testing.T) {
	hash, err := pythonJSONHash(map[string]any{"args": []string{}, "command": "tool", "env": map[string]string{"TOKEN": "sécret"}})
	if err != nil {
		t.Fatal(err)
	}
	if hash != "44f53d39ff611f861a3ba8138c35f407eeaf42150c5c3027a3a679aa58045f0e" {
		t.Fatalf("recovery fingerprint=%s", hash)
	}
}

func TestTargetMatchesExpectedPreservesOnlyReconcileExtras(t *testing.T) {
	expected := []saltMemberFingerprint{{Kind: "skill", Name: "managed", ContentHash: strings.Repeat("a", 64)}}
	current := []bridgeprotocol.InventoryMember{
		{Kind: "skill", Name: "managed", ContentHash: strings.Repeat("a", 64)},
		{Kind: "skill", Name: "later-user-entry", ContentHash: strings.Repeat("b", 64)},
		{Kind: "skill", Name: ".system", Protected: true},
	}
	if targetMatchesExpected(current, expected, false) {
		t.Fatal("destructive Apply recovery accepted an unmanaged member")
	}
	if !targetMatchesExpected(current, expected, true) {
		t.Fatal("reconcile recovery rejected a later unmanaged member")
	}
}
