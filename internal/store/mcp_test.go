package store

import "testing"

func TestNormalizeMCPSecretChanges(t *testing.T) {
	envSet, envRemove, headerSet, headerRemove, changed, err := normalizeMCPSecretChanges(&MCPSecretChanges{
		Env:     MCPSecretDelta{Set: map[string]string{" TOKEN ": "new-value"}, Remove: []string{"OLD", "OLD"}},
		Headers: MCPSecretDelta{Set: map[string]string{"authorization": "Bearer value"}, Remove: []string{"X-Old"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || envSet["TOKEN"] != "new-value" || len(envRemove) != 1 || envRemove[0] != "OLD" || headerSet["Authorization"] != "Bearer value" || len(headerRemove) != 1 || headerRemove[0] != "X-Old" {
		t.Fatalf("normalized envSet=%+v envRemove=%+v headerSet=%+v headerRemove=%+v changed=%v", envSet, envRemove, headerSet, headerRemove, changed)
	}
	for _, changes := range []*MCPSecretChanges{
		{Env: MCPSecretDelta{Set: map[string]string{"TOKEN": "value"}, Remove: []string{"TOKEN"}}},
		{Env: MCPSecretDelta{Set: map[string]string{"TOKEN": ""}}},
		{Headers: MCPSecretDelta{Set: map[string]string{"bad header": "value"}}},
	} {
		if _, _, _, _, _, err := normalizeMCPSecretChanges(changes); err == nil {
			t.Fatalf("invalid secret delta accepted: %+v", changes)
		}
	}
}
