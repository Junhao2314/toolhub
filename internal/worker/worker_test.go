package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/store"
)

type enforcementReadinessBridge struct {
	calls       []string
	capability  bridgeprotocol.RelayCapabilityResponse
	inspections map[string]bridgeprotocol.NativeClientInspectionResponse
	canary      bridgeprotocol.RelaySessionCanaryResponse
}

func (bridge *enforcementReadinessBridge) RelayCapability(context.Context) (bridgeprotocol.RelayCapabilityResponse, error) {
	bridge.calls = append(bridge.calls, "capability")
	return bridge.capability, nil
}

func (bridge *enforcementReadinessBridge) InspectNativeClient(_ context.Context, input bridgeprotocol.NativeClientInspectionRequest) (bridgeprotocol.NativeClientInspectionResponse, error) {
	bridge.calls = append(bridge.calls, input.ClientKind)
	return bridge.inspections[input.ClientKind], nil
}

func (bridge *enforcementReadinessBridge) RelaySessionCanary(_ context.Context, input bridgeprotocol.RelaySessionCanaryRequest) (bridgeprotocol.RelaySessionCanaryResponse, error) {
	bridge.calls = append(bridge.calls, "session-canary")
	bridge.canary.RoutingBundleHash = input.RoutingBundleHash
	return bridge.canary, nil
}

func TestProjectScanHealthDetectsPinnedDriftAndIgnoresUnmanagedExtras(t *testing.T) {
	matchingHash := strings.Repeat("a", 64)
	desiredMCPHash := strings.Repeat("b", 64)
	manifest := bridgeprotocol.DesiredManifest{
		Skills: []bridgeprotocol.SkillMember{
			{MemberID: "skill-match", Slug: "matching", ContentHash: matchingHash},
			{MemberID: "skill-missing", Slug: "missing", ContentHash: strings.Repeat("c", 64)},
		},
		MCPServers: []bridgeprotocol.MCPMember{{MemberID: "mcp-replace", Name: "search", ContentHash: desiredMCPHash}},
	}
	members := []bridgeprotocol.InventoryMember{
		{Kind: "skill", Name: "matching", ContentHash: matchingHash},
		{Kind: "skill", Name: "missing", ContentHash: strings.Repeat("c", 64), Protected: true},
		{Kind: "mcp", Name: "search", ContentHash: strings.Repeat("d", 64)},
		{Kind: "skill", Name: "unmanaged-extra", ContentHash: strings.Repeat("e", 64)},
	}

	health, drift := projectScanHealth(manifest, members)
	if health != bridgeprotocol.HealthDrifted {
		t.Fatalf("health=%q", health)
	}
	if len(drift.Add) != 1 || drift.Add[0].Name != "missing" || drift.Add[0].Reason != "protected" {
		t.Fatalf("add=%+v", drift.Add)
	}
	if len(drift.Replace) != 1 || drift.Replace[0].Name != "search" || drift.Replace[0].Reason != "content_hash_mismatch" {
		t.Fatalf("replace=%+v", drift.Replace)
	}
	if len(drift.Delete) != 0 || len(drift.Excluded) != 0 {
		t.Fatalf("unmanaged inventory must not be projected as drift: %+v", drift)
	}
}

func TestProjectScanHealthMarksMatchingPinnedMembersHealthy(t *testing.T) {
	hash := strings.Repeat("a", 64)
	manifest := bridgeprotocol.DesiredManifest{Skills: []bridgeprotocol.SkillMember{{MemberID: "managed", Slug: "formatter", ContentHash: hash}}}
	members := []bridgeprotocol.InventoryMember{
		{Kind: "skill", Name: "formatter", ContentHash: hash},
		{Kind: "skill", Name: "later-unmanaged", ContentHash: strings.Repeat("b", 64)},
	}

	health, drift := projectScanHealth(manifest, members)
	if health != bridgeprotocol.HealthHealthy {
		t.Fatalf("health=%q drift=%+v", health, drift)
	}
	if len(drift.Add) != 0 || len(drift.Replace) != 0 || len(drift.Delete) != 0 || len(drift.Excluded) != 0 {
		t.Fatalf("drift=%+v", drift)
	}
}

func TestValidateTargetBindingRejectsManagedUsernameChange(t *testing.T) {
	target := bridgeprotocol.Target{
		ID:              "11111111-1111-4111-8111-111111111111",
		NodeID:          "22222222-2222-4222-8222-222222222222",
		NodeKind:        bridgeprotocol.NodeKindLocal,
		Runtime:         bridgeprotocol.RuntimeClaude,
		ManagedUsername: "new-user",
	}
	manifest := bridgeprotocol.DesiredManifest{Target: target}
	manifest.Target.ManagedUsername = "old-user"

	assertRevisionConflict(t, validateTargetBinding(target, manifest, 0))
}

