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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func TestRelayProbeDiscoversJSONSSEPaginationAndSessionCapabilities(t *testing.T) {
	manager := probeManager(t)
	manifest := probeManifest("alpha", "alpha_extra", "resource", "prompt")
	seenSession := 0
	seenSecondPage := false
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			return nil, err
		}
		if input.Method != "initialize" {
			if request.Header.Get("Mcp-Session-Id") != "probe-session" {
				t.Fatalf("method %s omitted session header", input.Method)
			}
			seenSession++
		}
		if input.Method == "notifications/initialized" {
			return probeHTTPResponse(http.StatusAccepted, "application/json", "", nil), nil
		}
		result := `{}`
		contentType := "application/json"
		headers := make(http.Header)
		switch input.Method {
		case "initialize":
			result = `{"protocolVersion":"2024-11-05","capabilities":{"tools":{},"resources":{},"prompts":{}}}`
			contentType = "text/event-stream"
			headers.Set("Mcp-Session-Id", "probe-session")
		case "tools/list":
			if input.Params["cursor"] == "tools-2" {
				seenSecondPage = true
				result = `{"tools":[{"name":"alpha_status"}]}`
			} else {
				result = `{"tools":[{"name":"alpha_extra_status"}],"nextCursor":"tools-2"}`
			}
		case "resources/list":
			result = `{"resources":[{"name":"legacy","uri":"mcp://resource/info"}]}`
		case "resources/templates/list":
			result = `{"resourceTemplates":[{"name":"path","uriTemplate":"/resource/{id}"}]}`
			contentType = "text/event-stream"
		case "prompts/list":
			result = `{"prompts":[{"name":"prompt_summary"}]}`
		}
		body := `{"jsonrpc":"2.0","id":"` + input.Method + `","result":` + result + `}`
		if contentType == "text/event-stream" {
			body = "event: ping\ndata: not-json\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\nevent: message\ndata: " + body + "\n\n"
		}
		return probeHTTPResponse(http.StatusOK, contentType, body, headers), nil
	})}

	status, err := manager.ProbeMembers(context.Background(), 6276, manifest)
	if err != nil || !status.Healthy || status.Contract != "verified" || !seenSecondPage || seenSession != 6 {
		t.Fatalf("probe status=%+v err=%v secondPage=%v sessionRequests=%d", status, err, seenSecondPage, seenSession)
	}
	if status.Version != "1.2.3" {
		t.Fatalf("probe exposed non-canonical version %q", status.Version)
	}
	byName := map[string]bridgeprotocol.RelayMemberStatus{}
	for _, member := range status.MemberStatuses {
		byName[member.Name] = member
	}
	if byName["alpha"].Capabilities.Tools != 1 || byName["alpha_extra"].Capabilities.Tools != 1 {
		t.Fatalf("longest namespace attribution failed: %+v", byName)
	}
	if byName["resource"].Capabilities.Resources != 1 || byName["resource"].Capabilities.ResourceTemplates != 1 {
		t.Fatalf("resource attribution failed: %+v", byName["resource"])
	}
	if byName["prompt"].Capabilities.Prompts != 1 || len(byName["prompt"].CapabilityKinds) != 1 {
		t.Fatalf("prompt attribution failed: %+v", byName["prompt"])
	}
}

func TestRelayProbeFailsClosedForPartialMemberProjection(t *testing.T) {
	manager := probeManager(t)
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		if input.Method == "notifications/initialized" {
			return probeHTTPResponse(http.StatusAccepted, "application/json", "", nil), nil
		}
		result := `{"tools":[{"name":"ready_status"}]}`
		if input.Method == "initialize" {
			result = `{"protocolVersion":"2024-11-05","capabilities":{"tools":{}}}`
		}
		return probeHTTPResponse(http.StatusOK, "application/json", `{"jsonrpc":"2.0","id":"`+input.Method+`","result":`+result+`}`, nil), nil
	})}
	status, err := manager.ProbeMembers(context.Background(), 6276, probeManifest("ready", "missing"))
	if err != nil || status.Healthy || status.Contract != "incompatible" || status.ErrorCode != bridgeprotocol.ErrMCPMIncompatible {
		t.Fatalf("partial status=%+v err=%v", status, err)
	}
	if status.MemberStatuses[0].Status != "unavailable" && status.MemberStatuses[1].Status != "unavailable" {
		t.Fatalf("partial member projection omitted unavailable status: %+v", status.MemberStatuses)
	}
}

