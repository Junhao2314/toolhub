package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func TestRelayFullProbeUsesAdminContractObservationWithoutMCPToolCalls(t *testing.T) {
	manager := probeManager(t)
	manifest := probeManifestV2(t, "server")
	admin := &fakeMCPMAdmin{autoManifest: true}
	admin.configureManifest(t, manifest)
	admin.observation.Servers[0].Tools = []bridgeprotocol.ContractToolDTO{{
		Name: "search", RuntimeName: "search", InputSchema: map[string]any{"type": "object"}, Annotations: map[string]any{},
	}}
	manager.Admin = admin
	httpCalls := 0
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		httpCalls++
		return nil, errors.New("full probe must use the admin socket")
	})}

	status, err := manager.ProbeMembers(context.Background(), 6276, manifest)
	if err != nil || !status.Healthy || status.Contract != "verified" || len(status.MemberStatuses) != 1 || status.MemberStatuses[0].Capabilities.Tools != 1 {
		t.Fatalf("admin full probe status=%+v err=%v", status, err)
	}
	if httpCalls != 0 {
		t.Fatalf("full probe sent %d synthetic MCP calls", httpCalls)
	}
}

func TestRelayObservedContractsRequireExactDesiredMembersAndRevisions(t *testing.T) {
	manifest := probeManifestV2(t, "alpha", "beta")
	admin := &fakeMCPMAdmin{autoManifest: true}
	admin.configureManifest(t, manifest)
	valid := admin.observation
	checkedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		mutate func(*bridgeprotocol.ContractObservationResponse)
	}{
		{name: "missing server", mutate: func(value *bridgeprotocol.ContractObservationResponse) { value.Servers = value.Servers[:1] }},
		{name: "extra server", mutate: func(value *bridgeprotocol.ContractObservationResponse) {
			value.Servers = append(value.Servers, value.Servers[0])
		}},
		{name: "duplicate server", mutate: func(value *bridgeprotocol.ContractObservationResponse) { value.Servers[1] = value.Servers[0] }},
		{name: "wrong relay revision", mutate: func(value *bridgeprotocol.ContractObservationResponse) {
			value.RelayConfigurationRevisionID = uuid.NewString()
		}},
		{name: "wrong config revision", mutate: func(value *bridgeprotocol.ContractObservationResponse) {
			value.Servers[0].MCPConfigRevisionID = uuid.NewString()
		}},
		{name: "wrong server name", mutate: func(value *bridgeprotocol.ContractObservationResponse) { value.Servers[0].ServerName = "renamed" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Servers = append([]bridgeprotocol.ContractServerObservation(nil), valid.Servers...)
			test.mutate(&candidate)
			if statuses, err := attributeObservedRelayContracts(manifest, candidate, checkedAt); err == nil {
				t.Fatalf("mismatched observation was accepted: %+v", statuses)
			}
		})
	}
}

func TestRelayObservedServerWithNoToolsIsReady(t *testing.T) {
	manifest := probeManifestV2(t, "empty")
	admin := &fakeMCPMAdmin{autoManifest: true}
	admin.configureManifest(t, manifest)
	statuses, err := attributeObservedRelayContracts(manifest, admin.observation, time.Unix(1, 0).UTC())
	if err != nil || len(statuses) != 1 || statuses[0].Status != "ready" || len(statuses[0].CapabilityKinds) != 0 || statuses[0].Capabilities.Tools != 0 {
		t.Fatalf("zero-tool server status=%+v err=%v", statuses, err)
	}
}

