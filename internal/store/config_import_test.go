package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestMCPContentHashMatchesCanonicalStoreShape(t *testing.T) {
	input, err := NormalizeMCPInput(MCPInput{
		Name: " Search ", Description: " description ", Transport: "stdio", Command: "/usr/bin/search",
	})
	if err != nil {
		t.Fatal(err)
	}
	envRefs := map[string]string{"TOKEN": "00000000-0000-4000-8000-000000000001"}
	headerRefs := map[string]string{"Authorization": "00000000-0000-4000-8000-000000000002"}
	canonical := struct {
		Name, Transport, Command, URL string
		Args                          []string
		EnvRefs, HeaderRefs           map[string]string
	}{input.Name, input.Transport, input.Command, input.URL, input.Args, envRefs, headerRefs}
	body, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if got, want := MCPContentHash(input, envRefs, headerRefs), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("MCP hash=%s want=%s", got, want)
	}
	if input.Name != "search" || input.Description != "description" || input.Args == nil {
		t.Fatalf("MCP normalization did not preserve the Store shape: %+v", input)
	}
}

func TestNormalizeMCPInputRejectsProtectedAndMixedDefinitions(t *testing.T) {
	for _, input := range []MCPInput{
		{Name: "toolhub-reserved", Transport: "stdio", Command: "tool"},
		{Name: ".hidden", Transport: "stdio", Command: "tool"},
		{Name: "mixed", Transport: "stdio", Command: "tool", URL: "https://example.test/mcp"},
		{Name: "network", Transport: "http", URL: "https://example.test/mcp", Args: []string{"unexpected"}},
	} {
		if _, err := NormalizeMCPInput(input); err == nil {
			t.Fatalf("invalid MCP input was accepted: %+v", input)
		}
	}
}