func TestValidateTargetBindingRejectsRelayPortChange(t *testing.T) {
	target := bridgeprotocol.Target{
		ID:              "11111111-1111-4111-8111-111111111111",
		NodeID:          "22222222-2222-4222-8222-222222222222",
		NodeKind:        bridgeprotocol.NodeKindLocal,
		Runtime:         bridgeprotocol.RuntimeSharedRelay,
		ManagedUsername: "runner",
	}
	manifest := bridgeprotocol.DesiredManifest{Target: target, RelayPort: 6276}

	if err := validateTargetBinding(target, manifest, 6276); err != nil {
		t.Fatalf("unchanged binding was rejected: %v", err)
	}
	assertRevisionConflict(t, validateTargetBinding(target, manifest, 6277))
}

func TestPublicErrorReducesUntrustedBridgeErrorToBoundedClass(t *testing.T) {
	apiErr := publicError(&bridgeprotocol.APIError{Code: "upstream_secret_code", Message: "raw upstream marker", Details: map[string]any{"rawOutput": "secret"}})
	if apiErr.Code != bridgeprotocol.ErrInvalidRequest || apiErr.Message != "Request is invalid" || apiErr.Details != nil {
		t.Fatalf("worker public error was not bounded: %+v", apiErr)
	}
}

func TestRelayResultProjectionPreservesBlockedSuccess(t *testing.T) {
	status := bridgeprotocol.RelayStatus{Healthy: false, ErrorCode: bridgeprotocol.ErrRelayUnhealthy, ErrorReason: "relay unavailable"}
	result := bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthBlocked, Relay: &status, Error: relayProjectionError(status)}
	if normalizedResultHealth(result.Health) != bridgeprotocol.HealthBlocked {
		t.Fatal("blocked result was normalized to healthy")
	}
	code, reason := resultProjectionError(result)
	if code != bridgeprotocol.ErrRelayUnhealthy || reason != "Relay runtime is unhealthy" || !result.Error.Retryable {
		t.Fatalf("relay projection error code=%q reason=%q error=%+v", code, reason, result.Error)
	}
}

func TestPausedRelayIsHealthyOnlyWhenPauseIsEnforced(t *testing.T) {
	paused := bridgeprotocol.RelayStatus{Healthy: true, IntentionalPaused: true, State: "inactive", SystemdEnabled: false}
	if relayProjectedHealth(paused) != bridgeprotocol.HealthHealthy || relayProjectionError(paused) != nil {
		t.Fatalf("enforced pause projected incorrectly: %+v", paused)
	}
	failed := bridgeprotocol.RelayStatus{IntentionalPaused: true, State: "active", SystemdEnabled: true, ErrorCode: bridgeprotocol.ErrRelayUnhealthy, ErrorReason: "intentional relay pause is not enforced"}
	if relayProjectedHealth(failed) != bridgeprotocol.HealthBlocked || relayProjectionError(failed) == nil {
		t.Fatalf("unenforced pause projected incorrectly: %+v", failed)
	}
}

func TestRuntimeInventoryPayloadUsesPostCommitScan(t *testing.T) {
	member := bridgeprotocol.InventoryMember{ID: "skill:formatter", Kind: "skill", Name: "formatter"}
	relay := &bridgeprotocol.RelayStatus{State: "active", Healthy: true, FixedPort: 6276}
	payload := runtimeInventoryPayload(bridgeprotocol.ScanResponse{
		TargetRevision: strings.Repeat("a", 64),
		Members:        []bridgeprotocol.InventoryMember{member},
	}, bridgeprotocol.TargetResult{Relay: relay})

	members, ok := payload["members"].([]bridgeprotocol.InventoryMember)
	if !ok || len(members) != 1 || members[0].Name != "formatter" {
		t.Fatalf("members=%#v", payload["members"])
	}
	if payload["relay"] != relay {
		t.Fatalf("relay=%#v", payload["relay"])
	}
}

func TestGovernanceCommitKindsUseTypedApplyAndPersistReloadEvidence(t *testing.T) {
	routingHash := strings.Repeat("a", 64)
	manifest := bridgeprotocol.DesiredManifest{
		SchemaVersion: bridgeprotocol.ManifestSchemaVersionV2,
		RelayGovernance: &bridgeprotocol.RelayGovernanceManifest{
			RoutingHash: routingHash,
		},
	}
	request, err := json.Marshal(map[string]any{"manifest": manifest})
	if err != nil {
		t.Fatal(err)
	}
	for _, operationKind := range []string{"apply", "relay_config_apply", "policy_apply"} {
		if kind := bridgeCommitKind(operationKind); kind != "apply" {
			t.Fatalf("operation %s used Bridge kind %s", operationKind, kind)
		}
		item := store.WorkItem{
			Operation:       domain.Operation{Kind: operationKind},
			OperationTarget: domain.OperationTarget{Request: request},
			Target:          domain.Target{Runtime: domain.RuntimeSharedRelay},
		}
		persisted := persistedTargetResult(item, bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, Manifest: &manifest})
		body, err := json.Marshal(persisted)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["routingReloaded"] != true || decoded["routingHash"] != routingHash {
			t.Fatalf("operation %s evidence=%s", operationKind, body)
		}
	}
}

