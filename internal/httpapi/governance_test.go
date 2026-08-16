package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/config"
	"github.com/Junhao2314/toolhub/internal/store"
)

type relayEnforcementTestBridge struct {
	calls       []string
	capability  bridgeprotocol.RelayCapabilityResponse
	inspections map[string]bridgeprotocol.NativeClientInspectionResponse
	canary      bridgeprotocol.RelaySessionCanaryResponse
}

func (bridge *relayEnforcementTestBridge) RelayCapability(context.Context) (bridgeprotocol.RelayCapabilityResponse, error) {
	bridge.calls = append(bridge.calls, "capability")
	return bridge.capability, nil
}

func (bridge *relayEnforcementTestBridge) InspectNativeClient(_ context.Context, input bridgeprotocol.NativeClientInspectionRequest) (bridgeprotocol.NativeClientInspectionResponse, error) {
	bridge.calls = append(bridge.calls, input.ClientKind)
	return bridge.inspections[input.ClientKind], nil
}

func (bridge *relayEnforcementTestBridge) RelaySessionCanary(_ context.Context, input bridgeprotocol.RelaySessionCanaryRequest) (bridgeprotocol.RelaySessionCanaryResponse, error) {
	bridge.calls = append(bridge.calls, "session-canary")
	bridge.canary.RoutingBundleHash = input.RoutingBundleHash
	return bridge.canary, nil
}

func TestGovernanceRoutesRequireSessionAndCSRF(t *testing.T) {
	harness := newGovernanceHarness(t)
	profileID := "11111111-1111-4111-8111-111111111111"
	revisionID := "22222222-2222-4222-8222-222222222222"
	proposalID := "33333333-3333-4333-8333-333333333333"
	challengeID := "44444444-4444-4444-8444-444444444444"
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/relay/configuration"},
		{http.MethodPut, "/api/v1/relay/configuration"},
		{http.MethodPost, "/api/v1/relay/configuration/preflight"},
		{http.MethodPost, "/api/v1/relay/configuration/apply"},
		{http.MethodPost, "/api/v1/relay/configuration/prepare-profile-updates"},
		{http.MethodGet, "/api/v1/relay/contracts"},
		{http.MethodPost, "/api/v1/relay/contracts/observe"},
		{http.MethodPost, "/api/v1/relay/contracts/" + revisionID + "/accept"},
		{http.MethodPost, "/api/v1/relay/renames/" + proposalID + "/confirm"},
		{http.MethodGet, "/api/v1/mcp/policy"},
		{http.MethodPut, "/api/v1/mcp/policy"},
		{http.MethodPost, "/api/v1/mcp/policy/apply"},
		{http.MethodGet, "/api/v1/profiles/" + profileID + "/launch"},
		{http.MethodGet, "/api/v1/relay/confirmations"},
		{http.MethodPost, "/api/v1/relay/confirmations/" + challengeID + "/approve"},
		{http.MethodPost, "/api/v1/relay/confirmations/" + challengeID + "/reject"},
		{http.MethodGet, "/api/v1/relay/observations/live"},
		{http.MethodGet, "/api/v1/relay/observations/daily"},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path+" requires session", func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, nil)
			recorder := httptest.NewRecorder()
			harness.router.ServeHTTP(recorder, request)
			assertGovernanceError(t, recorder, http.StatusUnauthorized, "unauthenticated")
		})
		if route.method == http.MethodGet {
			continue
		}
		t.Run(route.method+" "+route.path+" requires csrf", func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, nil)
			request.AddCookie(&http.Cookie{Name: "toolhub_session", Value: harness.sessionToken})
			recorder := httptest.NewRecorder()
			harness.router.ServeHTTP(recorder, request)
			assertGovernanceError(t, recorder, http.StatusForbidden, "csrf_invalid")
		})
	}
}

func TestGovernanceConfigurationAndPolicyValidation(t *testing.T) {
	harness := newGovernanceHarness(t)

	relay := harness.request(t, http.MethodGet, "/api/v1/relay/configuration", "")
	if relay.Code != http.StatusOK || !strings.Contains(relay.Body.String(), `"current"`) || !strings.Contains(relay.Body.String(), `"applied"`) {
		t.Fatalf("relay projection status=%d body=%s", relay.Code, relay.Body.String())
	}
	policy := harness.request(t, http.MethodGet, "/api/v1/mcp/policy", "")
	if policy.Code != http.StatusOK || !strings.Contains(policy.Body.String(), `"current"`) || !strings.Contains(policy.Body.String(), `"applied"`) {
		t.Fatalf("policy projection status=%d body=%s", policy.Code, policy.Body.String())
	}

	invalid := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "relay unknown field", method: http.MethodPut, path: "/api/v1/relay/configuration", body: `{"revision":1,"mcpServerIds":[],"mcpRevisionIds":{},"metadata":{},"secretValue":"leak"}`},
		{name: "relay negative revision", method: http.MethodPut, path: "/api/v1/relay/configuration", body: `{"revision":-1,"mcpServerIds":[],"mcpRevisionIds":{},"metadata":{}}`},
		{name: "relay invalid uuid", method: http.MethodPut, path: "/api/v1/relay/configuration", body: `{"revision":1,"mcpServerIds":["not-a-uuid"],"mcpRevisionIds":{"not-a-uuid":"also-invalid"},"metadata":{}}`},
		{name: "relay preflight missing mode", method: http.MethodPost, path: "/api/v1/relay/configuration/preflight", body: `{"revisionId":"11111111-1111-4111-8111-111111111111","profileIds":[]}`},
		{name: "relay preflight invalid mode", method: http.MethodPost, path: "/api/v1/relay/configuration/preflight", body: `{"revisionId":"11111111-1111-4111-8111-111111111111","profileIds":[],"mode":"automatic"}`},
		{name: "policy unknown field", method: http.MethodPut, path: "/api/v1/mcp/policy", body: `{"revision":1,"catalogVersion":1,"explicitOverrides":{},"unclassifiedMutating":"confirm","reviewedReadOnly":"allow","arguments":{}}`},
		{name: "policy negative revision", method: http.MethodPut, path: "/api/v1/mcp/policy", body: `{"revision":-1,"catalogVersion":1,"explicitOverrides":{},"unclassifiedMutating":"confirm","reviewedReadOnly":"allow"}`},
		{name: "policy invalid decision", method: http.MethodPut, path: "/api/v1/mcp/policy", body: `{"revision":1,"catalogVersion":1,"explicitOverrides":{},"unclassifiedMutating":"execute","reviewedReadOnly":"allow"}`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			recorder := harness.request(t, test.method, test.path, test.body)
			assertGovernanceError(t, recorder, http.StatusBadRequest, "invalid_request")
		})
	}

	oversized := `{"revision":1,"mcpServerIds":[],"mcpRevisionIds":{},"metadata":{"note":"` + strings.Repeat("x", 70<<10) + `"}}`
	assertGovernanceError(t, harness.request(t, http.MethodPut, "/api/v1/relay/configuration", oversized), http.StatusBadRequest, "invalid_request")

	createdRelay := harness.request(t, http.MethodPut, "/api/v1/relay/configuration", `{"revision":1,"mcpServerIds":[],"mcpRevisionIds":{},"metadata":{"label":"candidate"}}`)
	if createdRelay.Code != http.StatusOK {
		t.Fatalf("create relay revision status=%d body=%s", createdRelay.Code, createdRelay.Body.String())
	}
	assertGovernanceError(t, harness.request(t, http.MethodPut, "/api/v1/relay/configuration", `{"revision":1,"mcpServerIds":[],"mcpRevisionIds":{},"metadata":{"label":"stale"}}`), http.StatusConflict, "state_conflict")

	createdPolicy := harness.request(t, http.MethodPut, "/api/v1/mcp/policy", `{"revision":1,"catalogVersion":1,"explicitOverrides":{},"unclassifiedMutating":"deny","reviewedReadOnly":"allow"}`)
	if createdPolicy.Code != http.StatusOK {
		t.Fatalf("create policy revision status=%d body=%s", createdPolicy.Code, createdPolicy.Body.String())
	}
	assertGovernanceError(t, harness.request(t, http.MethodPut, "/api/v1/mcp/policy", `{"revision":1,"catalogVersion":1,"explicitOverrides":{},"unclassifiedMutating":"confirm","reviewedReadOnly":"deny"}`), http.StatusConflict, "state_conflict")
}

