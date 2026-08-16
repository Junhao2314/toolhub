package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	state      string
	enabled    bool
	failStarts int
	actions    []string
}

func (f *fakeRelayController) Action(_ context.Context, action string) (string, error) {
	f.actions = append(f.actions, action)
	switch action {
	case "status":
		if f.state == "" {
			return "inactive", errors.New("inactive")
		}
		return f.state, nil
	case "is-enabled":
		if f.enabled {
			return "enabled", nil
		}
		return "disabled", errors.New("disabled")
	case "enable":
		f.enabled = true
		return "enabled", nil
	case "disable":
		f.enabled = false
		return "disabled", nil
	case "restart", "start", "start-unit":
		if f.failStarts > 0 {
			f.failStarts--
			return f.state, errors.New("injected restart failure")
		}
		if action == "start" {
			f.enabled = true
		}
		f.state = "active"
		return f.state, nil
	case "stop", "stop-unit":
		f.state = "inactive"
		if action == "stop" {
			f.enabled = false
		}
		return f.state, nil
	default:
		return "", errors.New("unsupported action")
	}
}

type fakeMCPMAdmin struct {
	reloadStatuses []bridgeprotocol.RelayAdminStatus
	reloadErrors   []error
	reloadCalls    int
	capability     *bridgeprotocol.RelayCapabilityResponse
	capabilityErr  error
	status         bridgeprotocol.RelayAdminStatus
	statusErr      error
	observation    bridgeprotocol.ContractObservationResponse
	observationErr error
	autoManifest   bool
}

func (f *fakeMCPMAdmin) Capability(context.Context) (bridgeprotocol.RelayCapabilityResponse, error) {
	if f.capabilityErr != nil {
		return bridgeprotocol.RelayCapabilityResponse{}, f.capabilityErr
	}
	if f.capability != nil {
		return *f.capability, nil
	}
	return bridgeprotocol.RelayCapabilityResponse{AdminProtocolVersion: 1, Features: append([]string(nil), requiredMCPMFeatures...), RoutingSchemaVersions: []int{1}, Runtime: "mcpm", RuntimeVersion: "1.0.0-toolhub.1"}, nil
}
func (f *fakeMCPMAdmin) ReloadRouting(context.Context) (bridgeprotocol.RelayAdminStatus, error) {
	index := f.reloadCalls
	f.reloadCalls++
	if index < len(f.reloadErrors) && f.reloadErrors[index] != nil {
		return bridgeprotocol.RelayAdminStatus{}, f.reloadErrors[index]
	}
	if index < len(f.reloadStatuses) {
		return f.reloadStatuses[index], nil
	}
	return f.status, nil
}
func (f *fakeMCPMAdmin) Status(context.Context) (bridgeprotocol.RelayAdminStatus, error) {
	if f.statusErr != nil {
		return bridgeprotocol.RelayAdminStatus{}, f.statusErr
	}
	if len(f.reloadStatuses) == 0 {
		return f.status, nil
	}
	return f.reloadStatuses[len(f.reloadStatuses)-1], nil
}
func (f *fakeMCPMAdmin) ObserveContracts(context.Context) (bridgeprotocol.ContractObservationResponse, error) {
	return f.observation, f.observationErr
}
func (f *fakeMCPMAdmin) ListConfirmations(context.Context) (bridgeprotocol.ConfirmationListResponse, error) {
	return bridgeprotocol.ConfirmationListResponse{Items: []bridgeprotocol.ConfirmationSummary{}}, nil
}
func (f *fakeMCPMAdmin) DecideConfirmation(context.Context, bool, bridgeprotocol.ConfirmationDecisionRequest) (bridgeprotocol.ConfirmationDecisionResponse, error) {
	return bridgeprotocol.ConfirmationDecisionResponse{}, nil
}
func (f *fakeMCPMAdmin) DrainObservations(context.Context, bridgeprotocol.ObservationDrainRequest) (bridgeprotocol.ObservationDrainResponse, error) {
	return bridgeprotocol.ObservationDrainResponse{Items: []bridgeprotocol.Observation{}}, nil
}

