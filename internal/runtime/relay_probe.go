package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

const (
	relayProbePerRequestTimeout = 30 * time.Second
	relayProbeTotalTimeout      = 90 * time.Second
)

type mcpCapability struct {
	Kind string
	Name string
	URI  string
}

type relayProbeState struct {
	sessionID    string
	capabilities []mcpCapability
}

type relayDiscoveryMethod struct {
	capability string
	method     string
	key        string
	kind       string
}

type mcpInitializeResult struct {
	ProtocolVersion string                     `json:"protocolVersion"`
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
}

func (r *RelayManager) ProbeMembers(ctx context.Context, port int, manifest bridgeprotocol.DesiredManifest) (bridgeprotocol.RelayStatus, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	checkedAt := r.relayNow().UTC()
	status := bridgeprotocol.RelayStatus{
		State:          "active",
		Endpoint:       endpoint,
		FixedPort:      port,
		SystemdEnabled: true,
		Contract:       "unavailable",
	}
	probeCtx, cancel := context.WithTimeout(ctx, relayProbeTotalTimeout)
	defer cancel()
	version, err := r.ValidateMCPM(probeCtx)
	if err != nil {
		status.ErrorCode = relayProbeErrorCode(err, bridgeprotocol.ErrMCPMIncompatible)
		status.ErrorReason = relayProbeSafeMessage(err, "mcpm runtime contract could not be verified")
		status.MemberStatuses = unavailableRelayMembers(manifest, status.ErrorCode, status.ErrorReason, checkedAt)
		return status, err
	}
	status.Version = version
	state := &relayProbeState{}
	var initialized mcpInitializeResult
	if err := r.relayRPC(probeCtx, endpoint, state, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "toolhub", "version": "1"},
	}, &initialized); err != nil {
		status.ErrorCode = relayProbeErrorCode(err, bridgeprotocol.ErrMCPMIncompatible)
		status.ErrorReason = "relay MCP initialize failed"
		status.MemberStatuses = unavailableRelayMembers(manifest, status.ErrorCode, status.ErrorReason, checkedAt)
		return status, err
	}
	if initialized.ProtocolVersion == "" || initialized.Capabilities == nil {
		err := errors.New("relay initialize response omitted its protocol contract")
		status.Contract = "incompatible"
		status.ErrorCode = bridgeprotocol.ErrMCPMIncompatible
		status.ErrorReason = "relay MCP initialize contract is incomplete"
		status.MemberStatuses = unavailableRelayMembers(manifest, status.ErrorCode, status.ErrorReason, checkedAt)
		return status, err
	}
	if err := r.relayNotify(probeCtx, endpoint, state, "notifications/initialized", map[string]any{}); err != nil {
		status.Contract = "incompatible"
		status.ErrorCode = relayProbeErrorCode(err, bridgeprotocol.ErrMCPMIncompatible)
		status.ErrorReason = "relay MCP initialization could not be completed"
		status.MemberStatuses = unavailableRelayMembers(manifest, status.ErrorCode, status.ErrorReason, checkedAt)
		return status, err
	}
	methods := []relayDiscoveryMethod{
		{"tools", "tools/list", "tools", "tool"},
		{"resources", "resources/list", "resources", "resource"},
		{"resources", "resources/templates/list", "resourceTemplates", "resource_template"},
		{"prompts", "prompts/list", "prompts", "prompt"},
	}
	pending := make([]relayDiscoveryMethod, 0, len(methods))
	for _, method := range methods {
		if _, advertised := initialized.Capabilities[method.capability]; advertised {
			pending = append(pending, method)
		}
	}
	for attempt := 0; attempt < 2 && len(pending) > 0; attempt++ {
		failed := make([]relayDiscoveryMethod, 0, len(pending))
		for _, method := range pending {
			capabilities, err := r.discoverRelayCapabilities(probeCtx, endpoint, state, method)
			if err != nil {
				failed = append(failed, method)
				continue
			}
			state.capabilities = append(state.capabilities, capabilities...)
		}
		pending = failed
	}
	discoveryFailed := len(pending) > 0
	status.MemberStatuses = attributeRelayCapabilities(manifest, state.capabilities, checkedAt)
	status.Healthy = true
	for _, member := range status.MemberStatuses {
		if member.Status != "ready" {
			status.Healthy = false
			status.Contract = "incompatible"
			status.ErrorCode = bridgeprotocol.ErrMCPMIncompatible
			status.ErrorReason = "relay namespace contract did not expose a capability for every desired MCP member"
			break
		}
	}
	if status.Healthy && discoveryFailed && len(manifest.MCPServers) > 0 {
		status.Healthy = false
		status.Contract = "incompatible"
		status.ErrorCode = bridgeprotocol.ErrMCPMIncompatible
		status.ErrorReason = "relay capability discovery did not complete"
	}
	if status.Healthy {
		status.Contract = "verified"
	}
	return status, nil
}

