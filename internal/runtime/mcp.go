package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

type SecretResolver func(context.Context, string) (string, error)

func ApplyMCP(ctx context.Context, paths Paths, dataDir, runtimeKind string, profile map[string]any, resolve SecretResolver) (map[string]any, error) {
	servers, _ := profile["servers"].([]any)
	resolved := map[string]any{}
	for _, raw := range servers {
		server, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := server["name"].(string)
		if name == "" {
			return nil, errors.New("MCP server name is required")
		}
		config := map[string]any{}
		for key, value := range server {
			if key != "id" && key != "name" && key != "envRefs" && key != "headerRefs" && key != "overrides" {
				config[key] = value
			}
		}
		environment := map[string]any{}
		if refs, ok := server["envRefs"].(map[string]any); ok {
			for envName, rawID := range refs {
				secretID, _ := rawID.(string)
				value, err := resolve(ctx, secretID)
				if err != nil {
					return nil, fmt.Errorf("resolve MCP secret reference: %w", err)
				}
				environment[envName] = value
			}
		}
		config["env"] = environment
		headers := map[string]any{}
		if refs, ok := server["headerRefs"].(map[string]any); ok {
			for headerName, rawID := range refs {
				secretID, _ := rawID.(string)
				value, err := resolve(ctx, secretID)
				if err != nil {
					return nil, fmt.Errorf("resolve MCP header reference: %w", err)
				}
				headers[headerName] = value
			}
		}
		if len(headers) > 0 {
			config["headers"] = headers
		}
		resolved[name] = config
	}
	payload := map[string]any{"mcpServers": resolved}
	if used, err := tryMCPM(ctx, runtimeKind, payload); used && err == nil {
		return map[string]any{"method": "mcpm", "serverCount": len(resolved)}, nil
	}
	configPath, format := mcpConfigPath(paths.Home, runtimeKind)
	if configPath == "" {
		return nil, errors.New("unsupported MCP runtime")
	}
	if err := structuralPatch(configPath, format, runtimeKind, resolved, dataDir); err != nil {
		return nil, err
	}
	return map[string]any{"method": "structured-config", "serverCount": len(resolved), "configPath": configPath}, nil
}

func tryMCPM(ctx context.Context, runtimeKind string, payload map[string]any) (bool, error) {
	binary, err := exec.LookPath("mcpm")
	if err != nil {
		return false, nil
	}
	temporary, err := os.CreateTemp("", "toolhub-mcpm-*.json")
	if err != nil {
		return true, err
	}
	defer os.Remove(temporary.Name())
	encoded, _ := json.Marshal(payload)
	if _, err := temporary.Write(encoded); err != nil {
		return true, err
	}
	_ = temporary.Close()
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, binary, "profile", "apply", "--runtime", runtimeKind, "--file", temporary.Name())
	output, err := command.CombinedOutput()
	if err != nil {
		return true, fmt.Errorf("mcpm apply failed: %s", strings.TrimSpace(string(output)))
	}
	return true, nil
}

func mcpConfigPath(home, runtimeKind string) (string, string) {
	switch runtimeKind {
	case "codex":
		return filepath.Join(home, ".codex", "config.toml"), "toml"
	case "claude":
		return filepath.Join(home, ".claude.json"), "json"
	case "grok":
		return filepath.Join(home, ".claude.json"), "json"
	case "hermes":
		return filepath.Join(home, ".hermes", "config.yaml"), "yaml"
	case "openclaw":
		return filepath.Join(home, ".openclaw", "workspace", "config", "mcporter.json"), "json"
	default:
		return "", ""
	}
}

func structuralPatch(configPath, format, runtimeKind string, servers map[string]any, dataDir string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return err
	}
	value := map[string]any{}
	if body, err := os.ReadFile(configPath); err == nil {
		switch format {
		case "json":
			if err := json.Unmarshal(body, &value); err != nil {
				return errors.New("existing JSON MCP config cannot be parsed")
			}
		case "toml":
			if err := toml.Unmarshal(body, &value); err != nil {
				return errors.New("existing TOML MCP config cannot be parsed")
			}
		case "yaml":
			if err := yaml.Unmarshal(body, &value); err != nil {
				return errors.New("existing YAML MCP config cannot be parsed")
			}
		}
		backup := filepath.Join(dataDir, "backups", "mcp", runtimeKind, time.Now().UTC().Format("20060102T150405Z")+"-"+filepath.Base(configPath))
		if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(backup, body, 0600); err != nil {
			return err
		}
	}
	key := "mcpServers"
	if runtimeKind == "codex" || runtimeKind == "hermes" {
		key = "mcp_servers"
	}
	value[key] = servers
	var encoded []byte
	var err error
	switch format {
	case "json":
		encoded, err = json.MarshalIndent(value, "", "  ")
	case "toml":
		encoded, err = toml.Marshal(value)
	case "yaml":
		encoded, err = yaml.Marshal(value)
	}
	if err != nil {
		return err
	}
	temporary := configPath + ".toolhub-new-" + uuid.NewString()
	if err := os.WriteFile(temporary, encoded, 0600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" && fileExists(configPath) {
		old := configPath + ".toolhub-old-" + uuid.NewString()
		if err := os.Rename(configPath, old); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		if err := os.Rename(temporary, configPath); err != nil {
			_ = os.Rename(old, configPath)
			return err
		}
		_ = os.Remove(old)
		return nil
	}
	return os.Rename(temporary, configPath)
}
