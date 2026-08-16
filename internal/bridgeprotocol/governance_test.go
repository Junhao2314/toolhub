package bridgeprotocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRoutingBundleAcceptsTheCompanionStrictContractShape(t *testing.T) {
	body := []byte(`{"schemaVersion":1,"mode":"compatibility","relayConfigurationRevisionId":"00000000-0000-0000-0000-000000000001","relayConfigurationHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","globalPolicyRevisionId":"00000000-0000-0000-0000-000000000002","globalPolicyHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","defaultProfileId":null,"servers":[{"serverId":"00000000-0000-0000-0000-000000000003","serverName":"acemcp","mcpConfigRevisionId":"00000000-0000-0000-0000-000000000004","acceptedContractRevisionId":null,"acceptedContractHash":null,"tools":[]}],"profiles":[]}`)
	var bundle RoutingBundle
	if err := DecodeGovernanceBody(body, &bundle); err != nil {
		t.Fatalf("companion routing shape rejected: %v", err)
	}
	if len(bundle.Profiles) != 0 {
		t.Fatalf("decoded bundle=%+v", bundle)
	}
}

func TestRoutingBundleAllowsPayloadLikeJSONSchemaPropertyNames(t *testing.T) {
	body := []byte(`{"schemaVersion":1,"mode":"compatibility","relayConfigurationRevisionId":"00000000-0000-0000-0000-000000000001","relayConfigurationHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","globalPolicyRevisionId":"00000000-0000-0000-0000-000000000002","globalPolicyHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","defaultProfileId":null,"servers":[{"serverId":"00000000-0000-0000-0000-000000000003","serverName":"acemcp","mcpConfigRevisionId":"00000000-0000-0000-0000-000000000004","acceptedContractRevisionId":null,"acceptedContractHash":null,"tools":[{"toolId":"00000000-0000-0000-0000-000000000005","name":"submit","inputSchema":{"type":"object","properties":{"arguments":{"type":"string"},"result":{"type":"string"},"prompt":{"type":"string"}}},"outputSchema":{},"annotations":{},"globalDecision":"allow","reasonCodes":[],"paused":false}]}],"profiles":[]}`)
	var bundle RoutingBundle
	if err := DecodeGovernanceBody(body, &bundle); err != nil {
		t.Fatalf("routing bundle rejected legitimate JSON Schema property names: %v", err)
	}
}

