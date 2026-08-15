package clientlaunch

import (
	"reflect"
	"testing"
)

func TestLaunchCommandUsesExactNativeArgv(t *testing.T) {
	profileID := "00000000-0000-0000-0000-000000000123"
	tests := []struct {
		kind        string
		executable  string
		args        []string
		displayLine string
	}{
		{
			kind:        "claude",
			executable:  "claude",
			args:        []string{"--strict-mcp-config", "--mcp-config", `{"mcpServers":{"toolhub-relay":{"type":"http","url":"http://127.0.0.1:6276/mcp?toolhub_profile=00000000-0000-0000-0000-000000000123&toolhub_client=claude"}}}`},
			displayLine: `claude --strict-mcp-config --mcp-config '{"mcpServers":{"toolhub-relay":{"type":"http","url":"http://127.0.0.1:6276/mcp?toolhub_profile=00000000-0000-0000-0000-000000000123&toolhub_client=claude"}}}'`,
		},
		{
			kind:        "codex",
			executable:  "codex",
			args:        []string{"--strict-config", "-c", `mcp_servers.toolhub-relay.url="http://127.0.0.1:6276/mcp?toolhub_profile=00000000-0000-0000-0000-000000000123&toolhub_client=codex"`},
			displayLine: `codex --strict-config -c 'mcp_servers.toolhub-relay.url="http://127.0.0.1:6276/mcp?toolhub_profile=00000000-0000-0000-0000-000000000123&toolhub_client=codex"'`,
		},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			command, err := BuildCommand(test.kind, profileID, 6276)
			if err != nil {
				t.Fatal(err)
			}
			if command.Executable != test.executable || !reflect.DeepEqual(command.Args, test.args) || command.Display != test.displayLine {
				t.Fatalf("command=%+v", command)
			}
		})
	}
}

func TestLaunchCommandRejectsUntrustedRoutingInputs(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		profileID string
		port      int
	}{
		{name: "client", kind: "hermes", profileID: "00000000-0000-0000-0000-000000000123", port: 6276},
		{name: "profile", kind: "claude", profileID: "profile'; touch /tmp/unsafe", port: 6276},
		{name: "port", kind: "codex", profileID: "00000000-0000-0000-0000-000000000123", port: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if command, err := BuildCommand(test.kind, test.profileID, test.port); err == nil || command.Executable != "" || len(command.Args) != 0 || command.Display != "" {
				t.Fatalf("command=%+v err=%v", command, err)
			}
		})
	}
}
