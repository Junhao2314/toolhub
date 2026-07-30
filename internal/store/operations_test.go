package store

import (
	"encoding/json"
	"testing"
)

func TestControlOperationKindsIncludeScheduledAndManualControlWork(t *testing.T) {
	want := map[string]bool{"skill_import": true, "update_check": true, "refresh": true, "backup_gc": true}
	for _, kind := range controlOperationKinds {
		delete(want, kind)
	}
	if len(want) != 0 {
		t.Fatalf("control operation claim omits kinds: %v", want)
	}
}

func TestMarshalJSONObjectNormalizesNilAndRejectsNonObjects(t *testing.T) {
	for name, input := range map[string]any{
		"nil":       nil,
		"typed nil": map[string]any(nil),
		"map":       map[string]any{"scheduled": true},
		"raw":       json.RawMessage(`{"targetRevision":"abc"}`),
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := marshalJSONObject(input)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(encoded, &object); err != nil || object == nil {
				t.Fatalf("expected a JSON object, got %q: %v", encoded, err)
			}
		})
	}
	for _, input := range []any{[]string{"not", "an", "object"}, json.RawMessage(`null`), json.RawMessage(`[]`)} {
		if _, err := marshalJSONObject(input); err == nil {
			t.Fatalf("expected %T input to be rejected", input)
		}
	}
}
