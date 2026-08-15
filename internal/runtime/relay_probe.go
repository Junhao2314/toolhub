package runtime

import (
	"context"
	"errors"
	"fmt"
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
		if err == nil {
			err = incompatibleMCPMError()
		}
		status.ErrorCode = relayProbeErrorCode(err, bridgeprotocol.ErrMCPMIncompatible)
		status.ErrorReason = "relay admin capability contract is incompatible"
		status.MemberStatuses = unavailableRelayMembers(manifest, status.ErrorCode, status.ErrorReason, checkedAt)
		return status, err
	}
	adminStatus, err := r.adminClient().Status(probeCtx)
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