func TestGovernanceBodyRejectsPayloadFieldsOutsideJSONSchemas(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"arguments":{"id":"payload"}}`),
		[]byte(`{"annotations":{"result":"payload"}}`),
		[]byte(`{"annotations":{"results":"payload"}}`),
		[]byte(`{"inputSchema":{},"prompt":"payload"}`),
		[]byte(`{"inputSchema":{},"prompts":"payload"}`),
		[]byte(`{"metadata":{"secretValue":"payload"}}`),
		[]byte(`{"annotations":{"inputSchema":{"prompt":"payload"}}}`),
		[]byte(`{"annotations":{"input_schema":{"secret_value":"payload"}}}`),
		[]byte(`{"metadata":{"output-schema":{"results":"payload"}}}`),
	} {
		if err := ValidateGovernanceBody(body); err == nil {
			t.Fatalf("governance body accepted payload field: %s", body)
		}
	}
}

func TestRelaySessionCanaryResponseIsBoundToCandidateProfilesAndProcesses(t *testing.T) {
	claudeID := "00000000-0000-0000-0000-000000000010"
	claudeRevisionID := "00000000-0000-0000-0000-000000000011"
	codexID := "00000000-0000-0000-0000-000000000020"
	codexRevisionID := "00000000-0000-0000-0000-000000000021"
	serverID := "00000000-0000-0000-0000-000000000030"
	bundle := RoutingBundle{
		SchemaVersion: 1, Mode: "enforced",
		RelayConfigurationRevisionID: "00000000-0000-0000-0000-000000000001",
		RelayConfigurationHash:       strings.Repeat("a", 64),
		GlobalPolicyRevisionID:       "00000000-0000-0000-0000-000000000002",
		GlobalPolicyHash:             strings.Repeat("b", 64),
		Servers: []ServerContractDTO{{
			ServerID: serverID, ServerName: "search", MCPConfigRevisionID: "00000000-0000-0000-0000-000000000031",
			AcceptedContractRevisionID: stringPointer("00000000-0000-0000-0000-000000000032"), AcceptedContractHash: stringPointer(strings.Repeat("c", 64)), Tools: []RoutingToolDTO{},
		}},
		Profiles: []PublishedProfileDTO{
			{ProfileID: claudeID, ProfileRevisionID: claudeRevisionID, ProfileRevisionHash: strings.Repeat("d", 64), ProfileName: "claude", ClientKind: RuntimeClaude, Servers: []ProfileServerRoutingDTO{}},
			{ProfileID: codexID, ProfileRevisionID: codexRevisionID, ProfileRevisionHash: strings.Repeat("e", 64), ProfileName: "codex", ClientKind: RuntimeCodex, Servers: []ProfileServerRoutingDTO{}},
		},
	}
	_, hash, err := bundle.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	response := RelaySessionCanaryResponse{
		RoutingBundleHash: hash,
		Profiles: []RelaySessionCanaryProfile{
			{ClientKind: RuntimeClaude, ProfileID: claudeID, ProfileRevisionID: claudeRevisionID, ToolCount: 0},
			{ClientKind: RuntimeCodex, ProfileID: codexID, ProfileRevisionID: codexRevisionID, ToolCount: 0},
		},
		MissingProfile:          RelaySessionCanaryMissing{Behavior: "empty", ToolCount: 0},
		InvalidProfileErrorCode: "profile_unknown",
		ConcurrentSessionCount:  2,
		UpstreamProcesses:       []RelaySessionCanaryProcess{{ServerID: serverID, ProcessCount: 1}},
	}
	if err := ValidateRelaySessionCanaryResponse(bundle, hash, response); err != nil {
		t.Fatalf("valid session canary rejected: %v", err)
	}
	response.InvalidProfileErrorCode = "profile_invalid"
	if err := ValidateRelaySessionCanaryResponse(bundle, hash, response); err == nil {
		t.Fatal("session canary accepted a malformed-only Profile check")
	}
	response.InvalidProfileErrorCode = "profile_unknown"
	response.UpstreamProcesses[0].ProcessCount = 2
	if err := ValidateRelaySessionCanaryResponse(bundle, hash, response); err == nil {
		t.Fatal("session canary accepted duplicate upstream processes")
	}
}

func TestRoutingBundleCanonicalUsesRFC8785CompatibleBytes(t *testing.T) {
	bundle := RoutingBundle{
		SchemaVersion:                1,
		Mode:                         "compatibility",
		RelayConfigurationRevisionID: "00000000-0000-0000-0000-000000000001",
		RelayConfigurationHash:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		GlobalPolicyRevisionID:       "00000000-0000-0000-0000-000000000002",
		GlobalPolicyHash:             "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Servers: []ServerContractDTO{{
			ServerID:            "00000000-0000-0000-0000-000000000003",
			ServerName:          "acemcp",
			MCPConfigRevisionID: "00000000-0000-0000-0000-000000000004",
			Tools:               []RoutingToolDTO{},
		}},
		Profiles: []PublishedProfileDTO{},
	}
	body, hash, err := bundle.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"defaultProfileId":null,"globalPolicyHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","globalPolicyRevisionId":"00000000-0000-0000-0000-000000000002","mode":"compatibility","profiles":[],"relayConfigurationHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","relayConfigurationRevisionId":"00000000-0000-0000-0000-000000000001","schemaVersion":1,"servers":[{"acceptedContractHash":null,"acceptedContractRevisionId":null,"mcpConfigRevisionId":"00000000-0000-0000-0000-000000000004","serverId":"00000000-0000-0000-0000-000000000003","serverName":"acemcp","tools":[]}]}`
	if string(body) != want {
		t.Fatalf("canonical body=%s\nwant=%s", body, want)
	}
	if hash != "b0dc0aadba0c8903ac51e6e108c6a011348cb7d149118c560b0d462829bce1af" {
		t.Fatalf("canonical hash=%s", hash)
	}
}

