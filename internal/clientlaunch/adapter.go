package clientlaunch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
)

type Command struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
	Display    string   `json:"display"`
}

func BuildCommand(clientKind, profileName string, port int) (Command, error) {
	if !relayProfileNamePattern.MatchString(profileName) {
		return Command{}, errors.New("launch Profile name is invalid")
	}
	if port < 1 || port > 65535 {
		return Command{}, errors.New("launch relay port is invalid")
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/mcp?profile=%s", port, url.QueryEscape(profileName))
	switch clientKind {
	case "claude":
		payload := struct {
			MCPServers map[string]struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"mcpServers"`
		}{MCPServers: map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		}{"toolhub-relay": {Type: "http", URL: endpoint}}}
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		err := encoder.Encode(payload)
		if err != nil {
			return Command{}, err
		}
		body := bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})
		args := []string{"--strict-mcp-config", "--mcp-config", string(body)}
		return Command{Executable: "claude", Args: args, Display: "claude --strict-mcp-config --mcp-config '" + string(body) + "'"}, nil
	case "codex":
		configuration := fmt.Sprintf(`mcp_servers.toolhub-relay.url="%s"`, endpoint)
		args := []string{"--strict-config", "-c", configuration}
		return Command{Executable: "codex", Args: args, Display: "codex --strict-config -c '" + configuration + "'"}, nil
	default:
		return Command{}, errors.New("unsupported native client kind")
	}
}

var relayProfileNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