func TestGovernanceApplyDefersDesiredSnapshotToFinalizer(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		sourceKind string
		runtime    string
		want       bool
	}{
		{name: "profile skill apply", kind: "apply", sourceKind: "profile_apply", runtime: domain.RuntimeClaude, want: true},
		{name: "profile relay apply", kind: "apply", sourceKind: "profile_apply", runtime: domain.RuntimeSharedRelay, want: true},
		{name: "relay configuration apply", kind: "relay_config_apply", sourceKind: "relay_config_apply", runtime: domain.RuntimeSharedRelay, want: true},
		{name: "global policy apply", kind: "policy_apply", sourceKind: "relay_config_apply", runtime: domain.RuntimeSharedRelay, want: true},
		{name: "target edit", kind: "edit", sourceKind: "target_edit", runtime: domain.RuntimeSharedRelay, want: false},
		{name: "restore", kind: "restore", sourceKind: "restore", runtime: domain.RuntimeSharedRelay, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := store.WorkItem{Operation: domain.Operation{Kind: test.kind}, Target: domain.Target{Runtime: test.runtime}}
			if got := desiredSnapshotOwnedByFinalizer(item, test.sourceKind); got != test.want {
				t.Fatalf("desiredSnapshotOwnedByFinalizer(%s, %s, %s)=%v, want %v", test.kind, test.sourceKind, test.runtime, got, test.want)
			}
		})
	}
}

func TestEnforcementPreflightChecksCapabilityThenClaudeThenCodex(t *testing.T) {
	bundle, body, hash := enforcementSessionCanaryBundle(t)
	bridge := &enforcementReadinessBridge{
		capability: bridgeprotocol.RelayCapabilityResponse{
			AdminProtocolVersion: 1,
			Features: []string{
				"profile-session-binding", "tool-filtering", "call-policy",
				"one-shot-confirmation", "payload-free-observations", "routing-hot-reload", "session-canary",
			},
			RoutingSchemaVersions: []int{1}, Runtime: "mcpm", RuntimeVersion: "2.15.0-toolhub.1",
		},
		inspections: map[string]bridgeprotocol.NativeClientInspectionResponse{
			"claude": {ClientKind: "claude", Version: "2.1.232", Supported: true},
			"codex":  {ClientKind: "codex", Version: "0.147.0", Supported: true},
		},
		canary: bridgeprotocol.RelaySessionCanaryResponse{
			Profiles: []bridgeprotocol.RelaySessionCanaryProfile{
				{ClientKind: bridgeprotocol.RuntimeClaude, ProfileID: bundle.Profiles[0].ProfileID, ProfileRevisionID: bundle.Profiles[0].ProfileRevisionID, ToolCount: 0},
				{ClientKind: bridgeprotocol.RuntimeCodex, ProfileID: bundle.Profiles[1].ProfileID, ProfileRevisionID: bundle.Profiles[1].ProfileRevisionID, ToolCount: 0},
			},
			MissingProfile: bridgeprotocol.RelaySessionCanaryMissing{Behavior: "empty", ToolCount: 0}, InvalidProfileErrorCode: "profile_unknown", ConcurrentSessionCount: 2, UpstreamProcesses: []bridgeprotocol.RelaySessionCanaryProcess{},
		},
	}
	request := bridgeprotocol.RelaySessionCanaryRequest{RoutingBundleHash: hash, RoutingBundle: body}
	if apiErr := validateEnforcementRuntime(context.Background(), bridge, "runner", request); apiErr != nil {
		t.Fatalf("valid enforcement runtime rejected: %+v", apiErr)
	}
	if got := strings.Join(bridge.calls, ","); got != "capability,claude,codex,session-canary" {
		t.Fatalf("enforcement check order=%s", got)
	}

	bridge.calls = nil
	bridge.capability.Features = bridge.capability.Features[:len(bridge.capability.Features)-1]
	if apiErr := validateEnforcementRuntime(context.Background(), bridge, "runner", request); apiErr == nil || apiErr.Code != bridgeprotocol.ErrMCPMIncompatible {
		t.Fatalf("incomplete capability error=%+v", apiErr)
	}
	if got := strings.Join(bridge.calls, ","); got != "capability" {
		t.Fatalf("client inspection ran after incompatible capability: %s", got)
	}

	bridge.capability.Features = append(bridge.capability.Features, "session-canary")
	bridge.calls = nil
	bridge.inspections["codex"] = bridgeprotocol.NativeClientInspectionResponse{ClientKind: "codex", ErrorCode: "native_client_not_found"}
	if apiErr := validateEnforcementRuntime(context.Background(), bridge, "runner", request); apiErr == nil || apiErr.Code != bridgeprotocol.ErrMCPMIncompatible {
		t.Fatalf("unsupported Codex error=%+v", apiErr)
	}
	if got := strings.Join(bridge.calls, ","); got != "capability,claude,codex" {
		t.Fatalf("unsupported client check order=%s", got)
	}
}

func enforcementSessionCanaryBundle(t *testing.T) (bridgeprotocol.RoutingBundle, json.RawMessage, string) {
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

func assertRevisionConflict(t *testing.T, err error) {
	t.Helper()
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrRevisionConflict {
		t.Fatalf("expected revision conflict, got %v", err)
	}
}
