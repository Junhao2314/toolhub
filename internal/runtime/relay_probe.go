package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

const (
	relayProbePerRequestTimeout = 30 * time.Second
	relayProbeTotalTimeout      = 90 * time.Second
	maxRelayMCPResponseBytes    = 2 << 20
)

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
	capability, err := r.adminClient().Capability(probeCtx)
	if err != nil || validateMCPMCapability(capability) != nil {
		// mcpm compatibility/pass-through mode intentionally has no ToolHub
		// governance admin socket. The fixed relay HTTP endpoint is still the
		// authoritative liveness signal in that mode; do not rewrite every
		// configured member to unavailable merely because governance is absent.
		if err != nil && r.adminUnavailable() {
			counts, probeErr := r.probeCompatibilityRelay(probeCtx, endpoint, manifest)
			if probeErr == nil {
				status.Healthy = true
				status.Contract = "compatibility"
				status.MemberStatuses = compatibilityRelayMembers(manifest, checkedAt, counts)
				return status, nil
			}
			err = probeErr
		}
		if err == nil {
			err = incompatibleMCPMError()
		}
		status.ErrorCode = relayProbeErrorCode(err, bridgeprotocol.ErrMCPMIncompatible)
		status.ErrorReason = "relay admin capability contract is incompatible"
		status.MemberStatuses = unavailableRelayMembers(manifest, status.ErrorCode, status.ErrorReason, checkedAt)
		return status, err
	}
	adminStatus, err := r.adminClient().Status(probeCtx)
	if err != nil && r.adminUnavailable() {
		counts, probeErr := r.probeCompatibilityRelay(probeCtx, endpoint, manifest)
		if probeErr == nil {
			status.Healthy = true
			status.Contract = "compatibility"
			status.MemberStatuses = compatibilityRelayMembers(manifest, checkedAt, counts)
			return status, nil
		}
		err = probeErr
	}
	if err != nil || !relayStatusMatchesManifest(adminStatus, manifest) {
		if err == nil {
			err = &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Relay admin status does not match the desired routing bundle"}
		}
		status.Contract = "incompatible"
		status.ErrorCode = relayProbeErrorCode(err, bridgeprotocol.ErrMCPMIncompatible)
		status.ErrorReason = "relay routing status is incompatible"
		status.MemberStatuses = unavailableRelayMembers(manifest, status.ErrorCode, status.ErrorReason, checkedAt)
		return status, err
	}
	observation, err := r.adminClient().ObserveContracts(probeCtx)
	if err != nil {
		if r.adminUnavailable() {
			counts, probeErr := r.probeCompatibilityRelay(probeCtx, endpoint, manifest)
			if probeErr == nil {
				status.Healthy = true
				status.Contract = "compatibility"
				status.MemberStatuses = compatibilityRelayMembers(manifest, checkedAt, counts)
				return status, nil
			}
			err = probeErr
		}
		status.ErrorCode = relayProbeErrorCode(err, bridgeprotocol.ErrRelayUnhealthy)
		status.ErrorReason = "relay contract observation failed"
		status.MemberStatuses = unavailableRelayMembers(manifest, status.ErrorCode, status.ErrorReason, checkedAt)
		return status, err
	}
	memberStatuses, err := attributeObservedRelayContracts(manifest, observation, checkedAt)
	if err != nil {
		status.Contract = "incompatible"
		status.ErrorCode = bridgeprotocol.ErrMCPMIncompatible
		status.ErrorReason = "relay contract observation did not match the desired members"
		status.MemberStatuses = unavailableRelayMembers(manifest, status.ErrorCode, status.ErrorReason, checkedAt)
		return status, err
	}
	status.Healthy = true
	status.Contract = "verified"
	status.MemberStatuses = memberStatuses
	return status, nil
}

func (r *RelayManager) adminUnavailable() bool {
	client, ok := r.adminClient().(*MCPMAdminClient)
	if !ok {
		return false
	}
	info, err := os.Lstat(client.socketPath)
	return err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0
}

type compatibilityCapabilityCounts struct {
	Tools             int
	Resources         int
	ResourceTemplates int
	Prompts           int
}

