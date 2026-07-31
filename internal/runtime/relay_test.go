package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

type fakeRelayController struct {
	state       string
	failRestart bool
	actions     []string
}

func (f *fakeRelayController) Action(_ context.Context, action string) (string, error) {
	f.actions = append(f.actions, action)
	switch action {
	case "status":
		if f.state == "" {
			return "inactive", errors.New("inactive")
		}
		return f.state, nil
	case "restart", "start":
		if f.failRestart {
			return f.state, errors.New("injected restart failure")
		}
		f.state = "active"
		return f.state, nil
	case "stop":
		f.state = "inactive"
		return f.state, nil
	default:
		return "", errors.New("unsupported action")
	}
}

func TestRelayReconcileNoOpCreatesNoBackup(t *testing.T) {
	manager, controller, user, target, port := relayFixture(t, true)
	manifest := relayManifest(target, port, "https://example.invalid/one")
	first := applyRelay(t, manager, user, manifest, "apply", false)
	if first.BackupID == "" {
		t.Fatal("explicit Apply did not create a backup")
	}
	beforeActions := len(controller.actions)
	result := applyRelay(t, manager, user, manifest, "reconcile", false)
	if result.BackupID != "" || result.Repaired {
		t.Fatalf("no-op reconcile result=%+v", result)
	}
	if len(controller.actions) != beforeActions+1 || controller.actions[len(controller.actions)-1] != "status" {
		t.Fatalf("no-op reconcile actions=%v", controller.actions[beforeActions:])
	}
	entries, err := os.ReadDir(filepath.Join(manager.BackupRoot, target.ID))
	if err != nil || len(entries) != 1 {
		t.Fatalf("backup count=%d err=%v", len(entries), err)
	}
}

func TestRelayPausedReconcileRepairsConfigWithoutRestart(t *testing.T) {
	manager, controller, user, target, port := relayFixture(t, false)
	manifest := relayManifest(target, port, "https://example.invalid/one")
	paths, _ := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	writeTestFile(t, paths.MCPMRegistry, []byte(`{"server":{"profile_tags":["toolhub"],"url":"https://drift.invalid","transport":"http"}}`))
	writeTestFile(t, paths.ClaudeConfig, []byte(`{"mcpServers":{"toolhub-relay":{"type":"http","url":"http://127.0.0.1:1/mcp"}}}`))
	writeTestFile(t, paths.CodexConfig, []byte("[mcp_servers.toolhub-relay]\nurl = 'http://127.0.0.1:1/mcp'\n"))
	writeTestFile(t, manager.EnvironmentFile, []byte("TOOLHUB_RELAY_PORT=1\n"))

	result := applyRelay(t, manager, user, manifest, "reconcile", true)
	if !result.Repaired || result.BackupID == "" {
		t.Fatalf("paused repair result=%+v", result)
	}
	for _, action := range controller.actions {
		if action == "restart" || action == "start" {
			t.Fatalf("paused reconcile started relay: %v", controller.actions)
		}
	}
	if body, _ := os.ReadFile(manager.EnvironmentFile); string(body) != "TOOLHUB_RELAY_PORT="+strconv.Itoa(port)+"\n" {
		t.Fatalf("invalid relay environment %q", body)
	}
}

func TestRelayApplyRollbackRestoresFiles(t *testing.T) {
	manager, controller, user, target, port := relayFixture(t, true)
	firstManifest := relayManifest(target, port, "https://example.invalid/one")
	applyRelay(t, manager, user, firstManifest, "apply", false)
	paths, _ := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	files := []string{paths.MCPMRegistry, paths.ClaudeConfig, paths.CodexConfig, manager.EnvironmentFile}
	before := map[string][]byte{}
	for _, path := range files {
		before[path], _ = os.ReadFile(path)
	}
	controller.failRestart = true
	current, _ := (&Manager{}).scanRelay(paths)
	request := bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "apply", Target: target, ExpectedRevision: current.Revision, Manifest: relayManifest(target, port, "https://example.invalid/two")}
	_, err := manager.Apply(context.Background(), user, request)
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrRelayUnhealthy {
		t.Fatalf("Apply error=%v", err)
	}
	for _, path := range files {
		after, readErr := os.ReadFile(path)
		if readErr != nil || string(after) != string(before[path]) {
			t.Fatalf("rollback mismatch for %s: err=%v", path, readErr)
		}
	}
}

func TestRelayApplyWaitsForHealthReadiness(t *testing.T) {
	manager, _, user, target, port := relayFixture(t, false)
	manager.readinessInterval = time.Millisecond
	manager.readinessTimeout = 100 * time.Millisecond
	attempts := 0
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("relay is still starting")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}

	result := applyRelay(t, manager, user, relayManifest(target, port, "https://example.invalid/one"), "apply", false)
	if result.Health != bridgeprotocol.HealthHealthy || attempts != 3 {
		t.Fatalf("Apply result=%+v health attempts=%d", result, attempts)
	}
}