func TestRelayProbeFailsClosedWhenAdvertisedDiscoveryFails(t *testing.T) {
	manager := probeManager(t)
	resourceAttempts := 0
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		if input.Method == "notifications/initialized" {
			return probeHTTPResponse(http.StatusAccepted, "application/json", "", nil), nil
		}
		if input.Method == "initialize" {
			return probeHTTPResponse(http.StatusOK, "application/json", `{"jsonrpc":"2.0","id":"initialize","result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{},"resources":{}}}}`, nil), nil
		}
		if input.Method == "resources/list" {
			resourceAttempts++
			return probeHTTPResponse(http.StatusInternalServerError, "application/json", `{}`, nil), nil
		}
		result := `{"tools":[{"name":"ready_status"}]}`
		if input.Method == "resources/templates/list" {
			result = `{"resourceTemplates":[]}`
		}
		return probeHTTPResponse(http.StatusOK, "application/json", `{"jsonrpc":"2.0","id":"`+input.Method+`","result":`+result+`}`, nil), nil
	})}
	status, err := manager.ProbeMembers(context.Background(), 6276, probeManifest("ready"))
	if err != nil || status.Healthy || status.Contract != "incompatible" || status.ErrorCode != bridgeprotocol.ErrMCPMIncompatible {
		t.Fatalf("discovery failure status=%+v err=%v", status, err)
	}
	if resourceAttempts != 2 {
		t.Fatalf("permanent discovery failure attempts=%d", resourceAttempts)
	}
}

func TestRelayProbeRetriesColdDiscoveryWithinTotalBudget(t *testing.T) {
	manager := probeManager(t)
	toolAttempts := 0
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		if input.Method == "notifications/initialized" {
			return probeHTTPResponse(http.StatusAccepted, "application/json", "", nil), nil
		}
		if input.Method == "initialize" {
			return probeHTTPResponse(http.StatusOK, "application/json", `{"jsonrpc":"2.0","id":"initialize","result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}}}}`, nil), nil
		}
		toolAttempts++
		if toolAttempts == 1 {
			return nil, context.DeadlineExceeded
		}
		return probeHTTPResponse(http.StatusOK, "application/json", `{"jsonrpc":"2.0","id":"tools/list","result":{"tools":[{"name":"cold_status"}]}}`, nil), nil
	})}

	status, err := manager.ProbeMembers(context.Background(), 6276, probeManifest("cold"))
	if err != nil || !status.Healthy || status.Contract != "verified" || toolAttempts != 2 {
		t.Fatalf("cold discovery status=%+v err=%v attempts=%d", status, err, toolAttempts)
	}
}

func TestRelayResourcePrefixesSupportPathAndLegacyProtocolStyles(t *testing.T) {
	manifest := probeManifest("path", "legacy", "mcpstyle")
	statuses := attributeRelayCapabilities(manifest, []mcpCapability{
		{Kind: "resource", URI: "/path/item"},
		{Kind: "resource", URI: "legacy://item"},
		{Kind: "resource_template", URI: "mcp://mcpstyle/{id}"},
	}, time.Unix(1, 0).UTC())
	for _, status := range statuses {
		if status.Status != "ready" {
			t.Fatalf("resource prefix status=%+v", statuses)
		}
	}
}

func TestRelayProbeTimeoutAndReasonRedaction(t *testing.T) {
	manager := probeManager(t)
	manager.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	// Leave enough budget for the race-instrumented mcpm --version subprocess
	// before exercising the transport timeout. A 5ms deadline can expire during
	// process startup and report mcpm_incompatible instead of relay_unhealthy.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	status, err := manager.ProbeMembers(ctx, 6276, probeManifest("server"))
	if err == nil || status.ErrorCode != bridgeprotocol.ErrRelayUnhealthy || status.Healthy {
		t.Fatalf("timeout status=%+v err=%v", status, err)
	}
	reason := safeRelayReason(errors.New(strings.Repeat("x", 230) + "\nsecret-line"))
	if len(reason) != 200 || strings.Contains(reason, "\n") || strings.Contains(reason, "secret-line") {
		t.Fatalf("unsafe relay reason %q", reason)
	}
}

func TestRelaySSEProbeReturnsBeforeStreamCloses(t *testing.T) {
	reader, writer := io.Pipe()
	done := make(chan struct{})
	release := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.WriteString(writer, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"tools/list\",\"result\":{\"tools\":[]}}\n\n")
		<-release
		_ = writer.Close()
	}()
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}
	started := time.Now()
	payload, err := readMCPResponse(response, "tools/list")
	_ = reader.Close()
	close(release)
	if err != nil || !bytes.Contains(payload, []byte(`"id":"tools/list"`)) || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("streaming SSE payload=%s err=%v elapsed=%s", payload, err, time.Since(started))
	}
	<-done
}

func probeManager(t *testing.T) *RelayManager {
	t.Helper()
	mcpm := filepath.Join(t.TempDir(), "mcpm")
	if err := os.WriteFile(mcpm, []byte("#!/bin/sh\nprintf 'mcpm 1.2.3\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
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

func probeHTTPResponse(status int, contentType, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	if contentType != "" {
		headers.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}