type compatibilityMCPListResult struct {
	Result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Resources []struct {
			Name string `json:"name"`
		} `json:"resources"`
		ResourceTemplates []struct {
			Name string `json:"name"`
		} `json:"resourceTemplates"`
		Prompts []struct {
			Name string `json:"name"`
		} `json:"prompts"`
	} `json:"result"`
	Error *struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	} `json:"error,omitempty"`
}

func (r *RelayManager) probeCompatibilityRelay(ctx context.Context, endpoint string, manifest bridgeprotocol.DesiredManifest) (map[string]compatibilityCapabilityCounts, error) {
	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	initBody := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "toolhub-relay-probe", "version": "1.0"},
		},
	}
	initResponse, session, err := r.postCompatibilityMCP(ctx, client, endpoint, "", initBody)
	if err != nil {
		return nil, err
	}
	var initialized struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := decodeCompatibilityMCPResponse(initResponse, &initialized); err != nil || len(initialized.Result) == 0 || len(initialized.Error) > 0 {
		return nil, errors.New("relay MCP initialize response is invalid")
	}
	defer r.closeCompatibilityMCPSession(ctx, client, endpoint, session)
	_, _, _ = r.postCompatibilityMCP(ctx, client, endpoint, session, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	body, _, err := r.postCompatibilityMCP(ctx, client, endpoint, session, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	if err != nil {
		return nil, err
	}
	var response compatibilityMCPListResult
	if err := decodeCompatibilityMCPResponse(body, &response); err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, errors.New("relay MCP capability request failed")
	}
	counts := make(map[string]compatibilityCapabilityCounts, len(manifest.MCPServers))
	for _, member := range manifest.MCPServers {
		counts[member.Name] = compatibilityCapabilityCounts{}
	}
	assign := func(name string, update func(*compatibilityCapabilityCounts)) {
		memberName := compatibilityMemberName(name, manifest)
		if memberName == "" {
			return
		}
		value := counts[memberName]
		update(&value)
		counts[memberName] = value
	}
	for _, item := range response.Result.Tools {
		assign(item.Name, func(value *compatibilityCapabilityCounts) { value.Tools++ })
	}
	for _, item := range response.Result.Resources {
		assign(item.Name, func(value *compatibilityCapabilityCounts) { value.Resources++ })
	}
	for _, item := range response.Result.ResourceTemplates {
		assign(item.Name, func(value *compatibilityCapabilityCounts) { value.ResourceTemplates++ })
	}
	for _, item := range response.Result.Prompts {
		assign(item.Name, func(value *compatibilityCapabilityCounts) { value.Prompts++ })
	}
	return counts, nil
}

