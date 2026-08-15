package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type fakeAdapter struct {
	refreshCalls         int
	commitCalls          int
	restoreCalls         int
	relayCalls           int
	relayCapabilityCalls int
	relayGovernanceCalls int
	nativeClientCalls    int
	nativeClientRequest  bridgeprotocol.NativeClientInspectionRequest
}

func (f *fakeAdapter) Health(context.Context) error { return nil }
func (f *fakeAdapter) RefreshNodes(context.Context) (bridgeprotocol.RefreshNodesResponse, error) {
	f.refreshCalls++
	return bridgeprotocol.RefreshNodesResponse{Nodes: []bridgeprotocol.NodeInfo{{NodeID: "node", Name: "local", Kind: "local", Status: "online"}}}, nil
}
func (f *fakeAdapter) Scan(context.Context, bridgeprotocol.ScanRequest) (bridgeprotocol.ScanResponse, error) {
	return bridgeprotocol.ScanResponse{TargetRevision: string(bytes.Repeat([]byte{'a'}, 64)), Members: []bridgeprotocol.InventoryMember{}}, nil
}
func (f *fakeAdapter) ExportLocalSkill(context.Context, bridgeprotocol.LocalSkillExportRequest) (bridgeprotocol.LocalSkillExportResponse, error) {
	return bridgeprotocol.LocalSkillExportResponse{}, nil
}
func (f *fakeAdapter) ExportLocalSkillBatch(context.Context, bridgeprotocol.LocalSkillBatchExportRequest) (bridgeprotocol.LocalSkillBatchExportResponse, error) {
	return bridgeprotocol.LocalSkillBatchExportResponse{}, nil
}
func (f *fakeAdapter) PreviewLocalMCP(context.Context, bridgeprotocol.LocalMCPPreviewRequest) (bridgeprotocol.LocalMCPPreviewResponse, error) {
	return bridgeprotocol.LocalMCPPreviewResponse{}, nil
}
func (f *fakeAdapter) CaptureLocalMCP(context.Context, bridgeprotocol.LocalMCPCaptureRequest) (bridgeprotocol.LocalMCPCaptureResponse, error) {
	return bridgeprotocol.LocalMCPCaptureResponse{Preview: bridgeprotocol.LocalMCPServerPreview{Name: "local", Transport: "stdio", Command: "tool", EnvKeys: []string{"TOKEN"}, HeaderKeys: []string{}, ContentHash: strings.Repeat("b", 64)}, Env: map[string]string{"TOKEN": "plaintext-capture-value"}}, nil
}
func (f *fakeAdapter) Preflight(context.Context, bridgeprotocol.PreflightRequest) (bridgeprotocol.PreflightResponse, error) {
	return bridgeprotocol.PreflightResponse{}, nil
}
func (f *fakeAdapter) Commit(context.Context, bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	f.commitCalls++
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy}, nil
}
func (f *fakeAdapter) Reconcile(context.Context, bridgeprotocol.ReconcileRequest) (bridgeprotocol.TargetResult, error) {
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy}, nil
}
func (f *fakeAdapter) Restore(context.Context, bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	f.restoreCalls++
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy}, nil
}
func (f *fakeAdapter) RemoveBackup(context.Context, bridgeprotocol.Backup) error { return nil }
func (f *fakeAdapter) Relay(context.Context, string, bridgeprotocol.RelayActionRequest) (bridgeprotocol.RelayStatus, error) {
	f.relayCalls++
	return bridgeprotocol.RelayStatus{State: "running", Healthy: true}, nil
}
func (f *fakeAdapter) RelayCapability(context.Context) (bridgeprotocol.RelayCapabilityResponse, error) {
	f.relayCapabilityCalls++
	return bridgeprotocol.RelayCapabilityResponse{AdminProtocolVersion: 1, Features: []string{"tool-filtering"}, RoutingSchemaVersions: []int{1}, Runtime: "mcpm", RuntimeVersion: "2.15.0-toolhub.1"}, nil
}
func (f *fakeAdapter) ReloadRelayGovernance(_ context.Context, input bridgeprotocol.RelayReloadRequest) (bridgeprotocol.RelayReloadResponse, error) {
	f.relayGovernanceCalls++
	return bridgeprotocol.RelayReloadResponse{Reloaded: true, RoutingBundleHash: input.RoutingBundleHash}, nil
}
func (f *fakeAdapter) ObserveRelayContracts(context.Context) (bridgeprotocol.ContractObservationResponse, error) {
	f.relayGovernanceCalls++
	return bridgeprotocol.ContractObservationResponse{Servers: []bridgeprotocol.ContractServerObservation{}}, nil
}
func (f *fakeAdapter) ListRelayConfirmations(context.Context) (bridgeprotocol.ConfirmationListResponse, error) {
	f.relayGovernanceCalls++
	return bridgeprotocol.ConfirmationListResponse{Items: []bridgeprotocol.ConfirmationSummary{}}, nil
}
func (f *fakeAdapter) DecideRelayConfirmation(_ context.Context, _ bool, input bridgeprotocol.ConfirmationDecisionRequest) (bridgeprotocol.ConfirmationDecisionResponse, error) {
	f.relayGovernanceCalls++
	return bridgeprotocol.ConfirmationDecisionResponse{ChallengeID: input.ChallengeID, BindingHash: input.BindingHash}, nil
}
func (f *fakeAdapter) DrainRelayObservations(context.Context, bridgeprotocol.ObservationDrainRequest) (bridgeprotocol.ObservationDrainResponse, error) {
	f.relayGovernanceCalls++
	return bridgeprotocol.ObservationDrainResponse{BootID: "boot-1", Items: []bridgeprotocol.Observation{}}, nil
}
func (f *fakeAdapter) InspectNativeClient(_ context.Context, input bridgeprotocol.NativeClientInspectionRequest) (bridgeprotocol.NativeClientInspectionResponse, error) {
	f.nativeClientCalls++
	f.nativeClientRequest = input
	return bridgeprotocol.NativeClientInspectionResponse{ClientKind: input.ClientKind, Version: "2.1.232", Supported: true}, nil
}

