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

func TestSyncSharedPayloadSigningIsCanonical(t *testing.T) {
	first := json.RawMessage(`{"sourceName":"root-shared","sourceId":"source-1","scopes":["skills","mcp"],"dryRun":true}`)
	second := json.RawMessage(`{"dryRun":true,"scopes":["skills","mcp"],"sourceId":"source-1","sourceName":"root-shared"}`)
	a, err := TaskSigningBytes("task-1", "sync_shared", first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := TaskSigningBytes("task-1", "sync_shared", second)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("sync_shared signing bytes differ: %q != %q", a, b)
	}
	if len(a) == 0 {
		t.Fatal("empty signing bytes")
	}
}
