package bridgeprotocol

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

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