func testServer(t *testing.T) (*Server, *fakeAdapter, []byte) {
	t.Helper()
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	adapter := &fakeAdapter{}
	key := bytes.Repeat([]byte{'k'}, 32)
	server, err := NewServer(key, journal, adapter, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return server, adapter, key
}

func signedRequest(t *testing.T, key []byte, method, path string, body []byte, now time.Time, nonce, idempotency string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	bridgeprotocol.SignRequest(request, key, body, now, nonce)
	if idempotency != "" {
		request.Header.Set(bridgeprotocol.HeaderIdempotencyKey, idempotency)
	}
	return request
}

func serveSignedMutation(t *testing.T, server *Server, key []byte, path, nonce, idempotency string, input any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := signedRequest(t, key, http.MethodPost, path, body, server.now(), nonce, idempotency)
	recorder := httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, request)
	return recorder
}

func validLocalTarget(runtime string) bridgeprotocol.Target {
	return bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: runtime, ManagedUsername: "operator"}
}

func TestServerRejectsReplayAndTampering(t *testing.T) {
	server, _, key := testServer(t)
	body := []byte(`{}`)
	request := signedRequest(t, key, http.MethodPost, "/v1/nodes/refresh", body, server.now(), "1234567890abcdef", "refresh-1")
	recorder := httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("first request status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	replay := signedRequest(t, key, http.MethodPost, "/v1/nodes/refresh", body, server.now(), "1234567890abcdef", "refresh-1")
	recorder = httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, replay)
	if recorder.Code != http.StatusConflict || !bytes.Contains(recorder.Body.Bytes(), []byte(bridgeprotocol.ErrReplay)) {
		t.Fatalf("replay status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	tampered := signedRequest(t, key, http.MethodPost, "/v1/nodes/refresh", body, server.now(), "abcdef1234567890", "refresh-2")
	tampered.Body = io.NopCloser(bytes.NewReader([]byte(`{"changed":true}`)))
	recorder = httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, tampered)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("tampered status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestMutationIdempotencyReturnsCachedResponseAndRejectsChangedBody(t *testing.T) {
	server, adapter, key := testServer(t)
	for index, nonce := range []string{"nonce-1234567890-a", "nonce-1234567890-b"} {
		request := signedRequest(t, key, http.MethodPost, "/v1/nodes/refresh", []byte(`{}`), server.now(), nonce, "same-operation")
		recorder := httptest.NewRecorder()
		server.Router().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", index, recorder.Code, recorder.Body.String())
		}
	}
	if adapter.refreshCalls != 1 {
		t.Fatalf("adapter was called %d times; want one", adapter.refreshCalls)
	}
	changed := []byte(`{"different":true}`)
	request := signedRequest(t, key, http.MethodPost, "/v1/nodes/refresh", changed, server.now(), "nonce-1234567890-c", "same-operation")
	recorder := httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !bytes.Contains(recorder.Body.Bytes(), []byte(bridgeprotocol.ErrIdempotencyConflict)) {
		t.Fatalf("conflict status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCommitRejectsOperationKindThatDoesNotMatchRoute(t *testing.T) {
	server, adapter, key := testServer(t)
	target := validLocalTarget(bridgeprotocol.RuntimeCodex)
	manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: target, Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{}}
	input := bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "edit", Target: target, ExpectedRevision: string(bytes.Repeat([]byte{'a'}, 64)), Manifest: manifest}
	recorder := serveSignedMutation(t, server, key, "/v1/targets/apply", "typed-kind-nonce-0001", "typed-kind-mismatch", input)
	if recorder.Code != http.StatusBadRequest || !bytes.Contains(recorder.Body.Bytes(), []byte(bridgeprotocol.ErrInvalidRequest)) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if adapter.commitCalls != 0 {
		t.Fatalf("adapter commit calls=%d; want zero", adapter.commitCalls)
	}
}

func TestRestoreRequiresSHA256ExpectedRevision(t *testing.T) {
	server, adapter, key := testServer(t)
	input := bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "restore", Target: validLocalTarget(bridgeprotocol.RuntimeClaude), BackupID: uuid.NewString()}
	recorder := serveSignedMutation(t, server, key, "/v1/targets/restore", "restore-nonce-000001", "restore-no-revision", input)
	if recorder.Code != http.StatusBadRequest || !bytes.Contains(recorder.Body.Bytes(), []byte("expectedRevision")) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if adapter.restoreCalls != 0 {
		t.Fatalf("adapter restore calls=%d; want zero", adapter.restoreCalls)
	}
}

