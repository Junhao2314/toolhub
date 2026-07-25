package protocol

import (
	"encoding/json"
	"testing"
)

func TestTaskSigningBytesCanonicalizesJSON(t *testing.T) {
	a, err := TaskSigningBytes("id", "scan", json.RawMessage(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := TaskSigningBytes("id", "scan", json.RawMessage(`{ "a": 1, "b": 2 }`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("semantic JSON produced different signing bytes: %q != %q", a, b)
	}
}