func TestRelayConfigurationProjectionIncludesBoundedRuntimeCapability(t *testing.T) {
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/relay/governance/capability" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, bridgeprotocol.RelayCapabilityResponse{
			AdminProtocolVersion: 1,
			Features: []string{
				"profile-session-binding", "tool-filtering", "call-policy",
				"one-shot-confirmation", "payload-free-observations", "routing-hot-reload", "session-canary",
				"untrusted-extra-feature",
			},
			RoutingSchemaVersions: []int{1},
			Runtime:               "mcpm",
			RuntimeVersion:        "2.15.0-toolhub.1",
		})
	}))

	response := harness.request(t, http.MethodGet, "/api/v1/relay/configuration", "")
	if response.Code != http.StatusOK {
		t.Fatalf("relay projection status=%d body=%s", response.Code, response.Body.String())
	}
	var projection struct {
		RuntimeCapability struct {
			Compatible     bool     `json:"compatible"`
			RuntimeVersion string   `json:"runtimeVersion"`
			Features       []string `json:"features"`
			ErrorCode      string   `json:"errorCode"`
		} `json:"runtimeCapability"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	capability := projection.RuntimeCapability
	if !capability.Compatible || capability.RuntimeVersion != "2.15.0-toolhub.1" || capability.ErrorCode != "" {
		t.Fatalf("runtime capability=%+v", capability)
	}
	wantFeatures := "profile-session-binding,tool-filtering,call-policy,one-shot-confirmation,payload-free-observations,routing-hot-reload,session-canary"
	if got := strings.Join(capability.Features, ","); got != wantFeatures {
		t.Fatalf("runtime features=%q want=%q", got, wantFeatures)
	}
}

func TestRelayEnforcementPreflightRunsCandidateSessionCanaryLast(t *testing.T) {
	bundle, body, hash := httpEnforcementCanaryBundle(t)
	bridge := &relayEnforcementTestBridge{
		capability: bridgeprotocol.RelayCapabilityResponse{AdminProtocolVersion: 1, Features: []string{"profile-session-binding", "tool-filtering", "call-policy", "one-shot-confirmation", "payload-free-observations", "routing-hot-reload", "session-canary"}, RoutingSchemaVersions: []int{1}, Runtime: "mcpm", RuntimeVersion: "2.15.0-toolhub.1"},
		inspections: map[string]bridgeprotocol.NativeClientInspectionResponse{
			bridgeprotocol.RuntimeClaude: {ClientKind: bridgeprotocol.RuntimeClaude, Version: "2.1.232", Supported: true},
			bridgeprotocol.RuntimeCodex:  {ClientKind: bridgeprotocol.RuntimeCodex, Version: "0.147.0", Supported: true},
		},
		canary: bridgeprotocol.RelaySessionCanaryResponse{
			Profiles: []bridgeprotocol.RelaySessionCanaryProfile{
				{ClientKind: bridgeprotocol.RuntimeClaude, ProfileID: bundle.Profiles[0].ProfileID, ProfileRevisionID: bundle.Profiles[0].ProfileRevisionID, ToolCount: 0},
				{ClientKind: bridgeprotocol.RuntimeCodex, ProfileID: bundle.Profiles[1].ProfileID, ProfileRevisionID: bundle.Profiles[1].ProfileRevisionID, ToolCount: 0},
			},
			MissingProfile: bridgeprotocol.RelaySessionCanaryMissing{Behavior: "empty", ToolCount: 0}, InvalidProfileErrorCode: "profile_invalid", ConcurrentSessionCount: 2, UpstreamProcesses: []bridgeprotocol.RelaySessionCanaryProcess{},
		},
	}
	request := bridgeprotocol.RelaySessionCanaryRequest{RoutingBundleHash: hash, RoutingBundle: body}
	if apiErr := validateRelayEnforcementRuntime(context.Background(), bridge, "runner", request); apiErr != nil {
		t.Fatalf("valid enforcement runtime rejected: %+v", apiErr)
	}
	if got := strings.Join(bridge.calls, ","); got != "capability,claude,codex,session-canary" {
		t.Fatalf("enforcement check order=%s", got)
	}

	bridge.calls = nil
	bridge.canary.ConcurrentSessionCount = 1
	if apiErr := validateRelayEnforcementRuntime(context.Background(), bridge, "runner", request); apiErr == nil || apiErr.Code != bridgeprotocol.ErrMCPMIncompatible {
		t.Fatalf("invalid session canary error=%+v", apiErr)
	}
}

func httpEnforcementCanaryBundle(t *testing.T) (bridgeprotocol.RoutingBundle, json.RawMessage, string) {
	t.Helper()
	bundle := bridgeprotocol.RoutingBundle{
		SchemaVersion: 1, Mode: "enforced", RelayConfigurationRevisionID: "00000000-0000-0000-0000-000000000001", RelayConfigurationHash: strings.Repeat("a", 64), GlobalPolicyRevisionID: "00000000-0000-0000-0000-000000000002", GlobalPolicyHash: strings.Repeat("b", 64), Servers: []bridgeprotocol.ServerContractDTO{},
		Profiles: []bridgeprotocol.PublishedProfileDTO{
			{ProfileID: "00000000-0000-0000-0000-000000000010", ProfileRevisionID: "00000000-0000-0000-0000-000000000011", ProfileRevisionHash: strings.Repeat("c", 64), ProfileName: "claude", ClientKind: bridgeprotocol.RuntimeClaude, Servers: []bridgeprotocol.ProfileServerRoutingDTO{}},
			{ProfileID: "00000000-0000-0000-0000-000000000020", ProfileRevisionID: "00000000-0000-0000-0000-000000000021", ProfileRevisionHash: strings.Repeat("d", 64), ProfileName: "codex", ClientKind: bridgeprotocol.RuntimeCodex, Servers: []bridgeprotocol.ProfileServerRoutingDTO{}},
		},
	}
	body, hash, err := bundle.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return bundle, body, hash
}

func TestRelayContractProjectionIncludesAppliedPolicyClassification(t *testing.T) {
	harness := newGovernanceHarness(t)
	ctx := context.Background()
	server, err := harness.store.SaveMCPServer(ctx, "", store.MCPInput{Name: "api-classified-contract", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := harness.store.ObserveContracts(ctx, store.ContractObservationInput{ServerID: server.ID, Tools: []store.ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.store.AcceptContract(ctx, server.ID, observed.Revision.ID); err != nil {
		t.Fatal(err)
	}

	response := harness.request(t, http.MethodGet, "/api/v1/relay/contracts", "")
	if response.Code != http.StatusOK {
		t.Fatalf("contract projection status=%d body=%s", response.Code, response.Body.String())
	}
	for _, field := range []string{`"status":"new_hidden"`, `"globalDecision":"allow"`, `"reasonCodes":["annotation_read_only","reviewed_read_only"]`} {
		if !strings.Contains(response.Body.String(), field) {
			t.Fatalf("contract projection missing %s: %s", field, response.Body.String())
		}
	}
}

func TestGovernanceApplyRoutesCreateBoundDurableOperations(t *testing.T) {
	const targetRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var preflightManifest bridgeprotocol.DesiredManifest
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/targets/preflight" {
			http.NotFound(w, r)
			return
		}
		var input bridgeprotocol.PreflightRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode preflight: %v", err)
		}
		preflightManifest = input.Manifest
		_, manifestHash, err := input.Manifest.Canonical()
		if err != nil {
			t.Errorf("canonicalize preflight manifest: %v", err)
			http.Error(w, "invalid manifest", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, bridgeprotocol.PreflightResponse{
			TargetRevision: targetRevision,
			ManifestHash:   manifestHash,
			Diff: bridgeprotocol.Diff{
				Add: []bridgeprotocol.DiffItem{}, Replace: []bridgeprotocol.DiffItem{},
				Delete: []bridgeprotocol.DiffItem{}, Excluded: []bridgeprotocol.DiffItem{},
			},
		})
	}))
	ctx := context.Background()
	relayDraft, err := harness.store.SaveRelayConfiguration(ctx, store.RelayConfigurationInput{Revision: 1, MCPServerIDs: []string{}, MCPRevisionIDs: map[string]string{}, Metadata: map[string]any{"label": "apply"}})
	if err != nil {
		t.Fatal(err)
	}
	policyDraft, err := harness.store.SaveGlobalPolicy(ctx, store.GlobalPolicyInput{Revision: 1, CatalogVersion: 1, ExplicitOverrides: map[string]string{}, UnclassifiedMutating: "deny", ReviewedReadOnly: "allow"})
	if err != nil {
		t.Fatal(err)
	}

	prepared := harness.request(t, http.MethodPost, "/api/v1/relay/configuration/prepare-profile-updates", `{"revisionId":"`+relayDraft.ID+`"}`)
	if prepared.Code != http.StatusOK || !strings.Contains(prepared.Body.String(), `"items":[]`) {
		t.Fatalf("prepare status=%d body=%s", prepared.Code, prepared.Body.String())
	}
	preflight := harness.request(t, http.MethodPost, "/api/v1/relay/configuration/preflight", `{"revisionId":"`+relayDraft.ID+`","profileIds":[],"mode":"compatibility"}`)
	if preflight.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s", preflight.Code, preflight.Body.String())
	}
	var preflightResponse struct {
		RoutingHash string `json:"routingHash"`
	}
	if err := json.Unmarshal(preflight.Body.Bytes(), &preflightResponse); err != nil {
		t.Fatal(err)
	}
	if preflightResponse.RoutingHash == "" || preflightManifest.RelayGovernance == nil || preflightManifest.RelayGovernance.RoutingHash != preflightResponse.RoutingHash {
		t.Fatalf("preflight routing hash was not bound: response=%+v manifest=%+v", preflightResponse, preflightManifest.RelayGovernance)
	}
	var routingBundle bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(preflightManifest.RelayGovernance.RoutingBundle, &routingBundle); err != nil || routingBundle.Mode != "compatibility" {
		t.Fatalf("preflight routing mode=%q err=%v", routingBundle.Mode, err)
	}

	relayApply := harness.request(t, http.MethodPost, "/api/v1/relay/configuration/apply", `{"revisionId":"`+relayDraft.ID+`","profileIds":[],"mode":"compatibility","targetRevision":"`+targetRevision+`","routingHash":"`+preflightResponse.RoutingHash+`"}`)
	relayOperationID := assertQueuedGovernanceOperation(t, relayApply, "relay_config_apply")
	relayOperation, err := harness.store.Operation(ctx, relayOperationID)
	if err != nil {
		t.Fatal(err)
	}
	var relayMetadata map[string]any
	if err := json.Unmarshal(relayOperation.Metadata, &relayMetadata); err != nil {
		t.Fatal(err)
	}
	if relayMetadata["mode"] != "compatibility" {
		t.Fatalf("relay operation mode=%v", relayMetadata["mode"])
	}
	if err := harness.store.CancelOperation(ctx, relayOperationID); err != nil {
		t.Fatal(err)
	}
	policyApply := harness.request(t, http.MethodPost, "/api/v1/mcp/policy/apply", `{"revisionId":"`+policyDraft.ID+`","targetRevision":"`+targetRevision+`"}`)
	assertQueuedGovernanceOperation(t, policyApply, "policy_apply")
}

func TestGovernancePreflightPreservesBrowserIdempotencyKey(t *testing.T) {
	const targetRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var bridgeIdempotencyKey string
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/targets/preflight" {
			http.NotFound(w, r)
			return
		}
		bridgeIdempotencyKey = r.Header.Get(bridgeprotocol.HeaderIdempotencyKey)
		var input bridgeprotocol.PreflightRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode preflight: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_, manifestHash, err := input.Manifest.Canonical()
		if err != nil {
			t.Errorf("canonicalize preflight manifest: %v", err)
			http.Error(w, "invalid manifest", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, bridgeprotocol.PreflightResponse{
			TargetRevision: targetRevision,
			ManifestHash:   manifestHash,
			Diff: bridgeprotocol.Diff{
				Add: []bridgeprotocol.DiffItem{}, Replace: []bridgeprotocol.DiffItem{},
				Delete: []bridgeprotocol.DiffItem{}, Excluded: []bridgeprotocol.DiffItem{},
			},
		})
	}))
	relayDraft, err := harness.store.SaveRelayConfiguration(context.Background(), store.RelayConfigurationInput{Revision: 1, MCPServerIDs: []string{}, MCPRevisionIDs: map[string]string{}, Metadata: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}

	wantKey := strings.Repeat("k", 200)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/relay/configuration/preflight", strings.NewReader(`{"revisionId":"`+relayDraft.ID+`","profileIds":[],"mode":"compatibility"}`))
	request.AddCookie(&http.Cookie{Name: "toolhub_session", Value: harness.sessionToken})
	request.Header.Set("X-CSRF-Token", harness.csrfToken)
	request.Header.Set("Idempotency-Key", wantKey)
	recorder := httptest.NewRecorder()
	harness.router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if bridgeIdempotencyKey != wantKey {
		t.Fatalf("Bridge idempotency key length=%d want=%d key=%q", len(bridgeIdempotencyKey), len(wantKey), bridgeIdempotencyKey)
	}
}

func TestValidBrowserPreflightResponseRejectsUnboundOrUnsafeDiff(t *testing.T) {
	memberID := "11111111-1111-4111-8111-111111111111"
	manifest := bridgeprotocol.DesiredManifest{
		SchemaVersion: bridgeprotocol.ManifestSchemaVersion,
		Target: bridgeprotocol.Target{
			ID: "22222222-2222-4222-8222-222222222222", NodeID: "33333333-3333-4333-8333-333333333333",
			NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: "runner",
		},
		Skills: []bridgeprotocol.SkillMember{{
			MemberID: memberID, SkillID: "44444444-4444-4444-8444-444444444444", VersionID: "55555555-5555-4555-8555-555555555555",
			Slug: "formatter", SHA256: strings.Repeat("a", 64), ContentHash: strings.Repeat("b", 64),
		}},
		MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{memberID},
	}
	_, manifestHash, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	valid := func() bridgeprotocol.PreflightResponse {
		return bridgeprotocol.PreflightResponse{
			TargetRevision: strings.Repeat("c", 64), ManifestHash: manifestHash,
			Diff: bridgeprotocol.Diff{
				Add:     []bridgeprotocol.DiffItem{{Kind: "skill", MemberID: memberID, Name: "formatter"}},
				Replace: []bridgeprotocol.DiffItem{}, Delete: []bridgeprotocol.DiffItem{}, Excluded: []bridgeprotocol.DiffItem{},
			},
		}
	}
	if !validBrowserPreflightResponse(manifest, valid()) {
		t.Fatal("valid preflight response was rejected")
	}
	for _, test := range []struct {
		name   string
		mutate func(*bridgeprotocol.PreflightResponse)
	}{
		{name: "overlong manageable delete", mutate: func(item *bridgeprotocol.PreflightResponse) {
			item.Diff.Add = []bridgeprotocol.DiffItem{}
			item.Diff.Delete = []bridgeprotocol.DiffItem{{Kind: "skill", Name: strings.Repeat("x", 129)}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := valid()
			test.mutate(&response)
			if validBrowserPreflightResponse(manifest, response) {
				t.Fatal("overlong manageable preflight item was accepted")
			}
		})
	}
	for _, nameLength := range []int{129, 255} {
		response := valid()
		response.Diff.Add = []bridgeprotocol.DiffItem{}
		response.Diff.Excluded = []bridgeprotocol.DiffItem{{Kind: "entry", Name: strings.Repeat("x", nameLength), Reason: "protected"}}
		if !validBrowserPreflightResponse(manifest, response) {
			t.Fatalf("protected %d-byte runtime entry was rejected", nameLength)
		}
	}
	tests := []struct {
		name   string
		mutate func(*bridgeprotocol.PreflightResponse)
	}{
		{name: "invalid target revision", mutate: func(item *bridgeprotocol.PreflightResponse) { item.TargetRevision = "invalid" }},
		{name: "manifest hash mismatch", mutate: func(item *bridgeprotocol.PreflightResponse) { item.ManifestHash = strings.Repeat("d", 64) }},
		{name: "nil diff collection", mutate: func(item *bridgeprotocol.PreflightResponse) { item.Diff.Delete = nil }},
		{name: "too many diff items", mutate: func(item *bridgeprotocol.PreflightResponse) {
			item.Diff.Delete = make([]bridgeprotocol.DiffItem, 10001)
			for index := range item.Diff.Delete {
				item.Diff.Delete[index] = bridgeprotocol.DiffItem{Kind: "skill", Name: "old-skill-" + strconv.Itoa(index)}
			}
		}},
		{name: "unknown kind", mutate: func(item *bridgeprotocol.PreflightResponse) { item.Diff.Add[0].Kind = "secret" }},
		{name: "inventory-only kind in add", mutate: func(item *bridgeprotocol.PreflightResponse) { item.Diff.Add[0].Kind = "entry" }},
		{name: "member binding mismatch", mutate: func(item *bridgeprotocol.PreflightResponse) {
			item.Diff.Add[0].MemberID = "66666666-6666-4666-8666-666666666666"
		}},
		{name: "raw reason", mutate: func(item *bridgeprotocol.PreflightResponse) { item.Diff.Add[0].Reason = "raw upstream error" }},
		{name: "inventory-only kind in delete", mutate: func(item *bridgeprotocol.PreflightResponse) {
			item.Diff.Add = []bridgeprotocol.DiffItem{}
			item.Diff.Delete = []bridgeprotocol.DiffItem{{Kind: "entry", Name: "preserved-file"}}
		}},
		{name: "overlong inventory name", mutate: func(item *bridgeprotocol.PreflightResponse) {
			item.Diff.Add = []bridgeprotocol.DiffItem{}
			item.Diff.Excluded = []bridgeprotocol.DiffItem{{Kind: "entry", Name: strings.Repeat("x", 256), Reason: "protected"}}
		}},
		{name: "nul inventory name", mutate: func(item *bridgeprotocol.PreflightResponse) {
			item.Diff.Add = []bridgeprotocol.DiffItem{}
			item.Diff.Excluded = []bridgeprotocol.DiffItem{{Kind: "entry", Name: "bad\x00name", Reason: "protected"}}
		}},
		{name: "duplicate across groups", mutate: func(item *bridgeprotocol.PreflightResponse) {
			item.Diff.Replace = append(item.Diff.Replace, item.Diff.Add[0])
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := valid()
			test.mutate(&response)
			if validBrowserPreflightResponse(manifest, response) {
				t.Fatal("invalid preflight response was accepted")
			}
		})
	}
}

func TestGovernanceRelayPreflightRejectsInvalidBridgeResponse(t *testing.T) {
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/targets/preflight" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, bridgeprotocol.PreflightResponse{
			TargetRevision: strings.Repeat("a", 64), ManifestHash: strings.Repeat("b", 64),
			Diff: bridgeprotocol.Diff{Add: []bridgeprotocol.DiffItem{}, Replace: []bridgeprotocol.DiffItem{}, Delete: []bridgeprotocol.DiffItem{}, Excluded: []bridgeprotocol.DiffItem{}},
		})
	}))
	relayDraft, err := harness.store.SaveRelayConfiguration(context.Background(), store.RelayConfigurationInput{
		Revision: 1, MCPServerIDs: []string{}, MCPRevisionIDs: map[string]string{}, Metadata: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}

	response := harness.request(t, http.MethodPost, "/api/v1/relay/configuration/preflight", `{"revisionId":"`+relayDraft.ID+`","profileIds":[],"mode":"compatibility"}`)
	assertGovernanceError(t, response, http.StatusBadGateway, "relay_response_invalid")
}

func TestGovernanceContractRoutesValidateAndQueueObservation(t *testing.T) {
	harness := newGovernanceHarness(t)
	contracts := harness.request(t, http.MethodGet, "/api/v1/relay/contracts", "")
	if contracts.Code != http.StatusOK || !strings.Contains(contracts.Body.String(), `"items":[]`) || !strings.Contains(contracts.Body.String(), `"renames":[]`) {
		t.Fatalf("contracts status=%d body=%s", contracts.Code, contracts.Body.String())
	}
	observe := harness.request(t, http.MethodPost, "/api/v1/relay/contracts/observe", `{}`)
	assertQueuedGovernanceOperation(t, observe, "contract_observe")

	invalid := []struct {
		name string
		path string
		body string
	}{
		{name: "invalid contract revision", path: "/api/v1/relay/contracts/not-a-uuid/accept", body: `{}`},
		{name: "contract unknown body", path: "/api/v1/relay/contracts/22222222-2222-4222-8222-222222222222/accept", body: `{"arguments":{}}`},
		{name: "invalid rename proposal", path: "/api/v1/relay/renames/not-a-uuid/confirm", body: `{}`},
		{name: "rename unknown body", path: "/api/v1/relay/renames/33333333-3333-4333-8333-333333333333/confirm", body: `{"rawError":"leak"}`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			assertGovernanceError(t, harness.request(t, http.MethodPost, test.path, test.body), http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestGovernanceConfirmationDecisionIsProfileBoundAndPayloadFree(t *testing.T) {
	const challengeID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const bindingHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	grantExpiresAt := float64(time.Now().Add(30*time.Second).UnixNano()) / float64(time.Second)
	var summary bridgeprotocol.ConfirmationSummary
	decisionCalls := 0
	decisionStatus := http.StatusOK
	decisionResponse := bridgeprotocol.ConfirmationDecisionResponse{ChallengeID: challengeID, BindingHash: bindingHash, GrantExpiresAt: &grantExpiresAt}
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/relay/governance/confirmations/list":
			writeJSON(w, http.StatusOK, bridgeprotocol.ConfirmationListResponse{Items: []bridgeprotocol.ConfirmationSummary{summary}})
		case "/v1/relay/governance/confirmations/approve", "/v1/relay/governance/confirmations/reject":
			decisionCalls++
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Errorf("decode decision: %v", err)
			}
			if len(input) != 2 || input["challengeId"] != challengeID || input["bindingHash"] != bindingHash {
				t.Errorf("unsafe decision body=%v", input)
			}
			if decisionStatus != http.StatusOK {
				writeJSON(w, decisionStatus, map[string]any{"error": map[string]any{"code": bridgeprotocol.ErrRelayUnhealthy, "message": "decision transport failed", "retryable": true}})
				return
			}
			writeJSON(w, http.StatusOK, decisionResponse)
		default:
			http.NotFound(w, r)
		}
	}))
	profile, err := harness.store.SaveProfile(context.Background(), "11111111-1111-4111-8111-111111111111", store.ProfileInput{Name: "claude-coding", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.store.Pool().Exec(context.Background(), `INSERT INTO published_profiles(profile_id,profile_revision_id) VALUES($1,$2)`, profile.ID, profile.CurrentRevisionID); err != nil {
		t.Fatal(err)
	}
	draft, err := harness.store.SaveProfile(context.Background(), profile.ID, store.ProfileInput{Name: "claude-coding-draft", ClientKind: "claude", Category: "coding", Revision: profile.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if draft.CurrentRevisionID == profile.CurrentRevisionID {
		t.Fatal("draft did not advance the current Profile revision")
	}
	summary = bridgeprotocol.ConfirmationSummary{
		ChallengeID: challengeID, BindingHash: bindingHash, ArgumentHash: strings.Repeat("a", 64), CreatedAt: 1, ExpiresAt: 2,
		ProfileID: profile.ID, ProfileRevisionID: profile.CurrentRevisionID, ProfileName: profile.Name, ClientKind: profile.ClientKind,
		ServerID: "22222222-2222-4222-8222-222222222222", ServerName: "acemcp", ToolID: "33333333-3333-4333-8333-333333333333", ToolName: "search", RuntimeName: "search",
		MCPConfigRevisionID: "44444444-4444-4444-8444-444444444444", ContractRevisionID: "55555555-5555-4555-8555-555555555555", GlobalPolicyRevisionID: "66666666-6666-4666-8666-666666666666",
		Decision: "confirm", ReasonCodes: []string{"annotation_mutating"}, ArgumentSummary: []bridgeprotocol.ArgumentSummary{{Pointer: "/o0", ValueType: "string", Sensitive: true}},
	}

	listed := harness.request(t, http.MethodGet, "/api/v1/relay/confirmations", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), challengeID) || strings.Contains(listed.Body.String(), "secret scalar") {
		t.Fatalf("confirmation list status=%d body=%s", listed.Code, listed.Body.String())
	}
	mismatch := harness.request(t, http.MethodPost, "/api/v1/relay/confirmations/"+challengeID+"/approve", `{"profileName":"Claude-Coding","bindingHash":"`+bindingHash+`"}`)
	assertGovernanceError(t, mismatch, http.StatusConflict, "confirmation_binding_mismatch")
	if decisionCalls != 0 {
		t.Fatalf("binding mismatch reached Bridge %d times", decisionCalls)
	}
	unknown := harness.request(t, http.MethodPost, "/api/v1/relay/confirmations/"+challengeID+"/approve", `{"profileName":"claude-coding","bindingHash":"`+bindingHash+`","arguments":{"query":"secret scalar"}}`)
	assertGovernanceError(t, unknown, http.StatusBadRequest, "invalid_request")
	overlongName := harness.request(t, http.MethodPost, "/api/v1/relay/confirmations/"+challengeID+"/approve", `{"profileName":"`+strings.Repeat("p", 121)+`","bindingHash":"`+bindingHash+`"}`)
	assertGovernanceError(t, overlongName, http.StatusBadRequest, "invalid_request")

	approved := harness.request(t, http.MethodPost, "/api/v1/relay/confirmations/"+challengeID+"/approve", `{"profileName":"claude-coding","bindingHash":"`+bindingHash+`"}`)
	if approved.Code != http.StatusOK || decisionCalls != 1 {
		t.Fatalf("approve status=%d calls=%d body=%s", approved.Code, decisionCalls, approved.Body.String())
	}
	decisionResponse.GrantExpiresAt = nil
	rejected := harness.request(t, http.MethodPost, "/api/v1/relay/confirmations/"+challengeID+"/reject", `{"bindingHash":"`+bindingHash+`"}`)
	if rejected.Code != http.StatusOK || decisionCalls != 2 {
		t.Fatalf("reject status=%d calls=%d body=%s", rejected.Code, decisionCalls, rejected.Body.String())
	}
	decisionResponse.BindingHash = strings.Repeat("d", 64)
	invalidResponse := harness.request(t, http.MethodPost, "/api/v1/relay/confirmations/"+challengeID+"/approve", `{"profileName":"claude-coding","bindingHash":"`+bindingHash+`"}`)
	assertGovernanceError(t, invalidResponse, http.StatusBadGateway, "confirmation_outcome_unknown")
	decisionResponse.BindingHash = bindingHash
	decisionStatus = http.StatusServiceUnavailable
	unknownOutcome := harness.request(t, http.MethodPost, "/api/v1/relay/confirmations/"+challengeID+"/approve", `{"profileName":"claude-coding","bindingHash":"`+bindingHash+`"}`)
	assertGovernanceError(t, unknownOutcome, http.StatusBadGateway, "confirmation_outcome_unknown")
	if decisionCalls != 4 {
		t.Fatalf("decision calls=%d want=4", decisionCalls)
	}
	audit, err := harness.store.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"arguments", "results", "prompts", "rawError", "secretValue", "sessionId", "secret scalar"} {
		if strings.Contains(strings.ToLower(string(audit)), strings.ToLower(forbidden)) {
			t.Fatalf("confirmation audit leaked %q: %s", forbidden, audit)
		}
	}
	var auditEvents []struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(audit, &auditEvents); err != nil {
		t.Fatal(err)
	}
	unknownAudited := false
	for _, event := range auditEvents {
		if event.Metadata["outcome"] == "unknown" {
			unknownAudited = true
			break
		}
	}
	if !unknownAudited {
		t.Fatalf("unknown confirmation outcome was not audited: %s", audit)
	}
}

func TestValidConfirmationDecisionResponseEnforcesGrantTTL(t *testing.T) {
	const challengeID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const bindingHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	now := float64(time.Now().UnixNano()) / float64(time.Second)
	validExpiry := now + 30
	expired := now - 1
	overlong := now + 61
	nan := math.NaN()
	positiveInfinity := math.Inf(1)
	summary := bridgeprotocol.ConfirmationSummary{ChallengeID: challengeID, BindingHash: bindingHash}
	tests := []struct {
		name    string
		approve bool
		expires *float64
		want    bool
	}{
		{name: "approval requires expiry", approve: true, want: false},
		{name: "approval accepts finite grant within ttl", approve: true, expires: &validExpiry, want: true},
		{name: "approval rejects expired grant", approve: true, expires: &expired, want: false},
		{name: "approval rejects grant beyond ttl", approve: true, expires: &overlong, want: false},
		{name: "approval rejects NaN expiry", approve: true, expires: &nan, want: false},
		{name: "approval rejects infinite expiry", approve: true, expires: &positiveInfinity, want: false},
		{name: "rejection omits expiry", approve: false, want: true},
		{name: "rejection forbids expiry", approve: false, expires: &validExpiry, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := bridgeprotocol.ConfirmationDecisionResponse{ChallengeID: challengeID, BindingHash: bindingHash, GrantExpiresAt: test.expires}
			if got := validConfirmationDecisionResponse(response, summary, test.approve); got != test.want {
				t.Fatalf("validConfirmationDecisionResponse()=%t want=%t expiry=%v", got, test.want, test.expires)
			}
		})
	}
}

func TestGovernanceConfirmationDecisionFailsClosedWhenAuditCannotPersist(t *testing.T) {
	const challengeID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const bindingHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	grantExpiresAt := float64(time.Now().Add(30*time.Second).UnixNano()) / float64(time.Second)
	var summary bridgeprotocol.ConfirmationSummary
	decisionCalls := 0
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/relay/governance/confirmations/list":
			writeJSON(w, http.StatusOK, bridgeprotocol.ConfirmationListResponse{Items: []bridgeprotocol.ConfirmationSummary{summary}})
		case "/v1/relay/governance/confirmations/approve":
			decisionCalls++
			writeJSON(w, http.StatusOK, bridgeprotocol.ConfirmationDecisionResponse{ChallengeID: challengeID, BindingHash: bindingHash, GrantExpiresAt: &grantExpiresAt})
		default:
			http.NotFound(w, r)
		}
	}))
	profile, err := harness.store.SaveProfile(context.Background(), "11111111-1111-4111-8111-111111111111", store.ProfileInput{Name: "claude-audit-failure", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.store.Pool().Exec(context.Background(), `INSERT INTO published_profiles(profile_id,profile_revision_id) VALUES($1,$2)`, profile.ID, profile.CurrentRevisionID); err != nil {
		t.Fatal(err)
	}
	summary = bridgeprotocol.ConfirmationSummary{
		ChallengeID: challengeID, BindingHash: bindingHash, ArgumentHash: strings.Repeat("a", 64), CreatedAt: 1, ExpiresAt: 2,
		ProfileID: profile.ID, ProfileRevisionID: profile.CurrentRevisionID, ProfileName: profile.Name, ClientKind: profile.ClientKind,
		ServerID: "22222222-2222-4222-8222-222222222222", ServerName: "acemcp", ToolID: "33333333-3333-4333-8333-333333333333", ToolName: "search", RuntimeName: "search",
		MCPConfigRevisionID: "44444444-4444-4444-8444-444444444444", ContractRevisionID: "55555555-5555-4555-8555-555555555555", GlobalPolicyRevisionID: "66666666-6666-4666-8666-666666666666",
		Decision: "confirm", ReasonCodes: []string{"annotation_mutating"}, ArgumentSummary: []bridgeprotocol.ArgumentSummary{},
	}
	if _, err := harness.store.Pool().Exec(context.Background(), `DROP TABLE audit_events`); err != nil {
		t.Fatal(err)
	}

	response := harness.request(t, http.MethodPost, "/api/v1/relay/confirmations/"+challengeID+"/approve", `{"profileName":"claude-audit-failure","bindingHash":"`+bindingHash+`"}`)
	assertGovernanceError(t, response, http.StatusInternalServerError, "audit_persistence_failed")
	if decisionCalls != 1 {
		t.Fatalf("decision calls=%d want=1", decisionCalls)
	}
}

func TestGovernanceConfirmationApprovalRejectsUnpublishedProfile(t *testing.T) {
	const challengeID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const bindingHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	decisionCalls := 0
	var summary bridgeprotocol.ConfirmationSummary
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/relay/governance/confirmations/list":
			writeJSON(w, http.StatusOK, bridgeprotocol.ConfirmationListResponse{Items: []bridgeprotocol.ConfirmationSummary{summary}})
		case "/v1/relay/governance/confirmations/approve":
			decisionCalls++
			writeJSON(w, http.StatusOK, bridgeprotocol.ConfirmationDecisionResponse{ChallengeID: challengeID, BindingHash: bindingHash})
		default:
			http.NotFound(w, r)
		}
	}))
	profile, err := harness.store.SaveProfile(context.Background(), "11111111-1111-4111-8111-111111111111", store.ProfileInput{Name: "claude-unpublished", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	summary = bridgeprotocol.ConfirmationSummary{
		ChallengeID: challengeID, BindingHash: bindingHash, ArgumentHash: strings.Repeat("a", 64), CreatedAt: 1, ExpiresAt: 2,
		ProfileID: profile.ID, ProfileRevisionID: profile.CurrentRevisionID, ProfileName: profile.Name, ClientKind: profile.ClientKind,
		ServerID: "22222222-2222-4222-8222-222222222222", ServerName: "acemcp", ToolID: "33333333-3333-4333-8333-333333333333", ToolName: "search", RuntimeName: "search",
		MCPConfigRevisionID: "44444444-4444-4444-8444-444444444444", ContractRevisionID: "55555555-5555-4555-8555-555555555555", GlobalPolicyRevisionID: "66666666-6666-4666-8666-666666666666",
		Decision: "confirm", ReasonCodes: []string{"annotation_mutating"}, ArgumentSummary: []bridgeprotocol.ArgumentSummary{},
	}

	response := harness.request(t, http.MethodPost, "/api/v1/relay/confirmations/"+challengeID+"/approve", `{"profileName":"claude-unpublished","bindingHash":"`+bindingHash+`"}`)
	assertGovernanceError(t, response, http.StatusConflict, "confirmation_binding_mismatch")
	if decisionCalls != 0 {
		t.Fatalf("unpublished confirmation reached Bridge %d times", decisionCalls)
	}
}

func TestGovernanceConfirmationLookupReportsRelayUnavailable(t *testing.T) {
	const challengeID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const bindingHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"code": bridgeprotocol.ErrRelayUnhealthy, "message": "relay unavailable"}})
	}))

	response := harness.request(t, http.MethodPost, "/api/v1/relay/confirmations/"+challengeID+"/approve", `{"profileName":"claude-coding","bindingHash":"`+bindingHash+`"}`)
	assertGovernanceError(t, response, http.StatusServiceUnavailable, "relay_unavailable")
}

func TestGovernanceRejectsInvalidSafeRelayDTOs(t *testing.T) {
	stringLength := 4
	validConfirmation := bridgeprotocol.ConfirmationSummary{
		ChallengeID: strings.Repeat("c", 64), BindingHash: strings.Repeat("b", 64), ArgumentHash: strings.Repeat("a", 64), CreatedAt: 1, ExpiresAt: 2,
		ProfileID: "11111111-1111-4111-8111-111111111111", ProfileRevisionID: "22222222-2222-4222-8222-222222222222", ProfileName: "claude-coding", ClientKind: "claude",
		ServerID: "33333333-3333-4333-8333-333333333333", ServerName: "acemcp", ToolID: "44444444-4444-4444-8444-444444444444", ToolName: "search", RuntimeName: "search",
		MCPConfigRevisionID: "55555555-5555-4555-8555-555555555555", ContractRevisionID: "66666666-6666-4666-8666-666666666666", GlobalPolicyRevisionID: "77777777-7777-4777-8777-777777777777",
		Decision: "confirm", ReasonCodes: []string{"annotation_mutating"}, ArgumentSummary: []bridgeprotocol.ArgumentSummary{{Pointer: "/o0", ValueType: "string", StringLength: &stringLength}},
	}
	if !validConfirmationSummaries([]bridgeprotocol.ConfirmationSummary{validConfirmation}) {
		t.Fatal("valid confirmation summary was rejected")
	}
	invalidConfirmations := []struct {
		name   string
		mutate func(*bridgeprotocol.ConfirmationSummary)
	}{
		{name: "non finite timestamp", mutate: func(item *bridgeprotocol.ConfirmationSummary) { item.CreatedAt = math.NaN() }},
		{name: "invalid config revision", mutate: func(item *bridgeprotocol.ConfirmationSummary) { item.MCPConfigRevisionID = "invalid" }},
		{name: "overlong profile name", mutate: func(item *bridgeprotocol.ConfirmationSummary) { item.ProfileName = strings.Repeat("p", 121) }},
		{name: "invalid client", mutate: func(item *bridgeprotocol.ConfirmationSummary) { item.ClientKind = "shared" }},
		{name: "invalid decision", mutate: func(item *bridgeprotocol.ConfirmationSummary) { item.Decision = "allow" }},
		{name: "empty reason", mutate: func(item *bridgeprotocol.ConfirmationSummary) { item.ReasonCodes = []string{""} }},
		{name: "untrusted reason", mutate: func(item *bridgeprotocol.ConfirmationSummary) { item.ReasonCodes = []string{"raw-upstream-marker"} }},
		{name: "overlong pointer", mutate: func(item *bridgeprotocol.ConfirmationSummary) {
			item.ArgumentSummary = []bridgeprotocol.ArgumentSummary{{Pointer: strings.Repeat("/", 513), ValueType: "string"}}
		}},
		{name: "raw text pointer", mutate: func(item *bridgeprotocol.ConfirmationSummary) {
			item.ArgumentSummary = []bridgeprotocol.ArgumentSummary{{Pointer: "raw argument marker", ValueType: "string"}}
		}},
		{name: "raw object key pointer", mutate: func(item *bridgeprotocol.ConfirmationSummary) {
			item.ArgumentSummary = []bridgeprotocol.ArgumentSummary{{Pointer: "/secret-value-marker", ValueType: "string"}}
		}},
		{name: "non canonical ordinal pointer", mutate: func(item *bridgeprotocol.ConfirmationSummary) {
			item.ArgumentSummary = []bridgeprotocol.ArgumentSummary{{Pointer: "/o01", ValueType: "string"}}
		}},
		{name: "invalid value type", mutate: func(item *bridgeprotocol.ConfirmationSummary) {
			item.ArgumentSummary = []bridgeprotocol.ArgumentSummary{{Pointer: "/o0", ValueType: "integer"}}
		}},
		{name: "negative string length", mutate: func(item *bridgeprotocol.ConfirmationSummary) {
			invalid := -1
			item.ArgumentSummary = []bridgeprotocol.ArgumentSummary{{Pointer: "/o0", ValueType: "string", StringLength: &invalid}}
		}},
	}
	for _, test := range invalidConfirmations {
		t.Run("confirmation "+test.name, func(t *testing.T) {
			item := validConfirmation
			test.mutate(&item)
			if validConfirmationSummaries([]bridgeprotocol.ConfirmationSummary{item}) {
				t.Fatal("invalid confirmation summary was accepted")
			}
		})
	}

	validObservation := bridgeprotocol.Observation{
		BootID: "88888888-8888-4888-8888-888888888888", Sequence: 1, ObservedAt: 1, MinuteBucket: "1970-01-01T00:00:00Z",
		ProfileID: "11111111-1111-4111-8111-111111111111", ProfileRevisionID: "22222222-2222-4222-8222-222222222222",
		ServerID: "33333333-3333-4333-8333-333333333333", ToolID: "44444444-4444-4444-8444-444444444444",
		Decision: "allow", ReasonCodes: []string{"reviewed_read_only"}, Outcome: "executed", ErrorClass: "none", DurationBucket: "lt_10ms",
	}
	validDrain := bridgeprotocol.ObservationDrainResponse{BootID: validObservation.BootID, NextSequence: 1, Items: []bridgeprotocol.Observation{validObservation}}
	validDrainRequest := bridgeprotocol.ObservationDrainRequest{Limit: 1}
	if !validObservationDrain(validDrain, validDrainRequest) {
		t.Fatal("valid observation drain was rejected")
	}
	invalidObservations := []struct {
		name   string
		mutate func(*bridgeprotocol.Observation)
	}{
		{name: "invalid decision", mutate: func(item *bridgeprotocol.Observation) { item.Decision = "execute" }},
		{name: "invalid outcome", mutate: func(item *bridgeprotocol.Observation) { item.Outcome = "ok" }},
		{name: "invalid error class", mutate: func(item *bridgeprotocol.Observation) { item.ErrorClass = "rawError" }},
		{name: "invalid duration bucket", mutate: func(item *bridgeprotocol.Observation) { item.DurationBucket = "1ms" }},
		{name: "untrusted reason", mutate: func(item *bridgeprotocol.Observation) { item.ReasonCodes = []string{"raw-upstream-marker"} }},
		{name: "timestamp bucket mismatch", mutate: func(item *bridgeprotocol.Observation) { item.MinuteBucket = "1970-01-01T00:01:00Z" }},
	}
	for _, test := range invalidObservations {
		t.Run("observation "+test.name, func(t *testing.T) {
			item := validObservation
			test.mutate(&item)
			if validObservationDrain(bridgeprotocol.ObservationDrainResponse{BootID: item.BootID, NextSequence: 1, Items: []bridgeprotocol.Observation{item}}, validDrainRequest) {
				t.Fatal("invalid observation drain was accepted")
			}
		})
	}
}

func TestGovernanceProfileLaunchUsesNativeInspection(t *testing.T) {
	nativeCalls := 0
	inspection := bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true}
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/native-clients/inspect" {
			http.NotFound(w, r)
			return
		}
		nativeCalls++
		writeJSON(w, http.StatusOK, inspection)
	}))
	profile, err := harness.store.SaveProfile(context.Background(), "11111111-1111-4111-8111-111111111111", store.ProfileInput{Name: "launch-draft", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	response := harness.request(t, http.MethodGet, "/api/v1/profiles/"+profile.ID+"/launch", "")
	if response.Code != http.StatusOK || nativeCalls != 1 || !strings.Contains(response.Body.String(), `"ready":false`) || strings.Contains(response.Body.String(), `"command"`) {
		t.Fatalf("launch status=%d nativeCalls=%d body=%s", response.Code, nativeCalls, response.Body.String())
	}
	invalidInspections := []struct {
		name       string
		inspection bridgeprotocol.NativeClientInspectionResponse
	}{
		{name: "supported below floor", inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.231", Supported: true}},
		{name: "overlong version", inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: strings.Repeat("1", 65), Supported: true}},
		{name: "supported with error", inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true, ErrorCode: "native_client_timeout"}},
		{name: "unknown error", inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", ErrorCode: "secret_marker_must_not_escape"}},
	}
	for _, test := range invalidInspections {
		t.Run(test.name, func(t *testing.T) {
			inspection = test.inspection
			response := harness.request(t, http.MethodGet, "/api/v1/profiles/"+profile.ID+"/launch", "")
			assertGovernanceError(t, response, http.StatusBadGateway, "relay_response_invalid")
		})
	}
	assertGovernanceError(t, harness.request(t, http.MethodGet, "/api/v1/profiles/not-a-uuid/launch", ""), http.StatusBadRequest, "invalid_request")
}

func TestGovernanceObservabilityRoutesAreBoundedAndPayloadFree(t *testing.T) {
	bootID := "77777777-7777-4777-8777-777777777777"
	var profileID, profileRevisionID string
	serverID := "33333333-3333-4333-8333-333333333333"
	toolID := "44444444-4444-4444-8444-444444444444"
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/relay/governance/observations/drain" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, bridgeprotocol.ObservationDrainResponse{BootID: bootID, NextSequence: 1, Items: []bridgeprotocol.Observation{{
			BootID: bootID, Sequence: 1, ObservedAt: 1, MinuteBucket: "1970-01-01T00:00:00Z", ProfileID: profileID, ProfileRevisionID: profileRevisionID,
			ServerID: serverID, ToolID: toolID, Decision: "allow", ReasonCodes: []string{"reviewed_read_only"}, Outcome: "executed", ErrorClass: "none", DurationBucket: "lt_10ms",
		}}})
	}))
	profile, err := harness.store.SaveProfile(context.Background(), "11111111-1111-4111-8111-111111111111", store.ProfileInput{Name: "observability", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	profileID, profileRevisionID = profile.ID, profile.CurrentRevisionID
	if _, err := harness.store.Pool().Exec(context.Background(), `INSERT INTO mcp_daily_aggregates(day,profile_id,profile_revision_id,client_kind,decision,outcome,error_class,call_count,error_count,duration_bucket) VALUES(current_date,$1,$2,'claude','allow','executed','',3,0,'lt_10ms')`, profileID, profileRevisionID); err != nil {
		t.Fatal(err)
	}

	live := harness.request(t, http.MethodGet, "/api/v1/relay/observations/live?afterSequence=0&limit=2", "")
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), `"sequence":1`) {
		t.Fatalf("live status=%d body=%s", live.Code, live.Body.String())
	}
	daily := harness.request(t, http.MethodGet, "/api/v1/relay/observations/daily?days=1&profileId="+profileID, "")
	if daily.Code != http.StatusOK || !strings.Contains(daily.Body.String(), `"callCount":3`) {
		t.Fatalf("daily status=%d body=%s", daily.Code, daily.Body.String())
	}
	for _, body := range []string{live.Body.String(), daily.Body.String()} {
		for _, forbidden := range []string{"arguments", "results", "prompts", "rawError", "secretValue", "sessionId"} {
			if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
				t.Fatalf("observation response leaked %q: %s", forbidden, body)
			}
		}
	}

	invalidPaths := []string{
		"/api/v1/relay/observations/live?afterBootId=bad&afterSequence=0&limit=2",
		"/api/v1/relay/observations/live?afterSequence=-1&limit=2",
		"/api/v1/relay/observations/live?afterSequence=0&limit=1001",
		"/api/v1/relay/observations/daily?days=32",
		"/api/v1/relay/observations/daily?days=1&profileId=bad",
	}
	for _, path := range invalidPaths {
		assertGovernanceError(t, harness.request(t, http.MethodGet, path, ""), http.StatusBadRequest, "invalid_request")
	}
}

func TestGovernanceLiveObservationsRejectBridgeCursorViolations(t *testing.T) {
	const bootID = "77777777-7777-4777-8777-777777777777"
	observation := func(sequence int64) bridgeprotocol.Observation {
		return bridgeprotocol.Observation{
			BootID: bootID, Sequence: sequence, ObservedAt: float64(sequence), MinuteBucket: "1970-01-01T00:00:00Z",
			ProfileID: "11111111-1111-4111-8111-111111111111", ProfileRevisionID: "22222222-2222-4222-8222-222222222222",
			ServerID: "33333333-3333-4333-8333-333333333333", ToolID: "44444444-4444-4444-8444-444444444444",
			Decision: "allow", Outcome: "executed", ErrorClass: "none", DurationBucket: "lt_10ms", ReasonCodes: []string{},
		}
	}
	var response bridgeprotocol.ObservationDrainResponse
	harness := newGovernanceHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/relay/governance/observations/drain" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, response)
	}))
	tests := []struct {
		name     string
		path     string
		response bridgeprotocol.ObservationDrainResponse
	}{
		{
			name: "requested limit exceeded", path: "/api/v1/relay/observations/live?limit=1",
			response: bridgeprotocol.ObservationDrainResponse{BootID: bootID, NextSequence: 2, Items: []bridgeprotocol.Observation{observation(1), observation(2)}},
		},
		{
			name: "same boot cursor replayed", path: "/api/v1/relay/observations/live?afterBootId=" + bootID + "&afterSequence=1&limit=2",
			response: bridgeprotocol.ObservationDrainResponse{BootID: bootID, NextSequence: 1, Items: []bridgeprotocol.Observation{observation(1)}},
		},
		{
			name: "same boot next sequence moved backward", path: "/api/v1/relay/observations/live?afterBootId=" + bootID + "&afterSequence=2&limit=2",
			response: bridgeprotocol.ObservationDrainResponse{BootID: bootID, NextSequence: 1, Items: []bridgeprotocol.Observation{}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response = test.response
			assertGovernanceError(t, harness.request(t, http.MethodGet, test.path, ""), http.StatusBadGateway, "relay_response_invalid")
		})
	}
}

func assertQueuedGovernanceOperation(t *testing.T, recorder *httptest.ResponseRecorder, kind string) string {
	t.Helper()
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("%s status=%d body=%s", kind, recorder.Code, recorder.Body.String())
	}
	var operation struct {
		ID       string         `json:"id"`
		Kind     string         `json:"kind"`
		Status   string         `json:"status"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Kind != kind || operation.Status != "queued" {
		t.Fatalf("operation=%+v", operation)
	}
	encoded, err := json.Marshal(operation.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"arguments", "results", "prompts", "rawError", "secretValue", "sessionId"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatalf("operation metadata leaked %q: %s", forbidden, encoded)
		}
	}
	return operation.ID
}