func TestRelayRejectsInvalidTargetAndPort(t *testing.T) {
	tests := []struct {
		name  string
		input bridgeprotocol.RelayActionRequest
	}{
		{name: "runtime", input: bridgeprotocol.RelayActionRequest{Target: validLocalTarget(bridgeprotocol.RuntimeCodex), Port: 6276}},
		{name: "port", input: bridgeprotocol.RelayActionRequest{Target: validLocalTarget(bridgeprotocol.RuntimeSharedRelay), Port: 0}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, adapter, key := testServer(t)
			recorder := serveSignedMutation(t, server, key, "/v1/relay/restart", "relay-nonce-00000"+string(rune('1'+index)), "relay-invalid-"+test.name, test.input)
			if recorder.Code != http.StatusBadRequest || !bytes.Contains(recorder.Body.Bytes(), []byte(bridgeprotocol.ErrInvalidRequest)) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if adapter.relayCalls != 0 {
				t.Fatalf("adapter relay calls=%d; want zero", adapter.relayCalls)
			}
		})
	}
}

func TestNativeClientInspectionRequiresTypedRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: `{}`},
		{name: "unknown field", body: `{"managedUsername":"operator","clientKind":"claude","path":"/tmp/claude"}`},
		{name: "invalid username", body: `{"managedUsername":"../root","clientKind":"claude"}`},
		{name: "invalid client", body: `{"managedUsername":"operator","clientKind":"hermes"}`},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, adapter, key := testServer(t)
			request := signedRequest(t, key, http.MethodPost, "/v1/native-clients/inspect", []byte(test.body), server.now(), fmt.Sprintf("native-invalid-%04d", index), "")
			recorder := httptest.NewRecorder()
			server.Router().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest || adapter.nativeClientCalls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", recorder.Code, adapter.nativeClientCalls, recorder.Body.String())
			}
		})
	}

	server, adapter, key := testServer(t)
	body := []byte(`{"managedUsername":"operator","clientKind":"claude"}`)
	request := signedRequest(t, key, http.MethodPost, "/v1/native-clients/inspect", body, server.now(), "native-valid-0001", "")
	recorder := httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || adapter.nativeClientCalls != 1 || adapter.nativeClientRequest.ManagedUsername != "operator" || adapter.nativeClientRequest.ClientKind != "claude" {
		t.Fatalf("status=%d calls=%d request=%+v body=%s", recorder.Code, adapter.nativeClientCalls, adapter.nativeClientRequest, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "path") || strings.Contains(recorder.Body.String(), "argv") {
		t.Fatalf("inspection leaked executable details: %s", recorder.Body.String())
	}
}

