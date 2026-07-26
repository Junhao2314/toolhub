package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

var errSharedMCPConcurrentChange = errors.New("shared MCP configuration changed during reconciliation")

type sharedMCPDocument struct {
	path          string
	format        string
	exists        bool
	mode          os.FileMode
	raw           []byte
	jsonRoot      map[string]any
	yamlDocument  yaml.Node
	yamlContainer *yaml.Node
	entries       map[string]any
}

func loadSharedMCPDocument(path, format string) (*sharedMCPDocument, error) {
	document := &sharedMCPDocument{path: path, format: format, entries: map[string]any{}}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newSharedMCPDocument(document), nil
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
	document.raw = body
	switch format {
	case MCPFormatClaudeSettingsJSON, MCPFormatCodexPluginJSON, MCPFormatOpenClawJSON:
		if err := json.Unmarshal(body, &document.jsonRoot); err != nil {
			return nil, errors.New("existing JSON MCP configuration cannot be parsed")
		}
		if document.jsonRoot == nil {
			return nil, errors.New("existing JSON MCP configuration root is not an object")
		}
		container, ok := document.jsonRoot["mcpServers"].(map[string]any)
		if !ok {
			if document.jsonRoot["mcpServers"] != nil {
				return nil, errors.New("existing mcpServers value is not an object")
			}
			container = map[string]any{}
			document.jsonRoot["mcpServers"] = container
		}
		for name, entry := range container {
			document.entries[name] = normalizeGenericValue(entry)
		}
	case MCPFormatHermesYAML:
		if err := yaml.Unmarshal(body, &document.yamlDocument); err != nil {
			return nil, errors.New("existing Hermes YAML configuration cannot be parsed")
		}
		if len(document.yamlDocument.Content) == 0 || document.yamlDocument.Content[0].Kind != yaml.MappingNode {
			return nil, errors.New("existing Hermes YAML root is not a mapping")
		}
		root := document.yamlDocument.Content[0]
		container := yamlMappingValue(root, "mcp_servers")
		if container == nil {
			container = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "mcp_servers"}, container)
		}
		if container.Kind != yaml.MappingNode {
			return nil, errors.New("existing Hermes mcp_servers value is not a mapping")
		}
		document.yamlContainer = container
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

func newSharedMCPDocument(document *sharedMCPDocument) *sharedMCPDocument {
	switch document.format {
	case MCPFormatClaudeSettingsJSON, MCPFormatCodexPluginJSON, MCPFormatOpenClawJSON:
		document.jsonRoot = map[string]any{"mcpServers": map[string]any{}}
	case MCPFormatHermesYAML:
		container := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "mcp_servers"}, container,
		}}
		document.yamlDocument = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
		document.yamlContainer = container
	}
	return document
}

func (document *sharedMCPDocument) entry(name string) (any, bool) {
	value, ok := document.entries[name]
	return value, ok
}

func (document *sharedMCPDocument) setEntry(name string, value map[string]any) error {
	value = normalizeGenericValue(value).(map[string]any)
	document.entries[name] = value
	switch document.format {
	case MCPFormatClaudeSettingsJSON, MCPFormatCodexPluginJSON, MCPFormatOpenClawJSON:
		container := document.jsonRoot["mcpServers"].(map[string]any)
		container[name] = value
	case MCPFormatHermesYAML:
		encoded := &yaml.Node{}
		if err := encoded.Encode(value); err != nil {
			return err
		}
		for index := 0; index+1 < len(document.yamlContainer.Content); index += 2 {
			if document.yamlContainer.Content[index].Value == name {
				document.yamlContainer.Content[index+1] = encoded
				return nil
			}
		}
		document.yamlContainer.Content = append(document.yamlContainer.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, encoded)
	}
	return nil
}

func (document *sharedMCPDocument) deleteEntry(name string) {
	delete(document.entries, name)
	switch document.format {
	case MCPFormatClaudeSettingsJSON, MCPFormatCodexPluginJSON, MCPFormatOpenClawJSON:
		delete(document.jsonRoot["mcpServers"].(map[string]any), name)
	case MCPFormatHermesYAML:
		for index := 0; index+1 < len(document.yamlContainer.Content); index += 2 {
			if document.yamlContainer.Content[index].Value == name {
				document.yamlContainer.Content = append(document.yamlContainer.Content[:index], document.yamlContainer.Content[index+2:]...)
				return
			}
		}
	}
}

func (document *sharedMCPDocument) encode() ([]byte, error) {
	switch document.format {
	case MCPFormatClaudeSettingsJSON, MCPFormatCodexPluginJSON, MCPFormatOpenClawJSON:
		return json.MarshalIndent(document.jsonRoot, "", "  ")
	case MCPFormatHermesYAML:
		return yaml.Marshal(&document.yamlDocument)
	default:
		return nil, errors.New("unsupported shared MCP renderer")
	}
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

func documentFingerprint(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func writeSharedMCPDocument(document *sharedMCPDocument, dataDir, sourceName, consumerKind string) error {
	body, err := document.encode()
	if err != nil {
		return err
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		body = append(body, '\n')
	}
	if err := os.MkdirAll(filepath.Dir(document.path), 0700); err != nil {
		return err
	}
	if document.exists {
		current, err := os.ReadFile(document.path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, document.raw) {
			return errSharedMCPConcurrentChange
		}
		backupRoot := filepath.Join(dataDir, "backups", "shared", safeStateName.ReplaceAllString(sourceName, "-"), consumerKind)
		if err := os.MkdirAll(backupRoot, 0700); err != nil {
			return err
		}
		backup := filepath.Join(backupRoot, time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+filepath.Base(document.path))
		if err := os.WriteFile(backup, document.raw, 0600); err != nil {
			return err
		}
		if err := pruneSharedBackups(backupRoot, 5); err != nil {
			return err
		}
	}
	temporary := filepath.Join(filepath.Dir(document.path), "."+filepath.Base(document.path)+".toolhub-new-"+uuid.NewString())
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Chmod(temporary, 0600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := replaceSharedMCPFile(temporary, document.path, document.exists); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	document.exists = true
	document.mode = 0600
	document.raw = append(document.raw[:0], body...)
	return syncDirectory(filepath.Dir(document.path))
}

func replaceSharedMCPFile(temporary, target string, targetExists bool) error {
	if goruntime.GOOS != "windows" || !targetExists {
		return os.Rename(temporary, target)
	}
	previous := target + ".toolhub-old-" + uuid.NewString()
	if err := os.Rename(target, previous); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Rename(previous, target)
		return err
	}
	_ = os.Remove(previous)
	return nil
}

func pruneSharedBackups(root string, keep int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) <= keep {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	for _, entry := range entries[keep:] {
		if entry.Type().IsRegular() {
			if err := os.Remove(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
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

func sortedEntryNames(entries map[string]any) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