func (f *fakeMCPMAdmin) configureManifest(t *testing.T, manifest bridgeprotocol.DesiredManifest) {
	t.Helper()
	f.status = bridgeprotocol.RelayAdminStatus{PublishedProfileRevisions: []bridgeprotocol.RelayAdminProfileRevision{}}
	f.observation = bridgeprotocol.ContractObservationResponse{Servers: make([]bridgeprotocol.ContractServerObservation, 0, len(manifest.MCPServers))}
	configRevisions := map[string]string{}
	if manifest.RelayGovernance != nil {
		var bundle bridgeprotocol.RoutingBundle
		if err := bridgeprotocol.DecodeGovernanceBody(manifest.RelayGovernance.RoutingBundle, &bundle); err != nil {
			t.Fatal(err)
		}
		f.status.Mode = bundle.Mode
		f.status.RelayConfigurationRevisionID = bundle.RelayConfigurationRevisionID
		f.status.GlobalPolicyRevisionID = bundle.GlobalPolicyRevisionID
		f.status.RoutingBundleHash = manifest.RelayGovernance.RoutingHash
		f.observation.RelayConfigurationRevisionID = bundle.RelayConfigurationRevisionID
		for _, server := range bundle.Servers {
			configRevisions[server.ServerID] = server.MCPConfigRevisionID
		}
		for _, profile := range bundle.Profiles {
			f.status.PublishedProfileRevisions = append(f.status.PublishedProfileRevisions, bridgeprotocol.RelayAdminProfileRevision{ProfileID: profile.ProfileID, ProfileRevisionID: profile.ProfileRevisionID, ProfileRevisionHash: profile.ProfileRevisionHash})
		}
	}
	for _, member := range manifest.MCPServers {
		f.observation.Servers = append(f.observation.Servers, bridgeprotocol.ContractServerObservation{ServerID: member.ServerID, ServerName: member.Name, MCPConfigRevisionID: configRevisions[member.ServerID], Tools: []bridgeprotocol.ContractToolDTO{}})
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
	if len(controller.actions) != beforeActions+2 || controller.actions[len(controller.actions)-2] != "status" || controller.actions[len(controller.actions)-1] != "is-enabled" {
		t.Fatalf("no-op reconcile actions=%v", controller.actions[beforeActions:])
	}
	entries, err := os.ReadDir(filepath.Join(manager.BackupRoot, target.ID))
	if err != nil || len(entries) != 1 {
		t.Fatalf("backup count=%d err=%v", len(entries), err)
	}
}

func TestRelayPausedReconcileRepairsConfigWithoutRestart(t *testing.T) {
	manager, controller, user, target, port := relayFixture(t, true)
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
		if action == "restart" || action == "start" || action == "start-unit" {
			t.Fatalf("paused reconcile started relay: %v", controller.actions)
		}
	}
	if controller.state != "inactive" || controller.enabled {
		t.Fatalf("paused reconcile did not disable and stop relay: state=%q enabled=%v actions=%v", controller.state, controller.enabled, controller.actions)
	}
	if body, _ := os.ReadFile(manager.EnvironmentFile); string(body) != "TOOLHUB_RELAY_PORT="+strconv.Itoa(port)+"\n" {
		t.Fatalf("invalid relay environment %q", body)
	}
}

func TestRelayApplyRuntimeFailureRollsBackFilesAndOldProcess(t *testing.T) {
	manager, controller, user, target, port := relayFixture(t, true)
	firstManifest := relayManifestV2(t, target, port, "https://example.invalid/one")
	applyRelay(t, manager, user, firstManifest, "apply", false)
	paths, _ := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	files := []string{paths.MCPMRegistry, paths.ClaudeConfig, paths.CodexConfig, paths.HermesConfig, paths.RoutingFile, manager.EnvironmentFile}
	before := map[string][]byte{}
	for _, path := range files {
		before[path], _ = os.ReadFile(path)
	}
	controller.failStarts = 1
	current, _ := (&Manager{}).scanRelay(paths)
	changed := firstManifest
	changed.MCPServers = append([]bridgeprotocol.MCPMember(nil), firstManifest.MCPServers...)
	changed.MCPServers[0].URL = "https://example.invalid/two"
	changed = withRelayGovernance(t, changed, "00000000-0000-0000-0000-000000000010", strings.Repeat("c", 64), "00000000-0000-0000-0000-000000000020", strings.Repeat("d", 64))
	request := bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "apply", Target: target, ExpectedRevision: current.Revision, Manifest: changed}
	if result, err := manager.Apply(context.Background(), user, request); err == nil || result.Status == bridgeprotocol.OperationSucceeded {
		t.Fatalf("Apply result=%+v error=%v", result, err)
	}
	for _, path := range files {
		after, readErr := os.ReadFile(path)
		if readErr != nil || string(after) != string(before[path]) {
			t.Fatalf("rollback mismatch for %s: err=%v body=%s", path, readErr, after)
		}
	}
	if controller.state != "active" || !controller.enabled {
		t.Fatalf("old relay process was not restored: state=%q enabled=%v actions=%v", controller.state, controller.enabled, controller.actions)
	}
}