func TestRelayGovernanceCapabilityIsTypedAndEphemeral(t *testing.T) {
	server, adapter, key := testServer(t)
	invalid := signedRequest(t, key, http.MethodPost, "/v1/relay/governance/capability", []byte(`{"path":"/tmp/relay.sock"}`), server.now(), "governance-capability-bad", "")
	invalidRecorder := httptest.NewRecorder()
	server.Router().ServeHTTP(invalidRecorder, invalid)
	if invalidRecorder.Code != http.StatusBadRequest || adapter.relayCapabilityCalls != 0 {
		t.Fatalf("invalid capability status=%d calls=%d body=%s", invalidRecorder.Code, adapter.relayCapabilityCalls, invalidRecorder.Body.String())
	}

	request := signedRequest(t, key, http.MethodPost, "/v1/relay/governance/capability", []byte(`{}`), server.now(), "governance-capability-ok", "")
	recorder := httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || adapter.relayCapabilityCalls != 1 || !bytes.Contains(recorder.Body.Bytes(), []byte(`"adminProtocolVersion":1`)) {
		t.Fatalf("capability status=%d calls=%d body=%s", recorder.Code, adapter.relayCapabilityCalls, recorder.Body.String())
	}
	assertIdempotencyJournalEmpty(t, server.journal)
}

func TestRelayGovernanceReadBatchesAreEphemeral(t *testing.T) {
	server, adapter, key := testServer(t)
	observed := serveSignedMutation(t, server, key, "/v1/relay/governance/contracts/observe", "governance-observe-read", "observe-read-key", map[string]any{})
	if observed.Code != http.StatusOK || adapter.relayGovernanceCalls != 1 {
		t.Fatalf("observe status=%d calls=%d body=%s", observed.Code, adapter.relayGovernanceCalls, observed.Body.String())
	}
	drained := serveSignedMutation(t, server, key, "/v1/relay/governance/observations/drain", "governance-drain-read", "drain-read-key", bridgeprotocol.ObservationDrainRequest{Limit: 1000})
	if drained.Code != http.StatusOK || adapter.relayGovernanceCalls != 2 {
		t.Fatalf("drain status=%d calls=%d body=%s", drained.Code, adapter.relayGovernanceCalls, drained.Body.String())
	}
	assertIdempotencyJournalEmpty(t, server.journal)
}

func TestRelayGovernanceReloadRequiresCanonicalBoundBundle(t *testing.T) {
	server, adapter, key := testServer(t)
	bundle := bridgeprotocol.RoutingBundle{
		SchemaVersion: 1, Mode: "compatibility",
		RelayConfigurationRevisionID: "00000000-0000-0000-0000-000000000001",
		RelayConfigurationHash:       strings.Repeat("a", 64),
		GlobalPolicyRevisionID:       "00000000-0000-0000-0000-000000000002",
		GlobalPolicyHash:             strings.Repeat("b", 64),
		Servers:                      []bridgeprotocol.ServerContractDTO{}, Profiles: []bridgeprotocol.PublishedProfileDTO{},
	}
	body, hash, err := bundle.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	input := bridgeprotocol.RelayReloadRequest{RelayConfigurationRevisionID: bundle.RelayConfigurationRevisionID, RoutingBundleHash: hash, RoutingBundle: body}
	recorder := serveSignedMutation(t, server, key, "/v1/relay/governance/reload", "governance-reload-ok", "governance-reload-key", input)
	if recorder.Code != http.StatusOK || adapter.relayGovernanceCalls != 1 || !bytes.Contains(recorder.Body.Bytes(), []byte(hash)) {
		t.Fatalf("reload status=%d calls=%d body=%s", recorder.Code, adapter.relayGovernanceCalls, recorder.Body.String())
	}
	assertJournalOmitsMarker(t, server.journal, "governance-reload-key", strings.Repeat("b", 64))

	input.RoutingBundleHash = strings.Repeat("c", 64)
	bad := serveSignedMutation(t, server, key, "/v1/relay/governance/reload", "governance-reload-bad", "governance-reload-bad-key", input)
	if bad.Code != http.StatusBadRequest || adapter.relayGovernanceCalls != 1 {
		t.Fatalf("invalid reload status=%d calls=%d body=%s", bad.Code, adapter.relayGovernanceCalls, bad.Body.String())
	}
}