func TestRelayRestorePinsSelectedBackupContent(t *testing.T) {
	manager, _, user, target, port := relayFixture(t, true)
	firstManifest := relayManifest(target, port, "https://example.invalid/one")
	applyRelay(t, manager, user, firstManifest, "apply", false)
	second := applyRelay(t, manager, user, relayManifest(target, port, "https://example.invalid/two"), "apply", false)
	paths, _ := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	current, _ := (&Manager{}).scanRelay(paths)
	manager.readinessInterval = time.Millisecond
	manager.readinessTimeout = 100 * time.Millisecond
	attempts := 0
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("restored relay is still starting")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	result, err := manager.Restore(context.Background(), user, bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "restore", Target: target, ExpectedRevision: current.Revision, BackupID: second.BackupID, Manifest: firstManifest})
	if err != nil || result.BackupID == "" {
		t.Fatalf("Restore result=%+v err=%v", result, err)
	}
	registry, err := readJSONMap(paths.MCPMRegistry)
	if err != nil {
		t.Fatal(err)
	}
	server := registry["server"].(map[string]any)
	if server["url"] != "https://example.invalid/one" {
		t.Fatalf("restored registry=%v", registry)
	}
	if attempts != 3 {
		t.Fatalf("Restore health attempts=%d", attempts)
	}
}

func TestValidateMCPMRejectsUnparseableVersion(t *testing.T) {
	bin := t.TempDir()
	mcpm := filepath.Join(bin, "mcpm")
	if err := os.WriteFile(mcpm, []byte("#!/bin/sh\nprintf 'development build\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager := NewRelayManager(&fakeRelayController{}, t.TempDir())
	manager.MCPMPath = mcpm
	_, err := manager.ValidateMCPM(context.Background())
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrMCPMIncompatible {
		t.Fatalf("ValidateMCPM error=%v", err)
	}
}

func TestSharedRelayScanRejectsSymlinkParent(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	paths, err := PathsFor(ManagedUser{Home: home}, bridgeprotocol.RuntimeSharedRelay)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSharedRelay(paths); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ScanSharedRelay error=%v", err)
	}
}

func TestSharedRelayScanProjectsContentHashOnlyForIntactRuntimeEntry(t *testing.T) {
	home := t.TempDir()
	paths, err := PathsFor(ManagedUser{Home: home}, bridgeprotocol.RuntimeSharedRelay)
	if err != nil {
		t.Fatal(err)
	}
	secretID := uuid.NewString()
	server := bridgeprotocol.MCPMember{
		MemberID:    uuid.NewString(),
		ServerID:    uuid.NewString(),
		Revision:    1,
		Name:        "server",
		Transport:   "stdio",
		Command:     "/usr/bin/server",
		Args:        []string{"serve"},
		EnvRefs:     map[string]string{"TOKEN": secretID},
		ContentHash: strings.Repeat("a", 64),
	}
	entry := expectedRelayEntry(server, map[string]string{secretID: "initial-secret"})
	writeRelayRegistry(t, paths.MCPMRegistry, entry)

	members, err := ScanSharedRelay(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].ContentHash != server.ContentHash {
		t.Fatalf("intact relay inventory=%+v", members)
	}
	encodedInventory, _ := json.Marshal(members)
	if strings.Contains(string(encodedInventory), "initial-secret") {
		t.Fatal("relay inventory exposed a plaintext secret")
	}

	entry["env"].(map[string]string)["TOKEN"] = "drifted-secret"
	writeRelayRegistry(t, paths.MCPMRegistry, entry)
	members, err = ScanSharedRelay(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].ContentHash == server.ContentHash || !bridgeprotocol.IsSHA256(members[0].ContentHash) {
		t.Fatalf("drifted relay inventory=%+v", members)
	}
	encodedInventory, _ = json.Marshal(members)
	if strings.Contains(string(encodedInventory), "drifted-secret") {
		t.Fatal("drifted relay inventory exposed a plaintext secret")
	}
}

func relayFixture(t *testing.T, healthy bool) (*RelayManager, *fakeRelayController, ManagedUser, bridgeprotocol.Target, int) {
	t.Helper()
	bin := t.TempDir()
	mcpm := filepath.Join(bin, "mcpm")
	if err := os.WriteFile(mcpm, []byte("#!/bin/sh\nprintf 'mcpm 1.0\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	user := ManagedUser{Name: "test", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	controller := &fakeRelayController{state: "inactive"}
	port := 6276
	if healthy {
		controller.state = "active"
	}
	manager := NewRelayManager(controller, t.TempDir())
	manager.MCPMPath = mcpm
	manager.EnvironmentFile = filepath.Join(t.TempDir(), "mcpm-relay.env")
	manager.portAvailable = func(int) error { return nil }
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeSharedRelay, ManagedUsername: user.Name}
	return manager, controller, user, target, port
}

func relayManifest(target bridgeprotocol.Target, port int, endpoint string) bridgeprotocol.DesiredManifest {
	memberID := uuid.NewString()
	return bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: target, MCPServers: []bridgeprotocol.MCPMember{{MemberID: memberID, ServerID: uuid.NewString(), Revision: 1, Name: "server", Transport: "http", URL: endpoint, ContentHash: strings.Repeat("a", 64)}}, Skills: []bridgeprotocol.SkillMember{}, ManagedMemberIDs: []string{memberID}, RelayPort: port}
}

func applyRelay(t *testing.T, manager *RelayManager, user ManagedUser, manifest bridgeprotocol.DesiredManifest, kind string, paused bool) bridgeprotocol.TargetResult {
	t.Helper()
	paths, _ := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	current, err := (&Manager{}).scanRelay(paths)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Apply(context.Background(), user, bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: kind, Target: manifest.Target, ExpectedRevision: current.Revision, Manifest: manifest, IntentionalPaused: paused})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func writeTestFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func writeRelayRegistry(t *testing.T, path string, entry map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"server": entry})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, body)
}