func (r *RelayManager) postCompatibilityMCP(ctx context.Context, client *http.Client, endpoint, session string, payload map[string]any) (string, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if session != "" {
		request.Header.Set("Mcp-Session-Id", session)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", "", &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "relay MCP capability request failed", Retryable: true}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxRelayMCPResponseBytes+1)
	responseBody, readErr := io.ReadAll(limited)
	if readErr != nil || len(responseBody) > maxRelayMCPResponseBytes {
		return "", "", errors.New("relay MCP capability response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "relay MCP capability request was rejected", Retryable: true}
	}
	return string(responseBody), response.Header.Get("Mcp-Session-Id"), nil
}

func (r *RelayManager) closeCompatibilityMCPSession(ctx context.Context, client *http.Client, endpoint, session string) {
	if session == "" {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return
	}
	request.Header.Set("Mcp-Session-Id", session)
	response, err := client.Do(request)
	if err == nil && response != nil {
		_ = response.Body.Close()
	}
}

func decodeCompatibilityMCPResponse(body string, output any) error {
	var data []byte
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			candidate := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if candidate != "" {
				data = []byte(candidate)
				break
			}
		}
	}
	if len(data) == 0 {
		data = []byte(strings.TrimSpace(body))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(output); err != nil {
		return errors.New("relay MCP capability response is invalid")
	}
	return nil
}

func compatibilityMemberName(runtimeName string, manifest bridgeprotocol.DesiredManifest) string {
	if len(manifest.MCPServers) == 1 {
		return manifest.MCPServers[0].Name
	}
	best := ""
	for _, member := range manifest.MCPServers {
		if strings.HasPrefix(runtimeName, member.Name+"_") && len(member.Name) > len(best) {
			best = member.Name
		}
	}
	return best
}

func compatibilityRelayMembers(manifest bridgeprotocol.DesiredManifest, checkedAt time.Time, counts map[string]compatibilityCapabilityCounts) []bridgeprotocol.RelayMemberStatus {
	result := make([]bridgeprotocol.RelayMemberStatus, 0, len(manifest.MCPServers))
	for _, server := range manifest.MCPServers {
		capabilities := counts[server.Name]
		kinds := make([]string, 0, 4)
		if capabilities.Tools > 0 {
			kinds = append(kinds, "tools")
		}
		if capabilities.Resources > 0 {
			kinds = append(kinds, "resources")
		}
		if capabilities.ResourceTemplates > 0 {
			kinds = append(kinds, "resourceTemplates")
		}
		if capabilities.Prompts > 0 {
			kinds = append(kinds, "prompts")
		}
		result = append(result, bridgeprotocol.RelayMemberStatus{
			MemberID: server.MemberID, Name: server.Name, Status: "ready",
			CapabilityKinds: kinds, Capabilities: bridgeprotocol.RelayCapabilityCounts{Tools: capabilities.Tools, Resources: capabilities.Resources, ResourceTemplates: capabilities.ResourceTemplates, Prompts: capabilities.Prompts}, CheckedAt: checkedAt,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func relayStatusMatchesManifest(status bridgeprotocol.RelayAdminStatus, manifest bridgeprotocol.DesiredManifest) bool {
	if manifest.SchemaVersion < bridgeprotocol.ManifestSchemaVersionV2 || manifest.RelayGovernance == nil {
		return true
	}
	return relayAdminStatusMatches(status, *manifest.RelayGovernance)
}

func attributeObservedRelayContracts(manifest bridgeprotocol.DesiredManifest, observation bridgeprotocol.ContractObservationResponse, checkedAt time.Time) ([]bridgeprotocol.RelayMemberStatus, error) {
	expectedConfig := map[string]string{}
	if manifest.SchemaVersion >= bridgeprotocol.ManifestSchemaVersionV2 && manifest.RelayGovernance != nil {
		var bundle bridgeprotocol.RoutingBundle
		if err := bridgeprotocol.DecodeGovernanceBody(manifest.RelayGovernance.RoutingBundle, &bundle); err != nil {
			return nil, err
		}
		if observation.RelayConfigurationRevisionID != bundle.RelayConfigurationRevisionID {
			return nil, errors.New("contract observation relay revision does not match")
		}
		for _, server := range bundle.Servers {
			expectedConfig[server.ServerID] = server.MCPConfigRevisionID
		}
	}
	members := make(map[string]bridgeprotocol.MCPMember, len(manifest.MCPServers))
	for _, member := range manifest.MCPServers {
		members[member.ServerID] = member
	}
	if len(observation.Servers) != len(members) {
		return nil, errors.New("contract observation server count does not match")
	}
	result := make([]bridgeprotocol.RelayMemberStatus, 0, len(members))
	seen := map[string]struct{}{}
	for _, observed := range observation.Servers {
		member, ok := members[observed.ServerID]
		if !ok || observed.ServerName != member.Name {
			return nil, errors.New("contract observation contains an unknown server")
		}
		if _, duplicate := seen[observed.ServerID]; duplicate {
			return nil, errors.New("contract observation contains a duplicate server")
		}
		seen[observed.ServerID] = struct{}{}
		if expected, ok := expectedConfig[observed.ServerID]; ok && observed.MCPConfigRevisionID != expected {
			return nil, errors.New("contract observation MCP config revision does not match")
		}
		if len(observed.Tools) > 500 {
			return nil, errors.New("contract observation exceeds the per-server tool limit")
		}
		capabilityKinds := []string{}
		if len(observed.Tools) > 0 {
			capabilityKinds = append(capabilityKinds, "tools")
		}
		result = append(result, bridgeprotocol.RelayMemberStatus{MemberID: member.MemberID, Name: member.Name, Status: "ready", CapabilityKinds: capabilityKinds, Capabilities: bridgeprotocol.RelayCapabilityCounts{Tools: len(observed.Tools)}, CheckedAt: checkedAt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (r *RelayManager) relayNow() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
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