func TestRelayGovernanceDrainRejectsCompanionLimitOverflow(t *testing.T) {
	server, adapter, key := testServer(t)
	input := bridgeprotocol.ObservationDrainRequest{AfterSequence: 0, Limit: 1001}
	recorder := serveSignedMutation(t, server, key, "/v1/relay/governance/observations/drain", "governance-drain-bad", "governance-drain-key", input)
	if recorder.Code != http.StatusBadRequest || adapter.relayGovernanceCalls != 0 {
		t.Fatalf("drain status=%d calls=%d body=%s", recorder.Code, adapter.relayGovernanceCalls, recorder.Body.String())
	}
	invalidBootID := "not-a-uuid"
	input = bridgeprotocol.ObservationDrainRequest{AfterBootID: &invalidBootID, Limit: 1000}
	recorder = serveSignedMutation(t, server, key, "/v1/relay/governance/observations/drain", "governance-drain-boot", "governance-drain-boot-key", input)
	if recorder.Code != http.StatusBadRequest || adapter.relayGovernanceCalls != 0 {
		t.Fatalf("drain boot status=%d calls=%d body=%s", recorder.Code, adapter.relayGovernanceCalls, recorder.Body.String())
	}
}

func assertIdempotencyJournalEmpty(t *testing.T, journal *Journal) {
	t.Helper()
	if err := journal.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketIdempotency).Stats().KeyN != 0 {
			return errors.New("ephemeral governance read entered the idempotency journal")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertJournalOmitsMarker(t *testing.T, journal *Journal, key, marker string) {
	t.Helper()
	if err := journal.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(bucketIdempotency).Get([]byte(key))
		if value == nil {
			return errors.New("governance mutation omitted its idempotency record")
		}
		if bytes.Contains(value, []byte(marker)) {
			return errors.New("governance mutation persisted request content")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJournalRejectsSensitiveValuesAndRecoversRunningOperation(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if err := journal.IdempotencyPut("secret", "hash", 200, []byte(`{"secretValues":{"id":"plaintext"}}`)); err == nil {
		t.Fatal("expected sensitive response rejection")
	}
	operation := bridgeprotocol.Operation{ID: "op-1", Kind: "apply", Status: bridgeprotocol.OperationRunning, Targets: []bridgeprotocol.OperationTarget{{TargetID: "target", Status: bridgeprotocol.OperationRunning}}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := journal.PutOperation(operation); err != nil {
		t.Fatal(err)
	}
	if err := journal.RecoverOperations(time.Now()); err != nil {
		t.Fatal(err)
	}
	recovered, err := journal.Operation("op-1")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != bridgeprotocol.OperationFailed || recovered.Targets[0].Error == nil || recovered.Targets[0].Error.Code != bridgeprotocol.ErrSaltJobMissing {
		t.Fatalf("unexpected recovered operation: %+v", recovered)
	}
	if recovered.Targets[0].Result == nil || recovered.Targets[0].Result.Error == nil {
		t.Fatalf("recovered operation omitted replayable target result: %+v", recovered)
	}
}

func TestPendingIdempotencyReplaysRecoveredTargetResult(t *testing.T) {
	server, adapter, key := testServer(t)
	target := validLocalTarget(bridgeprotocol.RuntimeCodex)
	manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: target, Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{}}
	input := bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "apply", Target: target, ExpectedRevision: strings.Repeat("a", 64), Manifest: manifest}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	idempotencyKey := "recovered-apply-request"
	hash := requestHash(http.MethodPost, "/v1/targets/apply", body)
	if _, existing, err := server.journal.IdempotencyBegin(idempotencyKey, hash, input.OperationID); err != nil || existing {
		t.Fatalf("begin pending idempotency existing=%v err=%v", existing, err)
	}
	result := bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: strings.Repeat("b", 64)}
	now := server.now().UTC()
	operation := bridgeprotocol.Operation{ID: input.OperationID, Kind: "toolhub.apply", Status: bridgeprotocol.OperationSucceeded, Targets: []bridgeprotocol.OperationTarget{{TargetID: target.ID, Status: bridgeprotocol.OperationSucceeded, SaltJID: "20260730120000000000", Result: &result}}, CreatedAt: now, UpdatedAt: now}
	if err := server.journal.PutOperation(operation); err != nil {
		t.Fatal(err)
	}
	request := signedRequest(t, key, http.MethodPost, "/v1/targets/apply", body, server.now(), "recovered-replay-nonce", idempotencyKey)
	recorder := httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(strings.Repeat("b", 64))) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if adapter.commitCalls != 0 {
		t.Fatalf("recovered replay called adapter %d times", adapter.commitCalls)
	}
}

