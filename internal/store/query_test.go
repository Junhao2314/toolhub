package store

import "testing"

func TestJSONTextUsesTextParameterForSimpleProtocol(t *testing.T) {
	encoded := []byte(`{"scheduled":true}`)
	parameter := jsonText(encoded)
	if parameter != string(encoded) {
		t.Fatalf("unexpected JSON text parameter: %q", parameter)
	}
	encoded[0] = '['
	if parameter != `{"scheduled":true}` {
		t.Fatal("JSON text parameter aliases the mutable byte buffer")
	}
}
