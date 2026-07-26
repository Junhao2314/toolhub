package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
)

func TestDecodeAgentTaskPreservesTypedApplyMCPPayload(t *testing.T) {
	payload, err := json.Marshal(protocol.ApplyMCPPayload{
		Runtime:     domain.RuntimeClaude,
		ProfileID:   "profile-1",
		ProfileName: "toolhub-claude",
		MCPMProfile: "toolhub-claude",
		Enabled:     true,
		Servers:     []protocol.MCPServerRef{{Name: "remote", Transport: "streamable-http", URL: "https://example.test/mcp"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(domain.AgentTask{ID: "task-1", Kind: "apply_mcp", Payload: payload, Signature: "signed"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := decodeAgentTask(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	var decoded protocol.ApplyMCPPayload
	if err := json.Unmarshal(task.Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if task.Kind != "apply_mcp" || decoded.MCPMProfile != "toolhub-claude" || len(decoded.Servers) != 1 || decoded.Servers[0].Transport != "streamable-http" {
		t.Fatalf("typed SSH/run-task payload changed: task=%+v payload=%+v", task, decoded)
	}
}