func TestRelayApplyMCPMContractFailureDoesNotWriteConfiguration(t *testing.T) {
	manager, _, user, target, port := relayFixture(t, false)
	manager.MCPMPath = filepath.Join(t.TempDir(), "missing-mcpm")
	paths, _ := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	current, _ := (&Manager{}).scanRelay(paths)
	request := bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "apply", Target: target, ExpectedRevision: current.Revision, Manifest: relayManifestV2(t, target, port, "https://example.invalid/one")}
	_, err := manager.Apply(context.Background(), user, request)
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrMCPMMissing {
		t.Fatalf("Apply contract error=%v", err)
	}
	for _, path := range []string{paths.MCPMRegistry, paths.ClaudeConfig, paths.CodexConfig, paths.HermesConfig, paths.RoutingFile, manager.EnvironmentFile} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("contract failure wrote %s: %v", path, statErr)
		}
	}
}

func TestRelayProfileOnlyApplyHotReloadsWithoutRestart(t *testing.T) {
	manager, controller, user, target, port := relayFixture(t, true)
	first := relayManifestV2(t, target, port, "https://example.invalid/one")
	applyRelay(t, manager, user, first, "apply", false)
	second := withRelayGovernance(t, first, first.RelayGovernance.RelayConfigurationRevisionID, first.RelayGovernance.RelayConfigurationHash, "00000000-0000-0000-0000-000000000099", strings.Repeat("e", 64))
	admin := &fakeMCPMAdmin{reloadStatuses: []bridgeprotocol.RelayAdminStatus{{Mode: "compatibility", RelayConfigurationRevisionID: second.RelayGovernance.RelayConfigurationRevisionID, GlobalPolicyRevisionID: "00000000-0000-0000-0000-000000000099", RoutingBundleHash: second.RelayGovernance.RoutingHash, PublishedProfileRevisions: []bridgeprotocol.RelayAdminProfileRevision{}}}}
	manager.Admin = admin
	controller.actions = nil
	result := applyRelay(t, manager, user, second, "apply", false)
	if result.Status != bridgeprotocol.OperationSucceeded || result.Health != bridgeprotocol.HealthHealthy || admin.reloadCalls != 1 {
		t.Fatalf("profile-only result=%+v reloadCalls=%d", result, admin.reloadCalls)
	}
	for _, action := range controller.actions {
		if action == "stop" || action == "stop-unit" || action == "start" || action == "start-unit" {
			t.Fatalf("profile-only Apply restarted relay: %v", controller.actions)
		}
	}
	paths, _ := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	body, err := os.ReadFile(paths.RoutingFile)
	if err != nil || string(body) != string(second.RelayGovernance.RoutingBundle) {
		t.Fatalf("routing file=%s err=%v", body, err)
	}
}

func TestRelayFirstGovernanceDeliveryRestartsInsteadOfHotReload(t *testing.T) {
	manager, controller, user, target, port := relayFixture(t, true)
	legacy := relayManifest(target, port, "https://example.invalid/one")
	applyRelay(t, manager, user, legacy, "apply", false)
	governed := withRelayGovernance(t, legacy, "00000000-0000-0000-0000-000000000010", strings.Repeat("c", 64), "00000000-0000-0000-0000-000000000020", strings.Repeat("d", 64))
	admin := manager.Admin.(*fakeMCPMAdmin)
	controller.actions = nil
	admin.reloadCalls = 0
	result := applyRelay(t, manager, user, governed, "apply", false)
	if result.Status != bridgeprotocol.OperationSucceeded || result.Health != bridgeprotocol.HealthHealthy {
		t.Fatalf("first governed Apply result=%+v", result)
	}
	if admin.reloadCalls != 0 {
		t.Fatalf("first governed Apply hot reloaded without an old routing bundle: reloadCalls=%d", admin.reloadCalls)
	}
	actions := strings.Join(controller.actions, ",")
	if !strings.Contains(actions, "enable,stop-unit,start-unit") {
		t.Fatalf("first governed Apply did not use fixed restart: actions=%v", controller.actions)
	}
}

