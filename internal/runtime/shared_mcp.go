package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type sharedMCPDocument struct {
	exists  bool
	mode    os.FileMode
	entries map[string]any
}

func loadSharedMCPDocument(path, format string) (*sharedMCPDocument, error) {
	document := &sharedMCPDocument{entries: map[string]any{}}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return nil, err
	}
	if len(body) > sharedManifestMaxBytes {
		return nil, errors.New("consumer MCP configuration exceeds 2 MiB")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	document.exists = true
	document.mode = info.Mode().Perm()
	switch format {
	case MCPFormatClaudeSettingsJSON, MCPFormatCodexPluginJSON, MCPFormatOpenClawJSON:
		var root map[string]any
		if err := json.Unmarshal(body, &root); err != nil {
			return nil, errors.New("existing JSON MCP configuration cannot be parsed")
		}
		if root == nil {
			return nil, errors.New("existing JSON MCP configuration root is not an object")
		}
		container, ok := root["mcpServers"].(map[string]any)
		if !ok {
			if root["mcpServers"] != nil {
				return nil, errors.New("existing mcpServers value is not an object")
			}
			container = map[string]any{}
		}
		for name, entry := range container {
			document.entries[name] = normalizeGenericValue(entry)
		}
	case MCPFormatHermesYAML:
		var yamlDocument yaml.Node
		if err := yaml.Unmarshal(body, &yamlDocument); err != nil {
			return nil, errors.New("existing Hermes YAML configuration cannot be parsed")
		}
		if len(yamlDocument.Content) == 0 || yamlDocument.Content[0].Kind != yaml.MappingNode {
			return nil, errors.New("existing Hermes YAML root is not a mapping")
		}
		root := yamlDocument.Content[0]
		container := yamlMappingValue(root, "mcp_servers")
		if container == nil {
			return document, nil
		}
		if container.Kind != yaml.MappingNode {
			return nil, errors.New("existing Hermes mcp_servers value is not a mapping")
		}
		for index := 0; index+1 < len(container.Content); index += 2 {
			name := container.Content[index].Value
			var entry any
			if err := container.Content[index+1].Decode(&entry); err != nil {
				return nil, errors.New("existing Hermes MCP entry cannot be decoded")
			}
			document.entries[name] = normalizeGenericValue(entry)
		}
	default:
		return nil, fmt.Errorf("unsupported shared MCP renderer %q", format)
	}
	return document, nil
}

func (document *sharedMCPDocument) entry(name string) (any, bool) {
	value, ok := document.entries[name]
	return value, ok
}

func renderSharedMCPEntry(format string, server SharedMCPServer) map[string]any {
	descriptor := server.Descriptor
	entry := map[string]any{}
	switch format {
	case MCPFormatOpenClawJSON:
		entry["description"] = server.Description
		entry["isActive"] = true
		if descriptor.Transport == "stdio" {
			entry["type"] = "stdio"
			entry["command"] = descriptor.Command
			if len(descriptor.Args) > 0 {
				entry["args"] = append([]string(nil), descriptor.Args...)
			}
			if len(server.Env) > 0 {
				entry["env"] = cloneStringMap(server.Env)
			}
		} else {
			entry["type"] = "streamableHttp"
			entry["baseUrl"] = descriptor.URL
			if len(server.Headers) > 0 {
				entry["headers"] = cloneStringMap(server.Headers)
			}
			if authorization := server.Headers["Authorization"]; authorization != "" {
				entry["command"] = "npx"
				entry["args"] = []string{"-y", "mcp-remote", descriptor.URL, "--header", "Authorization: " + authorization}
			}
		}
	default:
		if descriptor.Transport == "stdio" {
			entry["command"] = descriptor.Command
			if len(descriptor.Args) > 0 {
				entry["args"] = append([]string(nil), descriptor.Args...)
			}
		} else {
			if format == MCPFormatCodexPluginJSON {
				entry["type"] = "http"
			}
			entry["url"] = descriptor.URL
		}
		if len(server.Headers) > 0 {
			entry["headers"] = cloneStringMap(server.Headers)
		}
		if len(server.Env) > 0 || format == MCPFormatClaudeSettingsJSON {
			entry["env"] = cloneStringMap(server.Env)
		}
	}
	return normalizeGenericValue(entry).(map[string]any)
}

func entryFingerprint(value any) string {
	body, _ := json.Marshal(normalizeGenericValue(value))
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func normalizeGenericValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalizeGenericValue(item)
		}
		return result
	case map[any]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[fmt.Sprint(key)] = normalizeGenericValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeGenericValue(item)
		}
		return result
	case nil:
		return nil
	default:
		return typed
	}
}

func cleanLinkTarget(linkPath, target string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(linkPath), target))
}

func samePath(first, second string) bool {
	return filepath.Clean(strings.TrimSpace(first)) == filepath.Clean(strings.TrimSpace(second))
}
