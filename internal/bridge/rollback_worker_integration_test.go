package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Junhao2314/toolhub/internal/bridgeclient"
	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/market"
	toolruntime "github.com/Junhao2314/toolhub/internal/runtime"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/store"
	"github.com/Junhao2314/toolhub/internal/worker"
)

func TestCompatibilityRollbackRunsWorkerBridgeAndAtomicRelayRestoreIntegration(t *testing.T) {
	ctx := context.Background()
	st := newBridgeWorkerIntegrationStore(t)
	if err := st.BootstrapEnvironment(ctx, "rollback-worker-host", "runner", "UTC", availableRelayPort(t)); err != nil {
		t.Fatal(err)
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var relayTargetID string
	if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/shared-relay'`).Scan(&relayTargetID); err != nil {
		t.Fatal(err)
	}
	domainTarget, err := st.Target(ctx, relayTargetID)
	if err != nil {
		t.Fatal(err)
	}
	target := bridgeprotocol.Target{
		ID: domainTarget.ID, NodeID: domainTarget.NodeID, NodeKind: domainTarget.NodeKind,
		Runtime: domainTarget.Runtime, ManagedUsername: domainTarget.ManagedUsername,
	}

	server, err := st.SaveMCPServer(ctx, "", store.MCPInput{Name: "rollback_catalog", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ObserveContracts(ctx, store.ContractObservationInput{
		ServerID: server.ID,
		Tools:    []store.ObservedToolInput{{Name: "read_all", InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnlyHint: true}},
	}); err != nil {
		t.Fatal(err)
	}
	contractHistoryBefore := contractRevisionCount(t, st)

	user := toolruntime.ManagedUser{Name: "runner", Home: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}
	backupRoot := t.TempDir()
	routingPath := filepath.Join(user.Home, ".config", "mcpm", "toolhub-routing.json")
	process := &rollbackRelayProcess{routingPath: routingPath, grants: map[string]bool{}}
	controller := &rollbackRelayController{process: process}
	relayManager := toolruntime.NewRelayManager(controller, backupRoot)
	relayManager.Admin = &rollbackRelayAdmin{process: process}
	relayManager.EnvironmentFile = filepath.Join(t.TempDir(), "mcpm-relay.env")
	relayManager.MCPMPath = writeRollbackMCPM(t)
	relayManager.HTTPClient = &http.Client{Transport: rollbackRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})}

	compatibility := rollbackManifest(t, target, settings.RelayPort, server, "compatibility")
	enforced := rollbackModeManifest(t, compatibility, "enforced")
	localManager := toolruntime.NewManager(t.TempDir())
	initial, err := localManager.Scan(user, bridgeprotocol.RuntimeSharedRelay)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relayManager.Apply(ctx, user, bridgeprotocol.CommitRequest{
		OperationID: uuid.NewString(), OperationKind: "apply", Target: target,
		ExpectedRevision: initial.Revision, Manifest: compatibility,
	}); err != nil {
		t.Fatal(err)
	}
	compatibilityScan, err := localManager.Scan(user, bridgeprotocol.RuntimeSharedRelay)
	if err != nil {
		t.Fatal(err)
	}
	enforcedResult, err := relayManager.Apply(ctx, user, bridgeprotocol.CommitRequest{
		OperationID: uuid.NewString(), OperationKind: "apply", Target: target,
		ExpectedRevision: compatibilityScan.Revision, Manifest: enforced,
	})
	if err != nil || enforcedResult.BackupID == "" {
		t.Fatalf("enforced Apply result=%+v err=%v", enforcedResult, err)
	}
	process.addExpiredGrant("expired-before-rollback")

	if _, err := st.PinDesiredSnapshot(ctx, target.ID, "relay_config_apply", "", "", compatibility); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PinDesiredSnapshot(ctx, target.ID, "relay_config_apply", "", "", enforced); err != nil {
		t.Fatal(err)
	}
	selectedBackup, err := st.RecordBackup(ctx, bridgeprotocol.Backup{
		ID: enforcedResult.BackupID, TargetID: target.ID, NodeKind: target.NodeKind,
		Runtime: target.Runtime, Revision: compatibilityScan.Revision, CreatedAt: time.Now().UTC(),
	}, "", &compatibility)
	if err != nil {
		t.Fatal(err)
	}

	journal, err := OpenJournal(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	compositeAdapter := NewCompositeAdapter(journal, localManager, relayManager, nil)
	compositeAdapter.lookupManagedUser = func(name string) (toolruntime.ManagedUser, error) {
		if name != user.Name {
			return toolruntime.ManagedUser{}, errors.New("unexpected managed username")
		}
		return user, nil
	}
	adapter := &rollbackRecordingAdapter{CompositeAdapter: compositeAdapter}
	key := []byte(strings.Repeat("b", 32))
	bridgeServer, err := NewServer(key, journal, adapter, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(t.TempDir(), "bridge.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: bridgeServer.Router()}
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()
	t.Cleanup(func() {
		_ = httpServer.Close()
		if serveErr := <-serveDone; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			t.Errorf("serve Bridge: %v", serveErr)
		}
	})
	client, err := bridgeclient.New(socketPath, key)
	if err != nil {
		t.Fatal(err)
	}

	preflight, err := client.Preflight(ctx, "rollback-preflight", bridgeprotocol.PreflightRequest{Target: target, Manifest: compatibility})
	if err != nil {
		t.Fatal(err)
	}
	current, err := localManager.Scan(user, bridgeprotocol.RuntimeSharedRelay)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.TargetRevision != current.Revision || !bridgeprotocol.IsSHA256(preflight.ManifestHash) {
		t.Fatalf("rollback preflight=%+v current=%s", preflight, current.Revision)
	}

	request := map[string]any{
		"backupId": selectedBackup.ID, "targetRevision": preflight.TargetRevision,
		"sourceKind": "restore", "sourceId": selectedBackup.ID,
	}
	operation, err := st.CreateOperation(ctx, store.CreateOperationInput{
		Kind: "restore", SourceID: target.ID, IdempotencyKey: "worker-bridge-runtime-rollback",
		Request: request, TargetIDs: []string{target.ID}, TargetRequests: map[string]any{target.ID: request},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerContext, cancelWorker := context.WithCancel(context.Background())
	t.Cleanup(cancelWorker)
	worker.New(st, client, market.NewMulti(), slog.New(slog.NewTextHandler(io.Discard, nil))).Run(workerContext, 1)
	operation = waitForBridgeWorkerOperation(t, st, operation.ID)
	if operation.Status != bridgeprotocol.OperationSucceeded {
		detail, detailErr := st.OperationDetail(ctx, operation.ID)
		request, restoreErr := adapter.restoreEvidence()
		t.Fatalf("rollback operation=%+v detail=%s detailErr=%v request=%+v restoreErr=%T %v", operation, detail, detailErr, request, restoreErr, restoreErr)
	}
	cancelWorker()

	_, activeManifest, err := st.ActiveDesiredManifest(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	var restoredBundle bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(activeManifest.RelayGovernance.RoutingBundle, &restoredBundle); err != nil {
		t.Fatal(err)
	}
	if restoredBundle.Mode != "compatibility" {
		t.Fatalf("restored routing mode=%q", restoredBundle.Mode)
	}
	catalog, grants, activeProcesses, maxActiveProcesses := process.snapshot()
	if strings.Join(catalog, ",") != "read_all" {
		t.Fatalf("restored legacy catalog=%v", catalog)
	}
	if len(grants) != 0 {
		t.Fatalf("expired grants revived after rollback: %v", grants)
	}
	if activeProcesses != 1 || maxActiveProcesses != 1 {
		t.Fatalf("relay process counts active=%d max=%d", activeProcesses, maxActiveProcesses)
	}
	if after := contractRevisionCount(t, st); after != contractHistoryBefore {
		t.Fatalf("rollback changed Contract history %d -> %d", contractHistoryBefore, after)
	}

	var operationTargetID string
	var resultBody []byte
	if err := st.Pool().QueryRow(ctx, `SELECT id::text,result FROM operation_targets WHERE operation_id=$1`, operation.ID).Scan(&operationTargetID, &resultBody); err != nil {
		t.Fatal(err)
	}
	var result bridgeprotocol.TargetResult
	if err := json.Unmarshal(resultBody, &result); err != nil {
		t.Fatal(err)
	}
	if result.BackupID == "" || result.BackupID == selectedBackup.BridgeBackupID {
		t.Fatalf("restore did not create a new runtime backup: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(backupRoot, target.ID, result.BackupID, "state.json")); err != nil {
		t.Fatalf("restore backup missing: %v", err)
	}
	bridgeOperation, err := client.Operation(ctx, operationTargetID)
	if err != nil || bridgeOperation.Status != bridgeprotocol.OperationSucceeded {
		t.Fatalf("Bridge restore operation=%+v err=%v", bridgeOperation, err)
	}
}

type rollbackRelayProcess struct {
	mu                 sync.Mutex
	routingPath        string
	bundle             bridgeprotocol.RoutingBundle
	routingHash        string
	grants             map[string]bool
	catalog            []string
	activeProcesses    int
	maxActiveProcesses int
}

type rollbackRecordingAdapter struct {
	*CompositeAdapter
	mu             sync.Mutex
	restoreRequest bridgeprotocol.CommitRequest
	restoreErr     error
}

func (adapter *rollbackRecordingAdapter) Restore(ctx context.Context, request bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	result, err := adapter.CompositeAdapter.Restore(ctx, request)
	adapter.mu.Lock()
	adapter.restoreRequest, adapter.restoreErr = request, err
	adapter.mu.Unlock()
	return result, err
}

func (adapter *rollbackRecordingAdapter) restoreEvidence() (bridgeprotocol.CommitRequest, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.restoreRequest, adapter.restoreErr
}

func (process *rollbackRelayProcess) loadRouting() error {
	body, err := os.ReadFile(process.routingPath)
	if err != nil {
		return err
	}
	var bundle bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(body, &bundle); err != nil {
		return err
	}
	_, hash, err := bundle.Canonical()
	if err != nil {
		return err
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	process.bundle, process.routingHash = bundle, hash
	process.catalog = nil
	if bundle.Mode == "compatibility" {
		for _, server := range bundle.Servers {
			for _, tool := range server.Tools {
				process.catalog = append(process.catalog, tool.Name)
			}
		}
		sort.Strings(process.catalog)
	}
	return nil
}

func (process *rollbackRelayProcess) addExpiredGrant(id string) {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.grants[id] = true
}

func (process *rollbackRelayProcess) snapshot() ([]string, []string, int, int) {
	process.mu.Lock()
	defer process.mu.Unlock()
	catalog := append([]string(nil), process.catalog...)
	grants := make([]string, 0, len(process.grants))
	for grant := range process.grants {
		grants = append(grants, grant)
	}
	sort.Strings(grants)
	return catalog, grants, process.activeProcesses, process.maxActiveProcesses
}

type rollbackRelayController struct {
	process *rollbackRelayProcess
	enabled bool
}

func (controller *rollbackRelayController) Action(_ context.Context, action string) (string, error) {
	controller.process.mu.Lock()
	switch action {
	case "status":
		if controller.process.activeProcesses == 1 {
			controller.process.mu.Unlock()
			return "active", nil
		}
		controller.process.mu.Unlock()
		return "inactive", errors.New("inactive")
	case "is-enabled":
		if controller.enabled {
			controller.process.mu.Unlock()
			return "enabled", nil
		}
		controller.process.mu.Unlock()
		return "disabled", errors.New("disabled")
	case "enable":
		controller.enabled = true
		controller.process.mu.Unlock()
		return "enabled", nil
	case "disable":
		controller.enabled = false
		controller.process.mu.Unlock()
		return "disabled", nil
	case "stop", "stop-unit":
		controller.process.activeProcesses = 0
		if action == "stop" {
			controller.enabled = false
		}
		controller.process.mu.Unlock()
		return "inactive", nil
	case "start", "start-unit":
		if action == "start" {
			controller.enabled = true
		}
		controller.process.activeProcesses++
		if controller.process.activeProcesses > controller.process.maxActiveProcesses {
			controller.process.maxActiveProcesses = controller.process.activeProcesses
		}
		controller.process.grants = map[string]bool{}
	default:
		controller.process.mu.Unlock()
		return "", errors.New("unsupported relay action")
	}
	controller.process.mu.Unlock()
	err := controller.process.loadRouting()
	if err != nil {
		controller.process.mu.Lock()
		controller.process.activeProcesses = 0
		controller.process.mu.Unlock()
		return "inactive", err
	}
	return "active", nil
}

type rollbackRelayAdmin struct{ process *rollbackRelayProcess }

func (admin *rollbackRelayAdmin) Capability(context.Context) (bridgeprotocol.RelayCapabilityResponse, error) {
	return bridgeprotocol.RelayCapabilityResponse{AdminProtocolVersion: 1, Features: bridgeprotocol.RelayEnforcementFeatures(), RoutingSchemaVersions: []int{1}, Runtime: "mcpm", RuntimeVersion: "2.15.0-toolhub.1"}, nil
}

func (admin *rollbackRelayAdmin) SessionCanary(context.Context, bridgeprotocol.RelaySessionCanaryRequest) (bridgeprotocol.RelaySessionCanaryResponse, error) {
	return bridgeprotocol.RelaySessionCanaryResponse{}, errors.New("session canary is not part of compatibility restore")
}

func (admin *rollbackRelayAdmin) ReloadRouting(context.Context) (bridgeprotocol.RelayAdminStatus, error) {
	if err := admin.process.loadRouting(); err != nil {
		return bridgeprotocol.RelayAdminStatus{}, err
	}
	return admin.Status(context.Background())
}

func (admin *rollbackRelayAdmin) Status(context.Context) (bridgeprotocol.RelayAdminStatus, error) {
	admin.process.mu.Lock()
	defer admin.process.mu.Unlock()
	return bridgeprotocol.RelayAdminStatus{
		Mode: admin.process.bundle.Mode, RelayConfigurationRevisionID: admin.process.bundle.RelayConfigurationRevisionID,
		GlobalPolicyRevisionID: admin.process.bundle.GlobalPolicyRevisionID, RoutingBundleHash: admin.process.routingHash,
		PublishedProfileRevisions: []bridgeprotocol.RelayAdminProfileRevision{},
	}, nil
}

func (admin *rollbackRelayAdmin) ObserveContracts(context.Context) (bridgeprotocol.ContractObservationResponse, error) {
	admin.process.mu.Lock()
	defer admin.process.mu.Unlock()
	response := bridgeprotocol.ContractObservationResponse{RelayConfigurationRevisionID: admin.process.bundle.RelayConfigurationRevisionID, Servers: []bridgeprotocol.ContractServerObservation{}}
	for _, server := range admin.process.bundle.Servers {
		observed := bridgeprotocol.ContractServerObservation{ServerID: server.ServerID, ServerName: server.ServerName, MCPConfigRevisionID: server.MCPConfigRevisionID, Tools: []bridgeprotocol.ContractToolDTO{}}
		for _, tool := range server.Tools {
			observed.Tools = append(observed.Tools, bridgeprotocol.ContractToolDTO{Name: tool.Name, RuntimeName: tool.Name, InputSchema: tool.InputSchema, OutputSchema: tool.OutputSchema, Annotations: tool.Annotations})
		}
		response.Servers = append(response.Servers, observed)
	}
	return response, nil
}

func (*rollbackRelayAdmin) ListConfirmations(context.Context) (bridgeprotocol.ConfirmationListResponse, error) {
	return bridgeprotocol.ConfirmationListResponse{Items: []bridgeprotocol.ConfirmationSummary{}}, nil
}

func (*rollbackRelayAdmin) DecideConfirmation(context.Context, bool, bridgeprotocol.ConfirmationDecisionRequest) (bridgeprotocol.ConfirmationDecisionResponse, error) {
	return bridgeprotocol.ConfirmationDecisionResponse{}, errors.New("confirmation decision is not used")
}

func (*rollbackRelayAdmin) DrainObservations(context.Context, bridgeprotocol.ObservationDrainRequest) (bridgeprotocol.ObservationDrainResponse, error) {
	return bridgeprotocol.ObservationDrainResponse{}, errors.New("observation drain is not used")
}

func rollbackManifest(t *testing.T, target bridgeprotocol.Target, port int, server domain.MCPServer, mode string) bridgeprotocol.DesiredManifest {
	t.Helper()
	contractID, contractHash := uuid.NewString(), strings.Repeat("c", 64)
	toolID := uuid.NewString()
	bundle := bridgeprotocol.RoutingBundle{
		SchemaVersion: 1, Mode: mode, RelayConfigurationRevisionID: uuid.NewString(), RelayConfigurationHash: strings.Repeat("a", 64),
		GlobalPolicyRevisionID: uuid.NewString(), GlobalPolicyHash: strings.Repeat("b", 64),
		Servers: []bridgeprotocol.ServerContractDTO{{
			ServerID: server.ID, ServerName: server.Name, MCPConfigRevisionID: server.CurrentRevisionID,
			AcceptedContractRevisionID: &contractID, AcceptedContractHash: &contractHash,
			Tools: []bridgeprotocol.RoutingToolDTO{{ToolID: toolID, Name: "read_all", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{}, Annotations: map[string]any{}, GlobalDecision: "allow", ReasonCodes: []string{}}},
		}},
		Profiles: []bridgeprotocol.PublishedProfileDTO{},
	}
	routingBody, routingHash, err := bundle.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	memberID := uuid.NewString()
	manifest := bridgeprotocol.DesiredManifest{
		SchemaVersion: bridgeprotocol.ManifestSchemaVersionV2, Target: target,
		Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{{
			MemberID: memberID, ServerID: server.ID, Revision: server.Revision, Name: server.Name,
			Transport: server.Transport, URL: server.URL, ContentHash: server.ContentHash,
		}}, ManagedMemberIDs: []string{memberID}, RelayPort: port,
		RelayGovernance: &bridgeprotocol.RelayGovernanceManifest{
			RelayConfigurationRevisionID: bundle.RelayConfigurationRevisionID, RelayConfigurationHash: bundle.RelayConfigurationHash,
			RoutingBundle: routingBody, RoutingHash: routingHash,
		},
	}
	if err := manifest.Validate(true); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func rollbackModeManifest(t *testing.T, manifest bridgeprotocol.DesiredManifest, mode string) bridgeprotocol.DesiredManifest {
	t.Helper()
	var bundle bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(manifest.RelayGovernance.RoutingBundle, &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.Mode = mode
	body, hash, err := bundle.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	governance := *manifest.RelayGovernance
	governance.RoutingBundle, governance.RoutingHash = body, hash
	manifest.RelayGovernance = &governance
	if err := manifest.Validate(true); err != nil {
		t.Fatal(err)
	}
	return manifest
}

type rollbackRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip rollbackRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func writeRollbackMCPM(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcpm")
	contract := `{"adminProtocolVersion":1,"features":["profile-session-binding","tool-filtering","call-policy","one-shot-confirmation","payload-free-observations","routing-hot-reload","session-canary"],"routingSchemaVersions":[1],"runtime":"mcpm","runtimeVersion":"2.15.0-toolhub.1"}`
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+contract+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func availableRelayPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func contractRevisionCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var count int
	if err := st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM mcp_contract_revisions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func waitForBridgeWorkerOperation(t *testing.T, st *store.Store, operationID string) domain.Operation {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := st.Operation(context.Background(), operationID)
		if err != nil {
			t.Fatal(err)
		}
		if bridgeprotocol.IsTerminalOperationStatus(operation.Status) {
			return operation
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("operation %s did not finish", operationID)
	return domain.Operation{}
}

func newBridgeWorkerIntegrationStore(t *testing.T) *store.Store {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	adminConfig, err := pgx.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	databaseName := "toolhub_bridge_worker_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	databaseURL, err := url.Parse(config.ConnConfig.ConnString())
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	databaseURL.Path = "/" + databaseName
	cipher, err := security.NewCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	st, err := store.Open(ctx, databaseURL.String(), cipher)
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop Bridge worker integration database: %v", err)
		}
		_ = admin.Close(context.Background())
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}