func TestRoutingBundleCanonicalMatchesRFC8785NumberSerialization(t *testing.T) {
	bundle := RoutingBundle{
		SchemaVersion:                1,
		Mode:                         "compatibility",
		RelayConfigurationRevisionID: "00000000-0000-0000-0000-000000000001",
		RelayConfigurationHash:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		GlobalPolicyRevisionID:       "00000000-0000-0000-0000-000000000002",
		GlobalPolicyHash:             "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Servers: []ServerContractDTO{{
			ServerID:            "00000000-0000-0000-0000-000000000003",
			ServerName:          "acemcp",
			MCPConfigRevisionID: "00000000-0000-0000-0000-000000000004",
			Tools: []RoutingToolDTO{{
				ToolID:         "00000000-0000-0000-0000-000000000005",
				Name:           "vector",
				InputSchema:    map[string]any{"numbers": []any{333333333.33333329, 1e30, 4.50, 2e-3, 1e-27}},
				Annotations:    map[string]any{},
				GlobalDecision: "allow",
				ReasonCodes:    []string{},
			}},
		}},
		Profiles: []PublishedProfileDTO{},
	}
	body, _, err := bundle.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27]`)
	if !bytes.Contains(body, want) {
		t.Fatalf("canonical body does not contain RFC 8785 number vector:\n%s", body)
	}
}

func TestGovernanceCapabilityDTOHasNoPayloadFields(t *testing.T) {
	response := RelayCapabilityResponse{AdminProtocolVersion: 1, Features: []string{"tool-filtering"}, RoutingSchemaVersions: []int{1}, Runtime: "mcpm", RuntimeVersion: "2.15.0-toolhub.1"}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"arguments", "result", "results", "prompt", "prompts", "rawError", "secretValue", "secretValues", "sessionId"} {
		if containsJSONKey(body, forbidden) {
			t.Fatalf("capability DTO exposed %q: %s", forbidden, body)
		}
	}
	if err := ValidateGovernanceBody(body); err != nil {
		t.Fatal(err)
	}
}

func stringPointer(value string) *string { return &value }

func TestObservationDrainAndConfirmationDTOsRoundTrip(t *testing.T) {
	bootID := uuid.NewString()
	observation := Observation{BootID: bootID, Sequence: 4, ObservedAt: 1_786_838_400, MinuteBucket: "2026-08-16T00:00:00Z", ProfileID: uuid.NewString(), ProfileRevisionID: uuid.NewString(), ServerID: uuid.NewString(), ToolID: uuid.NewString(), Decision: "confirm", ReasonCodes: []string{"mutating"}, Outcome: "confirmation_required", ErrorClass: "timeout", DurationBucket: "lt_1s"}
	body, err := json.Marshal(ObservationDrainResponse{BootID: bootID, NextSequence: 4, Items: []Observation{observation}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded ObservationDrainResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.NextSequence != 4 || len(decoded.Items) != 1 || decoded.Items[0].ToolID != observation.ToolID || decoded.Items[0].BootID != bootID {
		t.Fatalf("decoded=%+v", decoded)
	}
	if err := ValidateGovernanceBody(body); err != nil {
		t.Fatal(err)
	}
}

func TestGovernanceBodyItemLimitIsCumulativeAcrossTheWholeTree(t *testing.T) {
	items := make([]any, GovernanceMaxItems)
	for index := range items {
		items[index] = index
	}
	body, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGovernanceBody(body); err == nil {
		t.Fatal("governance body accepted more than the global item limit")
	}
}

func containsJSONKey(body []byte, key string) bool {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(item any) bool {
		switch typed := item.(type) {
		case map[string]any:
			for childKey, child := range typed {
				if childKey == key || walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}
