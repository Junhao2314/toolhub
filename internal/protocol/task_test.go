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

func TestApplyMCPPayloadSigningIsCanonical(t *testing.T) {
	first := json.RawMessage(`{"profileId":"profile-1","mcpmProfile":"toolhub-codex","servers":[{"name":"memory"}],"dryRun":true}`)
	second := json.RawMessage(`{"dryRun":true,"servers":[{"name":"memory"}],"mcpmProfile":"toolhub-codex","profileId":"profile-1"}`)
	a, err := TaskSigningBytes("task-1", "apply_mcp", first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := TaskSigningBytes("task-1", "apply_mcp", second)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("apply_mcp signing bytes differ: %q != %q", a, b)
	}
	if len(a) == 0 {
		t.Fatal("empty signing bytes")
	}
}