func (r *RelayManager) discoverRelayCapabilities(ctx context.Context, endpoint string, state *relayProbeState, method relayDiscoveryMethod) ([]mcpCapability, error) {
	capabilities := []mcpCapability{}
	cursor := ""
	seenCursors := map[string]bool{}
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result map[string]any
		if err := r.relayRPC(ctx, endpoint, state, method.method, params, &result); err != nil {
			return nil, err
		}
		items, ok := result[method.key].([]any)
		if !ok {
			return nil, errors.New("relay capability list contract is invalid")
		}
		for _, item := range items {
			object, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _ := object["name"].(string)
			uri, _ := object["uri"].(string)
			if uri == "" {
				uri, _ = object["uriTemplate"].(string)
			}
			if name != "" || uri != "" {
				capabilities = append(capabilities, mcpCapability{Kind: method.kind, Name: name, URI: uri})
			}
		}
		next, _ := result["nextCursor"].(string)
		if next == "" {
			return capabilities, nil
		}
		if next == cursor || seenCursors[next] {
			return nil, errors.New("relay capability pagination cursor repeated")
		}
		seenCursors[next] = true
		cursor = next
	}
}

func (r *RelayManager) relayNow() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
}

func (r *RelayManager) relayRPC(ctx context.Context, endpoint string, state *relayProbeState, method string, params any, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": method, "method": method, "params": params})
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, relayProbePerRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2024-11-05")
	if state.sessionID != "" {
		request.Header.Set("Mcp-Session-Id", state.sessionID)
	}
	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if state.sessionID == "" {
		state.sessionID = response.Header.Get("Mcp-Session-Id")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("relay returned HTTP %d", response.StatusCode)
	}
	payload, err := readMCPResponse(response, method)
	if err != nil {
		return err
	}
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if envelope.Error != nil {
		return errors.New("relay JSON-RPC method failed")
	}
	if !jsonRPCIDMatches(envelope.ID, method) {
		return errors.New("relay JSON-RPC response id did not match the request")
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return err
		}
	}
	return nil
}

func (r *RelayManager) relayNotify(ctx context.Context, endpoint string, state *relayProbeState, method string, params any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, relayProbePerRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2024-11-05")
	if state.sessionID != "" {
		request.Header.Set("Mcp-Session-Id", state.sessionID)
	}
	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("relay returned HTTP %d", response.StatusCode)
	}
	return nil
}

func readMCPResponse(response *http.Response, requestID string) ([]byte, error) {
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return firstSSEData(response.Body, requestID)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 4<<20 {
		return nil, errors.New("relay MCP response exceeded the size limit")
	}
	return body, nil
}

