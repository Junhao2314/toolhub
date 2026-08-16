package clientlaunch

import (
	"reflect"
	"testing"
)

func TestLaunchCommandUsesExactNativeArgv(t *testing.T) {
	profileName := "coding"
	tests := []struct {
		kind        string
		executable  string
		args        []string
		displayLine string
	}{
		{
			kind:        "claude",
			executable:  "claude",
			args:        []string{"--strict-mcp-config", "--mcp-config", `{"mcpServers":{"toolhub-relay":{"type":"http","url":"http://127.0.0.1:6276/mcp?profile=coding"}}}`},
			displayLine: `claude --strict-mcp-config --mcp-config '{"mcpServers":{"toolhub-relay":{"type":"http","url":"http://127.0.0.1:6276/mcp?profile=coding"}}}'`,
		},
		{
			kind:        "codex",
			executable:  "codex",
			args:        []string{"--strict-config", "-c", `mcp_servers.toolhub-relay.url="http://127.0.0.1:6276/mcp?profile=coding"`},
			displayLine: `codex --strict-config -c 'mcp_servers.toolhub-relay.url="http://127.0.0.1:6276/mcp?profile=coding"'`,
		},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			command, err := BuildCommand(test.kind, profileName, 6276)
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
		name        string
		kind        string
		profileName string
		port        int
	}{
		{name: "client", kind: "hermes", profileName: "coding", port: 6276},
		{name: "profile", kind: "claude", profileName: "profile'; touch /tmp/unsafe", port: 6276},
		{name: "port", kind: "codex", profileName: "coding", port: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if command, err := BuildCommand(test.kind, test.profileName, test.port); err == nil || command.Executable != "" || len(command.Args) != 0 || command.Display != "" {
				t.Fatalf("command=%+v err=%v", command, err)
			}
		})
	}
}