func TestRecoveredBlockedRelayResultRemainsSuccessfulHTTPResponse(t *testing.T) {
	server, _, _ := testServer(t)
	apiErr := &bridgeprotocol.APIError{Code: bridgeprotocol.ErrMCPMIncompatible, Message: "namespace unavailable", Retryable: true}
	result := bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthBlocked, Relay: &bridgeprotocol.RelayStatus{State: "active", Endpoint: "http://127.0.0.1:6276/mcp", FixedPort: 6276, SystemdEnabled: true, Contract: "incompatible", ErrorCode: apiErr.Code, ErrorReason: apiErr.Message}, Error: apiErr}
	server.persistSafeOperation("relay-blocked", "apply", "target", result)
	status, body, ok := server.recoveredIdempotencyResponse("relay-blocked")
	if !ok || status != http.StatusOK || !bytes.Contains(body, []byte(`"health":"blocked"`)) || !bytes.Contains(body, []byte(`"relay"`)) {
		t.Fatalf("recovered relay status=%d ok=%v body=%s", status, ok, body)
	}
	if bytes.Contains(body, []byte(`{"error":{"code"`)) {
		t.Fatalf("blocked target was converted into a failed HTTP response: %s", body)
	}
}

func TestBackupGCAppliesAgeAndCount(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	now := time.Now().UTC()
	for index, age := range []time.Duration{time.Hour, 2 * time.Hour, 40 * 24 * time.Hour} {
		backup := bridgeprotocol.Backup{ID: string(rune('a' + index)), TargetID: "target", Runtime: "codex", Revision: "r", CreatedAt: now.Add(-age)}
		if err := journal.PutBackup(backup); err != nil {
			t.Fatal(err)
		}
	}
	var removed []string
	removedIDs, err := journal.GCBackups(now, 30, 1, func(backup bridgeprotocol.Backup) error { removed = append(removed, backup.ID); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(removedIDs) != 2 || len(removed) != 2 {
		t.Fatalf("removed=%v returned=%v; want two", removed, removedIDs)
	}
	items, err := journal.Backups("target")
	if err != nil || len(items) != 1 || items[0].ID != "a" {
		t.Fatalf("retained=%+v err=%v", items, err)
	}
}

func TestJournalFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	journal.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("journal mode=%o; want 0600", info.Mode().Perm())
	}
}

func TestSafeOperationJSONPersistsOnlyValidatedTypedResult(t *testing.T) {
	server, _, _ := testServer(t)
	server.persistSafeOperation("op", "apply", "target", bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, BackupID: "backup", Manifest: &bridgeprotocol.DesiredManifest{}, Details: map[string]any{"note": "editable"}})
	operation, err := server.journal.Operation("op")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(operation)
	if !bytes.Contains(body, []byte("backup")) {
		t.Fatalf("safe operation omitted replayable typed result: %s", body)
	}
	if bytes.Contains(body, []byte("manifest")) || bytes.Contains(body, []byte("editable")) {
		t.Fatalf("safe operation persisted editable result fields: %s", body)
	}
	sensitive := operation
	sensitive.ID = "unsafe"
	sensitive.Targets[0].Result.Details = map[string]any{"rawOutput": "salt plaintext"}
	if err := server.journal.PutOperation(sensitive); err == nil {
		t.Fatal("expected raw Bridge output in target result to be rejected")
	}
}

func TestEphemeralMCPCaptureNeverEntersIdempotencyJournal(t *testing.T) {
	server, _, key := testServer(t)
	input := bridgeprotocol.LocalMCPCaptureRequest{Target: validLocalTarget(bridgeprotocol.RuntimeClaude), ExpectedRevision: strings.Repeat("a", 64), Name: "local", ContentHash: strings.Repeat("b", 64)}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := signedRequest(t, key, http.MethodPost, "/v1/local/mcp/capture", body, server.now(), "capture-nonce-000001", "")
	recorder := httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte("plaintext-capture-value")) {
		t.Fatalf("capture status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	err = server.journal.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketIdempotency)
		if bucket.Stats().KeyN != 0 {
			return errors.New("ephemeral capture created an idempotency record")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