func TestRelayProfileOnlyReloadFailureRestoresOldRoutingBundle(t *testing.T) {
	manager, controller, user, target, port := relayFixture(t, true)
	first := relayManifestV2(t, target, port, "https://example.invalid/one")
	applyRelay(t, manager, user, first, "apply", false)
	second := withRelayGovernance(t, first, first.RelayGovernance.RelayConfigurationRevisionID, first.RelayGovernance.RelayConfigurationHash, "00000000-0000-0000-0000-000000000099", strings.Repeat("e", 64))
	manager.Admin = &fakeMCPMAdmin{
		reloadErrors:   []error{errors.New("injected reload failure"), nil},
		reloadStatuses: []bridgeprotocol.RelayAdminStatus{{}, {Mode: "compatibility", RelayConfigurationRevisionID: first.RelayGovernance.RelayConfigurationRevisionID, RoutingBundleHash: first.RelayGovernance.RoutingHash, PublishedProfileRevisions: []bridgeprotocol.RelayAdminProfileRevision{}}},
	}
	controller.actions = nil
	paths, _ := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	current, _ := (&Manager{}).scanRelay(paths)
	request := bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "apply", Target: target, ExpectedRevision: current.Revision, Manifest: second}
	if _, err := manager.Apply(context.Background(), user, request); err == nil {
		t.Fatal("profile-only reload failure unexpectedly succeeded")
	}
	body, err := os.ReadFile(paths.RoutingFile)
	if err != nil || string(body) != string(first.RelayGovernance.RoutingBundle) {
		t.Fatalf("old routing bundle was not restored: body=%s err=%v", body, err)
	}
	for _, action := range controller.actions {
		if action == "stop" || action == "stop-unit" || action == "start" || action == "start-unit" {
			t.Fatalf("recoverable hot reload failure restarted relay: %v", controller.actions)
		}
	}
}

func TestRelayBackupRecordsRoutingFileMetadata(t *testing.T) {
	manager, _, user, target, port := relayFixture(t, true)
	first := relayManifestV2(t, target, port, "https://example.invalid/one")
	applyRelay(t, manager, user, first, "apply", false)
	changed := first
	changed.MCPServers = append([]bridgeprotocol.MCPMember(nil), first.MCPServers...)
	changed.MCPServers[0].URL = "https://example.invalid/two"
	changed = withRelayGovernance(t, changed, "00000000-0000-0000-0000-000000000010", strings.Repeat("c", 64), "00000000-0000-0000-0000-000000000020", strings.Repeat("d", 64))
	result := applyRelay(t, manager, user, changed, "apply", false)
	backupDirectory := filepath.Join(manager.BackupRoot, target.ID, result.BackupID)
	routing, err := os.ReadFile(filepath.Join(backupDirectory, "routing"))
	if err != nil || string(routing) != string(first.RelayGovernance.RoutingBundle) {
		t.Fatalf("routing backup=%s err=%v", routing, err)
	}
	state, err := os.ReadFile(filepath.Join(backupDirectory, "state.json"))
	if err != nil || !bytes.Contains(state, []byte(`"routing":{"exists":true,"mode":384`)) || !bytes.Contains(state, []byte(first.RelayGovernance.RoutingHash)) {
		t.Fatalf("routing backup metadata=%s err=%v", state, err)
	}
}

func TestRelayApplyWriteFailureRollsBackConfiguration(t *testing.T) {
	manager, _, user, target, port := relayFixture(t, true)
	firstManifest := relayManifest(target, port, "https://example.invalid/one")
	applyRelay(t, manager, user, firstManifest, "apply", false)
	paths, _ := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	before, err := backupRelayFiles(paths, manager.EnvironmentFile)
	if err != nil {
		t.Fatal(err)
	}
	manager.EnvironmentFile = filepath.Join("/proc", "toolhub-relay-env-"+uuid.NewString())
	current, _ := (&Manager{}).scanRelay(paths)
	request := bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "apply", Target: target, ExpectedRevision: current.Revision, Manifest: relayManifest(target, port, "https://example.invalid/two")}
	if _, err := manager.Apply(context.Background(), user, request); err == nil {
		t.Fatal("Apply unexpectedly succeeded with an unwritable environment path")
	}
	for _, path := range []string{paths.MCPMRegistry, paths.ClaudeConfig, paths.CodexConfig, paths.HermesConfig} {
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != string(before[path].Body) {
			t.Fatalf("rollback mismatch for %s: err=%v", path, readErr)
		}
	}
}

