package bridgeprotocol

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validManifest() DesiredManifest {
	skillMember := uuid.NewString()
	mcpMember := uuid.NewString()
	return DesiredManifest{
		SchemaVersion: ManifestSchemaVersion,
		Target:        Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: NodeKindSalt, SaltMinionID: "minion-a", Runtime: RuntimeClaude, ManagedUsername: "operator"},
		ProfileID:     uuid.NewString(), ProfileRevision: 3,
		Skills:           []SkillMember{{MemberID: skillMember, SkillID: uuid.NewString(), VersionID: uuid.NewString(), Slug: "safe-skill", SHA256: string(bytes.Repeat([]byte{'a'}, 64)), ContentHash: string(bytes.Repeat([]byte{'c'}, 64))}},
		MCPServers:       []MCPMember{{MemberID: mcpMember, ServerID: uuid.NewString(), Revision: 2, Name: "docs", Transport: "http", URL: "https://example.invalid/mcp", HeaderRefs: map[string]string{"Authorization": uuid.NewString()}, ContentHash: string(bytes.Repeat([]byte{'b'}, 64))}},
		ManagedMemberIDs: []string{skillMember, mcpMember},
	}
}

func TestManifestCanonicalHashIsOrderStable(t *testing.T) {
	manifest := validManifest()
	_, first, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManagedMemberIDs[0], manifest.ManagedMemberIDs[1] = manifest.ManagedMemberIDs[1], manifest.ManagedMemberIDs[0]
	_, second, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("canonical hash changed with input order: %s != %s", first, second)
	}
}

func TestManifestRejectsHermesWritesAndInvalidSecretReferences(t *testing.T) {
	manifest := validManifest()
	manifest.Target.Runtime = RuntimeHermes
	if err := manifest.Validate(true); err == nil {
		t.Fatal("expected Hermes write rejection")
	}
	manifest = validManifest()
	manifest.MCPServers[0].HeaderRefs["Authorization"] = "plaintext-secret"
	if err := manifest.Validate(true); err == nil {
		t.Fatal("expected non-reference secret rejection")
	}
}

func TestManifestRejectsProtectedSkill(t *testing.T) {
	manifest := validManifest()
	manifest.Skills[0].Slug = ".system"
	if err := manifest.Validate(true); err == nil {
		t.Fatal("expected protected Skill rejection")
	}
}

func TestManifestV2SharedRelayRequiresSecretFreeGovernanceAndCanonicalRoutingHash(t *testing.T) {
	legacy := validManifest()
	legacy.Target = Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: NodeKindLocal, Runtime: RuntimeSharedRelay, ManagedUsername: "operator"}
	legacy.Skills = []SkillMember{}
	legacy.MCPServers = []MCPMember{}
	legacy.ManagedMemberIDs = []string{}
	legacy.RelayPort = 6276
	legacy.SchemaVersion = 2
	routing := RoutingBundle{SchemaVersion: 1, Mode: "compatibility", RelayConfigurationRevisionID: uuid.NewString(), RelayConfigurationHash: string(bytes.Repeat([]byte{'a'}, 64)), GlobalPolicyRevisionID: uuid.NewString(), GlobalPolicyHash: string(bytes.Repeat([]byte{'b'}, 64))}
	routingBody, routingHash, err := routing.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	legacy.RelayGovernance = &RelayGovernanceManifest{RelayConfigurationRevisionID: routing.RelayConfigurationRevisionID, RelayConfigurationHash: routing.RelayConfigurationHash, RoutingBundle: routingBody, RoutingHash: routingHash}
	if err := legacy.Validate(true); err != nil {
		t.Fatalf("valid v2 rejected: %v", err)
	}
	legacy.RelayGovernance.RoutingHash = string(bytes.Repeat([]byte{'c'}, 64))
	if err := legacy.Validate(true); err == nil {
		t.Fatal("v2 accepted a mismatched routing hash")
	}
	nonRelay := validManifest()
	nonRelay.SchemaVersion = 2
	nonRelay.RelayGovernance = &RelayGovernanceManifest{RelayConfigurationRevisionID: uuid.NewString(), RelayConfigurationHash: string(bytes.Repeat([]byte{'a'}, 64)), RoutingBundle: routingBody, RoutingHash: routingHash}
	if err := nonRelay.Validate(true); err == nil {
		t.Fatal("non-relay v2 accepted governance")
	}
}

