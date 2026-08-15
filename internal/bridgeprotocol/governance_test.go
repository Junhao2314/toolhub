package bridgeprotocol

import (
	"encoding/json"
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

func TestGovernanceCapabilityDTOHasNoPayloadFields(t *testing.T) {
	response := RelayCapabilityResponse{AdminProtocolVersion: 1, Features: []string{"tool-filtering"}, RoutingSchemaVersions: []int{1}, Runtime: "mcpm", RuntimeVersion: "2.15.0-toolhub.1"}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"arguments", "result", "prompt", "rawError", "secretValues", "sessionId"} {
		if containsJSONKey(body, forbidden) {
			t.Fatalf("capability DTO exposed %q: %s", forbidden, body)
		}
	}
	if err := ValidateGovernanceBody(body); err != nil {
		t.Fatal(err)
	}
}

func TestObservationDrainAndConfirmationDTOsRoundTrip(t *testing.T) {
	observation := Observation{ProfileID: uuid.NewString(), ProfileRevisionID: uuid.NewString(), ServerID: uuid.NewString(), ToolID: uuid.NewString(), Decision: "confirm", Outcome: "confirmation_required", ErrorClass: "timeout", DurationBucket: "100-250ms"}
	body, err := json.Marshal(ObservationDrainResponse{BootID: "boot-1", Cursor: 4, Items: []Observation{observation}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded ObservationDrainResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Cursor != 4 || len(decoded.Items) != 1 || decoded.Items[0].ToolID != observation.ToolID {
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