func TestRelayApplyWaitsForHealthReadiness(t *testing.T) {
	manager, _, user, target, port := relayFixture(t, false)
	manager.readinessInterval = time.Millisecond
	manager.readinessTimeout = 100 * time.Millisecond
	readinessAttempts := 0
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			readinessAttempts++
		}
		if request.Method == http.MethodGet && readinessAttempts < 3 {
			return nil, errors.New("relay is still starting")
		}
		return healthyRelayResponse(request)
	})}

	result := applyRelay(t, manager, user, relayManifest(target, port, "https://example.invalid/one"), "apply", false)
	if result.Health != bridgeprotocol.HealthHealthy || readinessAttempts != 3 {
		t.Fatalf("Apply result=%+v health attempts=%d", result, readinessAttempts)
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
	readinessAttempts := 0
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			readinessAttempts++
		}
		if request.Method == http.MethodGet && readinessAttempts < 3 {
			return nil, errors.New("restored relay is still starting")
		}
		return healthyRelayResponse(request)
	})}
	manager.Admin.(*fakeMCPMAdmin).configureManifest(t, firstManifest)
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
	if readinessAttempts != 3 {
		t.Fatalf("Restore health attempts=%d", readinessAttempts)
	}
}

func TestRelayRestoreRuntimeFailureRollsBackFilesAndOldProcess(t *testing.T) {
	manager, controller, user, target, port := relayFixture(t, true)
	first := relayManifestV2(t, target, port, "https://example.invalid/one")
	applyRelay(t, manager, user, first, "apply", false)
	second := first
	second.MCPServers = append([]bridgeprotocol.MCPMember(nil), first.MCPServers...)
	second.MCPServers[0].URL = "https://example.invalid/two"
	second = withRelayGovernance(t, second, "00000000-0000-0000-0000-000000000010", strings.Repeat("c", 64), "00000000-0000-0000-0000-000000000020", strings.Repeat("d", 64))
	secondResult := applyRelay(t, manager, user, second, "apply", false)
	paths, _ := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	files := []string{paths.MCPMRegistry, paths.ClaudeConfig, paths.CodexConfig, paths.HermesConfig, paths.RoutingFile, manager.EnvironmentFile}
	before := map[string][]byte{}
	for _, path := range files {
		before[path], _ = os.ReadFile(path)
	}
	controller.failStarts = 1
	current, _ := (&Manager{}).scanRelay(paths)
	request := bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "restore", Target: target, ExpectedRevision: current.Revision, BackupID: secondResult.BackupID, Manifest: first}
	if result, err := manager.Restore(context.Background(), user, request); err == nil || result.Status == bridgeprotocol.OperationSucceeded {
		t.Fatalf("Restore result=%+v err=%v", result, err)
	}
	for _, path := range files {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before[path]) {
			t.Fatalf("restore rollback mismatch for %s: err=%v body=%s", path, err, after)
		}
	}
	if controller.state != "active" || !controller.enabled {
		t.Fatalf("old relay process was not restored: state=%q enabled=%v actions=%v", controller.state, controller.enabled, controller.actions)
	}
}

func TestRelayRestorePostWriteScanFailureRestoresOldProcessAndPort(t *testing.T) {
	manager, controller, user, target, requestedPort := relayFixture(t, true)
	first := relayManifest(target, requestedPort, "https://example.invalid/one")
	applyRelay(t, manager, user, first, "apply", false)
	oldPort := requestedPort + 1
	second := relayManifest(target, oldPort, "https://example.invalid/two")
	selected := applyRelay(t, manager, user, second, "apply", false)
	paths, err := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	if err != nil {
		t.Fatal(err)
	}
	current, err := (&Manager{}).scanRelay(paths)
	if err != nil {
		t.Fatal(err)
	}
	portChecks := []int{}
	manager.portAvailable = func(port int) error {
		portChecks = append(portChecks, port)
		return nil
	}
	scans := 0
	manager.scanRelayState = func(paths TargetPaths) (ScanResult, error) {
		scans++
		if scans == 2 {
			return ScanResult{}, errors.New("injected post-write scan failure")
		}
		return (&Manager{}).scanRelay(paths)
	}
	request := bridgeprotocol.CommitRequest{
		OperationID: uuid.NewString(), OperationKind: "restore", Target: target, ExpectedRevision: current.Revision,
		BackupID: selected.BackupID, Manifest: first, IntentionalPaused: true,
	}
	if result, err := manager.Restore(context.Background(), user, request); err == nil || result.Status == bridgeprotocol.OperationSucceeded {
		t.Fatalf("Restore result=%+v err=%v", result, err)
	}
	if controller.state != "active" || !controller.enabled {
		t.Fatalf("old relay process was not restored: state=%q enabled=%v actions=%v", controller.state, controller.enabled, controller.actions)
	}
	if len(portChecks) != 1 || portChecks[0] != oldPort {
		t.Fatalf("process recovery ports=%v want [%d]", portChecks, oldPort)
	}
	environment, err := os.ReadFile(manager.EnvironmentFile)
	if err != nil || string(environment) != "TOOLHUB_RELAY_PORT="+strconv.Itoa(oldPort)+"\n" {
		t.Fatalf("old relay environment was not restored: body=%s err=%v", environment, err)
	}
}