func TestGovernanceDTOValidationRejectsSensitiveFieldsAndBounds(t *testing.T) {
	unsafe := []byte(`{"challengeId":"abc","arguments":{"token":"secret"}}`)
	if err := ValidateGovernanceBody(unsafe); err == nil {
		t.Fatal("sensitive governance body accepted")
	}
	tooDeep := []byte(`{"a":{"b":{"c":{"d":{"e":{"f":true}}}}}}`)
	if err := ValidateGovernanceBody(tooDeep); err == nil {
		t.Fatal("deep governance body accepted")
	}
	response := ContractObservationResponse{ServerID: uuid.NewString(), ContractRevisionID: uuid.NewString(), Tools: []ContractToolDTO{{Name: "read_item", InputSchema: map[string]any{}, OutputSchema: map[string]any{}, Annotations: map[string]any{}, Presentation: map[string]any{}}}}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGovernanceBody(body); err != nil {
		t.Fatalf("safe governance body rejected: %v", err)
	}
}

func TestSignedRequestDetectsTamperingAndExpiry(t *testing.T) {
	key := bytes.Repeat([]byte{'k'}, 32)
	now := time.Unix(1_800_000_000, 0)
	request, err := http.NewRequest(http.MethodPost, "http://unix/v1/targets/apply?mode=commit", bytes.NewReader([]byte(`{"ok":true}`)))
	if err != nil {
		t.Fatal(err)
	}
	SignRequest(request, key, []byte(`{"ok":true}`), now, "1234567890abcdef")
	if err := VerifyRequest(request, key, []byte(`{"ok":true}`), now.Add(29*time.Second), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRequest(request, key, []byte(`{"ok":false}`), now, 30*time.Second); err == nil {
		t.Fatal("expected tampered body rejection")
	}
	if err := VerifyRequest(request, key, []byte(`{"ok":true}`), now.Add(31*time.Second), 30*time.Second); err == nil {
		t.Fatal("expected expired request rejection")
	}
}

func TestProtectedScopes(t *testing.T) {
	for _, name := range []string{".system", ".hidden", "toolhub-internal", ".."} {
		if !IsProtectedSkillEntry(name) {
			t.Fatalf("expected %q to be protected", name)
		}
	}
	if IsProtectedSkillEntry("my-skill") {
		t.Fatal("ordinary Skill was protected")
	}
	if !IsManagedMCPScope("user") || IsManagedMCPScope("project") || IsManagedMCPScope("managed") || IsManagedMCPScope("plugin") {
		t.Fatal("MCP scope boundary is incorrect")
	}
}

func TestIsSHA256RequiresCanonicalLowercaseHex(t *testing.T) {
	if !IsSHA256(string(bytes.Repeat([]byte{'a'}, 64))) {
		t.Fatal("canonical SHA-256 was rejected")
	}
	for _, value := range []string{"", string(bytes.Repeat([]byte{'a'}, 63)), string(bytes.Repeat([]byte{'A'}, 64)), string(bytes.Repeat([]byte{'z'}, 64))} {
		if IsSHA256(value) {
			t.Fatalf("invalid SHA-256 %q was accepted", value)
		}
	}
}

func TestValidateManagedUsername(t *testing.T) {
	for _, value := range []string{"root", "toolhub_user", "build-runner", "_service"} {
		if err := ValidateManagedUsername(value); err != nil {
			t.Fatalf("valid managed username %q was rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", "Root", "two words", "-service", "user.name", "abcdefghijklmnopqrstuvwxyzabcdefg"} {
		if err := ValidateManagedUsername(value); err == nil {
			t.Fatalf("invalid managed username %q was accepted", value)
		}
	}

	manifest := validManifest()
	manifest.Target.ManagedUsername = "Root"
	if err := manifest.Validate(true); err == nil {
		t.Fatal("manifest accepted an invalid managed username")
	}
}

func TestRelayStatusRoundTripKeepsOnlyTypedHealthProjection(t *testing.T) {
	checkedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	result := TargetResult{Status: OperationSucceeded, Health: HealthBlocked, Relay: &RelayStatus{State: "active", Endpoint: "http://127.0.0.1:6276/mcp", FixedPort: 6276, SystemdEnabled: true, Contract: "incompatible", MemberStatuses: []RelayMemberStatus{{MemberID: uuid.NewString(), Name: "server", Status: "unavailable", CapabilityKinds: []string{}, Capabilities: RelayCapabilityCounts{}, CheckedAt: checkedAt, ErrorCode: ErrMCPMIncompatible, ErrorReason: "namespace unavailable"}}, ErrorCode: ErrMCPMIncompatible, ErrorReason: "namespace unavailable"}}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"command", "args", "env", "headers", "secretValues", "rawOutput"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("relay projection exposed forbidden field %q: %s", forbidden, body)
		}
	}
	var decoded TargetResult
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, decoded) {
		t.Fatalf("relay DTO round trip changed: before=%+v after=%+v", result, decoded)
	}
}