func firstSSEData(body io.Reader, requestID string) ([]byte, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var lines []string
	totalBytes := 0
	flush := func() []byte {
		if len(lines) == 0 {
			return nil
		}
		candidate := []byte(strings.Join(lines, "\n"))
		lines = nil
		if json.Valid(candidate) && jsonRPCPayloadMatches(candidate, requestID) {
			return candidate
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		totalBytes += len(line) + 1
		if totalBytes > 4<<20 {
			return nil, errors.New("relay MCP response exceeded the size limit")
		}
		if strings.TrimSpace(line) == "" {
			if candidate := flush(); candidate != nil {
				return candidate, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if candidate := flush(); candidate != nil {
		return candidate, nil
	}
	return nil, errors.New("relay SSE response did not contain the requested JSON-RPC response")
}

func jsonRPCPayloadMatches(payload []byte, requestID string) bool {
	var envelope struct {
		ID json.RawMessage `json:"id"`
	}
	return json.Unmarshal(payload, &envelope) == nil && jsonRPCIDMatches(envelope.ID, requestID)
}

func jsonRPCIDMatches(raw json.RawMessage, requestID string) bool {
	var value string
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value == requestID
}

func attributeRelayCapabilities(manifest bridgeprotocol.DesiredManifest, capabilities []mcpCapability, checkedAt time.Time) []bridgeprotocol.RelayMemberStatus {
	statuses := make([]bridgeprotocol.RelayMemberStatus, 0, len(manifest.MCPServers))
	for _, server := range manifest.MCPServers {
		statuses = append(statuses, bridgeprotocol.RelayMemberStatus{MemberID: server.MemberID, Name: server.Name, Status: "unavailable", CapabilityKinds: []string{}, CheckedAt: checkedAt, ErrorCode: bridgeprotocol.ErrMCPMIncompatible, ErrorReason: "no attributed capability was discovered"})
	}
	sort.Slice(statuses, func(i, j int) bool { return len(statuses[i].Name) > len(statuses[j].Name) })
	for _, capability := range capabilities {
		index := attributedRelayMember(statuses, capability)
		if index < 0 {
			continue
		}
		switch capability.Kind {
		case "tool":
			statuses[index].Capabilities.Tools++
		case "resource":
			statuses[index].Capabilities.Resources++
		case "resource_template":
			statuses[index].Capabilities.ResourceTemplates++
		case "prompt":
			statuses[index].Capabilities.Prompts++
		}
	}
	for index := range statuses {
		counts := statuses[index].Capabilities
		if counts.Tools+counts.Resources+counts.ResourceTemplates+counts.Prompts > 0 {
			statuses[index].Status = "ready"
			statuses[index].ErrorCode = ""
			statuses[index].ErrorReason = ""
			if counts.Tools > 0 {
				statuses[index].CapabilityKinds = append(statuses[index].CapabilityKinds, "tools")
			}
			if counts.Resources > 0 {
				statuses[index].CapabilityKinds = append(statuses[index].CapabilityKinds, "resources")
			}
			if counts.ResourceTemplates > 0 {
				statuses[index].CapabilityKinds = append(statuses[index].CapabilityKinds, "resourceTemplates")
			}
			if counts.Prompts > 0 {
				statuses[index].CapabilityKinds = append(statuses[index].CapabilityKinds, "prompts")
			}
		}
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses
}

func attributedRelayMember(statuses []bridgeprotocol.RelayMemberStatus, capability mcpCapability) int {
	for index, member := range statuses {
		switch capability.Kind {
		case "tool", "prompt":
			if strings.HasPrefix(capability.Name, member.Name+"_") {
				return index
			}
		case "resource", "resource_template":
			if relayResourceBelongsToMember(capability.Name, capability.URI, member.Name) {
				return index
			}
		}
	}
	return -1
}

func relayResourceBelongsToMember(name, uri, member string) bool {
	for _, value := range []string{name, uri} {
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "/"+member+"/") ||
			strings.HasPrefix(value, member+"/") ||
			strings.HasPrefix(value, member+"://") ||
			strings.HasPrefix(value, "mcp://"+member+"/") {
			return true
		}
	}
	return false
}

func unavailableRelayMembers(manifest bridgeprotocol.DesiredManifest, code, reason string, checkedAt time.Time) []bridgeprotocol.RelayMemberStatus {
	result := make([]bridgeprotocol.RelayMemberStatus, 0, len(manifest.MCPServers))
	for _, server := range manifest.MCPServers {
		result = append(result, bridgeprotocol.RelayMemberStatus{MemberID: server.MemberID, Name: server.Name, Status: "unavailable", CapabilityKinds: []string{}, CheckedAt: checkedAt, ErrorCode: code, ErrorReason: reason})
	}
	return result
}

func relayProbeErrorCode(err error, fallback string) string {
	var apiErr *bridgeprotocol.APIError
	if errors.As(err, &apiErr) && apiErr.Code != "" {
		return apiErr.Code
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return bridgeprotocol.ErrRelayUnhealthy
	}
	return fallback
}

func relayProbeSafeMessage(err error, fallback string) string {
	var apiErr *bridgeprotocol.APIError
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return safeRelayReason(errors.New(apiErr.Message))
	}
	return fallback
}

func safeRelayReason(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' {
			return ' '
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
	runes := []rune(value)
	if len(runes) > 200 {
		value = string(runes[:200])
	}
	return value
}