func TestRelayRestoreProcessPreservesRunningAndEnabledState(t *testing.T) {
	tests := []struct {
		name     string
		previous relayProcessSnapshot
	}{
		{name: "running and enabled", previous: relayProcessSnapshot{Running: true, Enabled: true}},
		{name: "running and disabled", previous: relayProcessSnapshot{Running: true, Enabled: false}},
		{name: "stopped and enabled", previous: relayProcessSnapshot{Running: false, Enabled: true}},
		{name: "stopped and disabled", previous: relayProcessSnapshot{Running: false, Enabled: false}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, controller, _, _, port := relayFixture(t, true)
			if err := manager.restoreRelayProcess(context.Background(), test.previous, port); err != nil {
				t.Fatal(err)
			}
			if got := relayStateRunning(controller.state); got != test.previous.Running || controller.enabled != test.previous.Enabled {
				t.Fatalf("restored process state=(running=%v, enabled=%v), want %+v; actions=%v", got, controller.enabled, test.previous, controller.actions)
			}
		})
	}
}

func TestRelayApplyReplacesHermesMCPWithSharedAnchor(t *testing.T) {
	manager, _, user, target, port := relayFixture(t, false)
	paths, err := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, paths.HermesConfig, []byte("theme: dark\nmcp_servers:\n  old-server:\n    url: https://old.invalid/mcp\n  another-server:\n    command: old\n"))

	manifest := relayManifest(target, port, "https://example.invalid/one")
	result := applyRelay(t, manager, user, manifest, "apply", false)
	if result.BackupID == "" {
		t.Fatalf("Apply did not create a relay backup: %+v", result)
	}
	root, err := readYAMLMap(paths.HermesConfig)
	if err != nil {
		t.Fatal(err)
	}
	if root["theme"] != "dark" {
		t.Fatalf("Apply did not preserve unrelated Hermes config: %+v", root)
	}
	servers, err := yamlStringMap(root["mcp_servers"])
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("Hermes MCP entries were not mirrored: %+v", servers)
	}
	anchor, ok := servers[RelayAnchor].(map[string]any)
	wantURL := "http://127.0.0.1:" + strconv.Itoa(port) + "/mcp"
	if !ok || anchor["url"] != wantURL || anchor["enabled"] != true {
		t.Fatalf("unexpected Hermes relay anchor: %+v", servers[RelayAnchor])
	}
	members, err := ScanSharedRelay(paths)
	if err != nil {
		t.Fatal(err)
	}
	foundHermes := false
	for _, member := range members {
		if member.ID == "anchor:hermes" {
			foundHermes = true
			break
		}
	}
	if !foundHermes {
		t.Fatalf("shared relay inventory omitted Hermes anchor: %+v", members)
	}
	backupBody, err := os.ReadFile(filepath.Join(manager.BackupRoot, target.ID, result.BackupID, "hermes"))
	if err != nil || !strings.Contains(string(backupBody), "old-server") {
		t.Fatalf("relay backup omitted prior Hermes config: err=%v body=%s", err, backupBody)
	}
}