func TestRelayProbeFailsClosedForAdminContractFailures(t *testing.T) {
	manifest := probeManifestV2(t, "server")
	tests := []struct {
		name   string
		mutate func(*fakeMCPMAdmin)
		code   string
	}{
		{name: "capability transport", mutate: func(admin *fakeMCPMAdmin) { admin.capabilityErr = context.DeadlineExceeded }, code: bridgeprotocol.ErrRelayUnhealthy},
		{name: "capability incompatible", mutate: func(admin *fakeMCPMAdmin) {
			admin.capability = &bridgeprotocol.RelayCapabilityResponse{AdminProtocolVersion: 2, Runtime: "mcpm"}
		}, code: bridgeprotocol.ErrMCPMIncompatible},
		{name: "status mismatch", mutate: func(admin *fakeMCPMAdmin) { admin.status.RoutingBundleHash = strings.Repeat("f", 64) }, code: bridgeprotocol.ErrRevisionConflict},
		{name: "observation unavailable", mutate: func(admin *fakeMCPMAdmin) { admin.observationErr = errors.New("upstream marker must not escape") }, code: bridgeprotocol.ErrRelayUnhealthy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := probeManager(t)
			admin := &fakeMCPMAdmin{autoManifest: true}
			admin.configureManifest(t, manifest)
			test.mutate(admin)
			manager.Admin = admin
			status, err := manager.ProbeMembers(context.Background(), 6276, manifest)
			if err == nil || status.Healthy || status.ErrorCode != test.code {
				t.Fatalf("admin failure status=%+v err=%v", status, err)
			}
			if strings.Contains(status.ErrorReason, "marker") {
				t.Fatalf("admin error leaked into status reason %q", status.ErrorReason)
			}
		})
	}
}

func TestRelayProbeReasonIsBoundedAndSingleLine(t *testing.T) {
	reason := safeRelayReason(errors.New(strings.Repeat("x", 230) + "\nsecret-line"))
	if len(reason) != 200 || strings.Contains(reason, "\n") || strings.Contains(reason, "secret-line") {
		t.Fatalf("unsafe relay reason %q", reason)
	}
}

func TestRelayCompatibilityProbeReportsAggregatedCapabilities(t *testing.T) {
	manager := probeManager(t)
	manager.Admin = newMCPMAdminClient(filepath.Join(t.TempDir(), "missing.sock"), time.Second)
	manifest := probeManifest("alpha", "beta")
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodDelete {
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		}
		body := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-06-18\"}}\n\n"
		session := "compatibility-session"
		if request.Header.Get("Mcp-Session-Id") != "" && strings.Contains(string(mustReadBody(t, request)), "tools/list") {
			body = "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"tools\":[{\"name\":\"alpha_lookup\",\"description\":\"x\"},{\"name\":\"alpha_write\",\"inputSchema\":{\"type\":\"object\"}},{\"name\":\"beta_search\"}],\"resources\":[{\"name\":\"beta_doc\"}],\"resourceTemplates\":[],\"prompts\":[{\"name\":\"alpha_prompt\"}]}}\n\n"
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Mcp-Session-Id": []string{session}}}, nil
	})}
	status, err := manager.ProbeMembers(context.Background(), 6276, manifest)
	if err != nil || !status.Healthy || status.Contract != "compatibility" {
		t.Fatalf("compatibility status=%+v err=%v", status, err)
	}
	if len(status.MemberStatuses) != 2 || status.MemberStatuses[0].Capabilities.Tools != 2 || status.MemberStatuses[0].Capabilities.Prompts != 1 || status.MemberStatuses[1].Capabilities.Tools != 1 || status.MemberStatuses[1].Capabilities.Resources != 1 {
		t.Fatalf("member capabilities=%+v", status.MemberStatuses)
	}
}

func mustReadBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

func probeManager(t *testing.T) *RelayManager {
	t.Helper()
	mcpm := filepath.Join(t.TempDir(), "mcpm")
	writeCompatibleMCPM(t, mcpm, "1.2.3-toolhub.1")
	manager := NewRelayManager(&fakeRelayController{state: "active", enabled: true}, t.TempDir())
	manager.MCPMPath = mcpm
	manager.now = func() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) }
	return manager
}

func probeManifest(names ...string) bridgeprotocol.DesiredManifest {
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeSharedRelay, ManagedUsername: "root"}
	manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: target, Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{}, RelayPort: 6276}
	for _, name := range names {
		memberID := uuid.NewString()
		manifest.MCPServers = append(manifest.MCPServers, bridgeprotocol.MCPMember{MemberID: memberID, ServerID: uuid.NewString(), Revision: 1, Name: name, Transport: "http", URL: "https://example.invalid/mcp", ContentHash: strings.Repeat("a", 64)})
		manifest.ManagedMemberIDs = append(manifest.ManagedMemberIDs, memberID)
	}
	return manifest
}

func probeManifestV2(t *testing.T, names ...string) bridgeprotocol.DesiredManifest {
	t.Helper()
	return withRelayGovernance(t, probeManifest(names...), "00000000-0000-0000-0000-000000000001", strings.Repeat("a", 64), "00000000-0000-0000-0000-000000000002", strings.Repeat("b", 64))
}