type governanceHarness struct {
	router       http.Handler
	sessionToken string
	csrfToken    string
	store        *store.Store
}

func newGovernanceHarness(t *testing.T, bridgeHandlers ...http.Handler) governanceHarness {
	t.Helper()
	st := newHTTPIntegrationStore(t)
	ctx := context.Background()
	if _, err := st.BootstrapAccount(ctx, "admin", "governance-test-password"); err != nil {
		t.Fatal(err)
	}
	if err := st.BootstrapEnvironment(ctx, "governance-host", "operator", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	sessionToken, csrfToken, _, err := st.CreateSession(ctx, time.Hour, "127.0.0.1", "governance-test")
	if err != nil {
		t.Fatal(err)
	}
	api := New(st, nil, nil, nil, config.Config{SessionTTL: time.Hour}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(bridgeHandlers) > 0 {
		api.bridge = newHTTPTestBridgeClient(t, bridgeHandlers[0])
	}
	return governanceHarness{router: api.Router(), sessionToken: sessionToken, csrfToken: csrfToken, store: st}
}

func (h governanceHarness) request(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: "toolhub_session", Value: h.sessionToken})
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", h.csrfToken)
		request.Header.Set("Idempotency-Key", "governance-test-key")
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func assertGovernanceError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status=%d want=%d body=%s", recorder.Code, status, recorder.Body.String())
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"requestId"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != code || envelope.Error.Message == "" || envelope.Error.RequestID == "" {
		t.Fatalf("error envelope=%+v", envelope.Error)
	}
}