func TestRelayReconcileRepairsHermesAnchorAndPreservesUnmanaged(t *testing.T) {
	manager, _, user, target, port := relayFixture(t, false)
	paths, err := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, paths.HermesConfig, []byte("mcp_servers:\n  local-only:\n    command: keep\n  toolhub-relay:\n    url: http://127.0.0.1:1/mcp\n    enabled: false\n"))
	result := applyRelay(t, manager, user, relayManifest(target, port, "https://example.invalid/one"), "reconcile", false)
	if !result.Repaired || result.BackupID == "" {
		t.Fatalf("Hermes reconcile result=%+v", result)
	}
	root, err := readYAMLMap(paths.HermesConfig)
	if err != nil {
		t.Fatal(err)
	}
	servers, err := yamlStringMap(root["mcp_servers"])
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := servers["local-only"]; !ok {
		t.Fatalf("reconcile removed unmanaged Hermes MCP entry: %+v", servers)
	}
	anchor, _ := servers[RelayAnchor].(map[string]any)
	wantURL := "http://127.0.0.1:" + strconv.Itoa(port) + "/mcp"
	if anchor["url"] != wantURL || anchor["enabled"] != true {
		t.Fatalf("reconcile did not repair Hermes anchor: %+v", anchor)
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

func TestValidateMCPMRejectsPlainUpstreamVersionWithoutToolHubContract(t *testing.T) {
	mcpm := filepath.Join(t.TempDir(), "mcpm")
	if err := os.WriteFile(mcpm, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf 'mcpm 2.15.0\\n'; exit 0; fi\nprintf 'No such command toolhub\\n' >&2\nexit 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	manager := NewRelayManager(&fakeRelayController{}, t.TempDir())
	manager.MCPMPath = mcpm
	_, err := manager.ValidateMCPM(context.Background())
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrMCPMIncompatible {
		t.Fatalf("plain mcpm contract error=%v", err)
	}
}

func TestValidateMCPMRequiresCompleteToolHubCapabilityContract(t *testing.T) {
	contract := `{"adminProtocolVersion":1,"features":["profile-session-binding","tool-filtering","call-policy","one-shot-confirmation","payload-free-observations","routing-hot-reload"],"routingSchemaVersions":[1],"runtime":"mcpm","runtimeVersion":"2.15.0-toolhub.1"}`
	mcpm := filepath.Join(t.TempDir(), "mcpm")
	if err := os.WriteFile(mcpm, []byte("#!/bin/sh\nprintf '%s\\n' '"+contract+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	manager := NewRelayManager(&fakeRelayController{}, t.TempDir())
	manager.MCPMPath = mcpm
	version, err := manager.ValidateMCPM(context.Background())
	if err != nil || version != "2.15.0-toolhub.1" {
		t.Fatalf("ToolHub capability version=%q err=%v", version, err)
	}

	missingFeature := strings.Replace(contract, `,"routing-hot-reload"`, "", 1)
	if err := os.WriteFile(mcpm, []byte("#!/bin/sh\nprintf '%s\\n' '"+missingFeature+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = manager.ValidateMCPM(context.Background())
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrMCPMIncompatible {
		t.Fatalf("incomplete ToolHub capability error=%v", err)
	}
}

func TestValidateMCPMReturnsContractRuntimeVersion(t *testing.T) {
	mcpm := filepath.Join(t.TempDir(), "mcpm")
	writeCompatibleMCPM(t, mcpm, "12.3.4-toolhub.1")
	manager := NewRelayManager(&fakeRelayController{}, t.TempDir())
	manager.MCPMPath = mcpm
	version, err := manager.ValidateMCPM(context.Background())
	if err != nil || version != "12.3.4-toolhub.1" {
		t.Fatalf("ValidateMCPM version=%q err=%v", version, err)
	}
}

func TestRelayRestartUsesFixedPortStopWaitStartSequence(t *testing.T) {
	manager, controller, _, _, port := relayFixture(t, false)
	controller.state = "activating"
	controller.enabled = false
	portChecks := 0
	manager.portAvailable = func(int) error {
		portChecks++
		return nil
	}
	controller.actions = nil
	if err := manager.RestartFixed(context.Background(), port); err != nil {
		t.Fatal(err)
	}
	want := []string{"enable", "stop-unit", "start-unit"}
	if strings.Join(controller.actions, ",") != strings.Join(want, ",") || portChecks != 1 || !controller.enabled || controller.state != "active" {
		t.Fatalf("restart actions=%v portChecks=%d state=%q enabled=%v", controller.actions, portChecks, controller.state, controller.enabled)
	}
}

func TestRelayRestartStopsBeforeReportingFixedPortConflict(t *testing.T) {
	manager, controller, _, _, port := relayFixture(t, true)
	manager.portAvailable = func(int) error { return errors.New("occupied") }
	controller.actions = nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	err := manager.RestartFixed(ctx, port)
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrRelayPortConflict {
		t.Fatalf("RestartFixed error=%v", err)
	}
	if len(controller.actions) != 2 || controller.actions[0] != "enable" || controller.actions[1] != "stop-unit" || controller.state != "inactive" {
		t.Fatalf("port-conflict restart actions=%v state=%q", controller.actions, controller.state)
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
	writeCompatibleMCPM(t, mcpm, "1.0.0-toolhub.1")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	user := ManagedUser{Name: "test", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	controller := &fakeRelayController{state: "inactive"}
	port := 6276
	if healthy {
		controller.state = "active"
		controller.enabled = true
	}
	manager := NewRelayManager(controller, t.TempDir())
	manager.MCPMPath = mcpm
	manager.EnvironmentFile = filepath.Join(t.TempDir(), "mcpm-relay.env")
	manager.portAvailable = func(int) error { return nil }
	manager.Admin = &fakeMCPMAdmin{autoManifest: true}
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return healthyRelayResponse(request)
	})}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeSharedRelay, ManagedUsername: user.Name}
	return manager, controller, user, target, port
}

func writeCompatibleMCPM(t *testing.T, path, version string) {
	t.Helper()
	contract := `{"adminProtocolVersion":1,"features":["profile-session-binding","tool-filtering","call-policy","one-shot-confirmation","payload-free-observations","routing-hot-reload"],"routingSchemaVersions":[1],"runtime":"mcpm","runtimeVersion":"` + version + `"}`
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+contract+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
}

func relayManifest(target bridgeprotocol.Target, port int, endpoint string) bridgeprotocol.DesiredManifest {
	memberID := uuid.NewString()
	return bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: target, MCPServers: []bridgeprotocol.MCPMember{{MemberID: memberID, ServerID: uuid.NewString(), Revision: 1, Name: "server", Transport: "http", URL: endpoint, ContentHash: strings.Repeat("a", 64)}}, Skills: []bridgeprotocol.SkillMember{}, ManagedMemberIDs: []string{memberID}, RelayPort: port}
}

func relayManifestV2(t *testing.T, target bridgeprotocol.Target, port int, endpoint string) bridgeprotocol.DesiredManifest {
	t.Helper()
	return withRelayGovernance(t, relayManifest(target, port, endpoint), "00000000-0000-0000-0000-000000000001", strings.Repeat("a", 64), "00000000-0000-0000-0000-000000000002", strings.Repeat("b", 64))
}

func withRelayGovernance(t *testing.T, manifest bridgeprotocol.DesiredManifest, relayRevisionID, relayHash, policyRevisionID, policyHash string) bridgeprotocol.DesiredManifest {
	t.Helper()
	manifest.SchemaVersion = bridgeprotocol.ManifestSchemaVersionV2
	servers := make([]bridgeprotocol.ServerContractDTO, 0, len(manifest.MCPServers))
	for index, server := range manifest.MCPServers {
		servers = append(servers, bridgeprotocol.ServerContractDTO{
			ServerID: server.ServerID, ServerName: server.Name,
			MCPConfigRevisionID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(server.ServerID+strconv.Itoa(index))).String(),
			Tools:               []bridgeprotocol.RoutingToolDTO{},
		})
	}
	bundle := bridgeprotocol.RoutingBundle{
		SchemaVersion: 1, Mode: "compatibility",
		RelayConfigurationRevisionID: relayRevisionID, RelayConfigurationHash: relayHash,
		GlobalPolicyRevisionID: policyRevisionID, GlobalPolicyHash: policyHash,
		Servers: servers, Profiles: []bridgeprotocol.PublishedProfileDTO{},
	}
	body, hash, err := bundle.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	manifest.RelayGovernance = &bridgeprotocol.RelayGovernanceManifest{RelayConfigurationRevisionID: relayRevisionID, RelayConfigurationHash: relayHash, RoutingBundle: body, RoutingHash: hash}
	return manifest
}

func applyRelay(t *testing.T, manager *RelayManager, user ManagedUser, manifest bridgeprotocol.DesiredManifest, kind string, paused bool) bridgeprotocol.TargetResult {
	t.Helper()
	if admin, ok := manager.Admin.(*fakeMCPMAdmin); ok && admin.autoManifest {
		admin.configureManifest(t, manifest)
	}
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

func healthyRelayResponse(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	}
	var envelope struct {
		Method string `json:"method"`
	}
	if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Method == "notifications/initialized" {
		return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody, Header: make(http.Header)}, nil
	}
	result := `{"tools":[]}`
	switch envelope.Method {
	case "initialize":
		result = `{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"mcpm","version":"1"}}`
	case "tools/list":
		result = `{"tools":[{"name":"server_status"}]}`
	}
	body := `{"jsonrpc":"2.0","id":"` + envelope.Method + `","result":` + result + `}`
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
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
