package runtime

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/skills"
)

type Paths struct {
	Home         string            `json:"home"`
	RuntimeRoots map[string]string `json:"runtimeRoots"`
}

func DefaultPaths(home string) Paths {
	return Paths{Home: home, RuntimeRoots: map[string]string{
		domain.RuntimeCodex:    filepath.Join(home, ".codex", "skills"),
		domain.RuntimeClaude:   filepath.Join(home, ".claude", "skills"),
		domain.RuntimeHermes:   filepath.Join(home, ".hermes", "skills"),
		domain.RuntimeGrok:     filepath.Join(home, ".grok", "skills"),
		domain.RuntimeOpenClaw: filepath.Join(home, ".openclaw", "workspace", "skills"),
	}}
}

func ScanAll(paths Paths) ([]domain.InventoryRuntime, error) {
	result, err := ScanAllWithKey(paths, nil)
	return result.Runtimes, err
}

type InventoryScan struct {
	Runtimes      []domain.InventoryRuntime
	SharedSources []domain.SharedSourceInventory
	MCPSecrets    map[string]MCPSecretValues
}

type MCPSecretValues struct {
	Env     map[string]string
	Headers map[string]string
}

func ScanAllWithKey(paths Paths, fingerprintKey []byte) (InventoryScan, error) {
	return ScanAllConfigured(paths, nil, "", fingerprintKey)
}

func ScanAllConfigured(paths Paths, sharedSources []SharedSourceConfig, dataDir string, fingerprintKey []byte) (InventoryScan, error) {
	if paths.Home == "" {
		return InventoryScan{}, errors.New("home directory is required")
	}
	result := InventoryScan{MCPSecrets: map[string]MCPSecretValues{}}
	for _, kind := range []string{domain.RuntimeCodex, domain.RuntimeClaude, domain.RuntimeHermes, domain.RuntimeGrok, domain.RuntimeOpenClaw} {
		root := paths.RuntimeRoots[kind]
		if root == "" {
			root = DefaultPaths(paths.Home).RuntimeRoots[kind]
		}
		inventory := map[string]any{"skills": []any{}, "symlinks": []any{}, "managed": 0, "protected": 0}
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			skills, symlinks, managed, protected, err := scanSkillRoot(root)
			if err != nil {
				return InventoryScan{}, err
			}
			inventory["skills"] = skills
			inventory["symlinks"] = symlinks
			inventory["managed"] = managed
			inventory["protected"] = protected
		}
		config, descriptors, secrets := scanRuntimeConfig(paths.Home, kind, fingerprintKey)
		for identity, values := range secrets {
			result.MCPSecrets[identity] = values
		}
		if kind == domain.RuntimeHermes {
			inventory["hubLock"] = readJSONRedacted(filepath.Join(paths.Home, ".hermes", ".hub", "lock.json"))
			inventory["taps"] = listNames(filepath.Join(paths.Home, ".hermes", "taps"))
		}
		result.Runtimes = append(result.Runtimes, domain.InventoryRuntime{Kind: kind, RootPath: root, Version: "", Config: config, Inventory: inventory, MCPServers: descriptors})
	}
	if len(sharedSources) > 0 {
		result.SharedSources = ScanSharedSources(sharedSources, dataDir, fingerprintKey)
	}
	return result, nil
}

func scanSkillRoot(root string) ([]any, []any, int, int, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	var discoveries []any
	var symlinks []any
	managed, protected := 0, 0
	err = filepath.WalkDir(rootAbs, func(current string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		relative, _ := filepath.Rel(rootAbs, current)
		depth := strings.Count(filepath.ToSlash(relative), "/")
		if depth > 6 && item.IsDir() {
			return filepath.SkipDir
		}
		info, err := item.Info()
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(current)
			resolved, resolveErr := filepath.EvalSymlinks(current)
			escaped := resolveErr != nil || (resolved != rootAbs && !strings.HasPrefix(resolved, rootAbs+string(filepath.Separator)))
			symlinks = append(symlinks, map[string]any{"path": relative, "target": target, "broken": readErr != nil || resolveErr != nil, "escaped": escaped})
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if item.Name() != "SKILL.md" {
			return nil
		}
		directory := filepath.Dir(current)
		marker := filepath.Join(directory, ".toolhub-managed.json")
		isManaged := fileExists(marker)
		isProtected := strings.Contains(filepath.ToSlash(directory), "/.system/") || filepath.Base(directory) == ".system"
		if isManaged {
			managed++
		}
		if isProtected {
			protected++
		}
		hash := ""
		if pkg, scanErr := skills.ScanDirectory(directory, skills.DefaultLimits); scanErr == nil {
			hash = pkg.SHA256
		}
		discoveries = append(discoveries, map[string]any{"name": filepath.Base(directory), "path": directory, "managed": isManaged, "protected": isProtected, "disabled": strings.Contains(filepath.ToSlash(directory), "/.toolhub-disabled/"), "sha256": hash})
		return nil
	})
	return discoveries, symlinks, managed, protected, err
}

func scanRuntimeConfig(home, kind string, fingerprintKey []byte) (map[string]any, []domain.MCPDescriptor, map[string]MCPSecretValues) {
	paths := map[string][]string{
		domain.RuntimeCodex: {
			filepath.Join(home, ".codex", "config.toml"),
			filepath.Join(home, ".codex", ".tmp", "plugins", "plugins", "shared-mcp", ".mcp.json"),
		},
		domain.RuntimeClaude:   {filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "settings.json")},
		domain.RuntimeHermes:   {filepath.Join(home, ".hermes", "config.yaml")},
		domain.RuntimeGrok:     {},
		domain.RuntimeOpenClaw: {filepath.Join(home, ".openclaw", "workspace", "config", "mcporter.json")},
	}
	result := map[string]any{"platform": runtime.GOOS, "configFiles": []any{}}
	if kind == domain.RuntimeGrok {
		result["mcpInheritedFrom"] = domain.RuntimeClaude
	}
	var files []any
	descriptorsByName := map[string]domain.MCPDescriptor{}
	secrets := map[string]MCPSecretValues{}
	for _, candidate := range paths[kind] {
		if info, err := os.Stat(candidate); err == nil {
			files = append(files, map[string]any{"path": candidate, "size": info.Size(), "modifiedAt": info.ModTime()})
			for name, rawConfig := range readMCPConfig(candidate, kind) {
				config, ok := rawConfig.(map[string]any)
				if !ok {
					continue
				}
				descriptor, values, err := normalizeMCPServer(kind, name, config, fingerprintKey)
				if err != nil {
					continue
				}
				descriptorsByName[name] = descriptor
				secrets[descriptor.Identity] = values
			}
		}
	}
	names := make([]string, 0, len(descriptorsByName))
	for name := range descriptorsByName {
		names = append(names, name)
	}
	sort.Strings(names)
	descriptors := make([]domain.MCPDescriptor, 0, len(names))
	summaries := make(map[string]any, len(names))
	for _, name := range names {
		descriptor := descriptorsByName[name]
		descriptors = append(descriptors, descriptor)
		summaries[name] = map[string]any{"transport": descriptor.Transport, "command": descriptor.Command, "args": descriptor.Args, "url": descriptor.URL, "envKeys": descriptor.EnvKeys, "headerKeys": descriptor.HeaderKeys}
	}
	result["configFiles"] = files
	result["mcpServers"] = summaries
	result["mcpm"] = readJSONRedacted(filepath.Join(home, ".config", "mcpm", "config.json"))
	return result, descriptors, secrets
}

func normalizeMCPServer(kind, name string, config map[string]any, fingerprintKey []byte) (domain.MCPDescriptor, MCPSecretValues, error) {
	descriptor := domain.MCPDescriptor{Name: name, Command: stringValue(config["command"]), URL: stringValue(config["url"]), Args: stringSlice(config["args"])}
	if descriptor.URL == "" {
		descriptor.URL = stringValue(config["baseUrl"])
	}
	descriptor.Transport = strings.ToLower(stringValue(config["transport"]))
	if descriptor.Transport == "" {
		descriptor.Transport = strings.ToLower(stringValue(config["type"]))
	}
	if descriptor.Transport == "http" {
		descriptor.Transport = "streamable-http"
	}
	if descriptor.Transport == "streamablehttp" {
		descriptor.Transport = "streamable-http"
	}
	if descriptor.Transport == "" {
		if descriptor.Command != "" {
			descriptor.Transport = "stdio"
		} else if descriptor.URL != "" {
			descriptor.Transport = "streamable-http"
		}
	}
	environment := stringMap(config["env"])
	if len(environment) == 0 {
		environment = stringMap(config["environment"])
	}
	for key := range environment {
		descriptor.EnvKeys = append(descriptor.EnvKeys, key)
	}
	headers := stringMap(config["headers"])
	canonicalHeaders := make(map[string]string, len(headers))
	for key := range headers {
		canonical, err := protocol.NormalizeHeaderName(key)
		if err != nil {
			return domain.MCPDescriptor{}, MCPSecretValues{}, err
		}
		if _, exists := canonicalHeaders[canonical]; exists {
			return domain.MCPDescriptor{}, MCPSecretValues{}, errors.New("duplicate MCP HTTP header name")
		}
		canonicalHeaders[canonical] = headers[key]
		descriptor.HeaderKeys = append(descriptor.HeaderKeys, canonical)
	}
	headers = canonicalHeaders
	var err error
	descriptor, err = protocol.NormalizeMCPDescriptor(kind, descriptor)
	if err != nil {
		return domain.MCPDescriptor{}, MCPSecretValues{}, err
	}
	if len(fingerprintKey) > 0 && (len(environment) > 0 || len(headers) > 0) {
		descriptor.SecretFingerprint = security.FingerprintSecretMap(fingerprintKey, combinedSecretValues(environment, headers))
	}
	return descriptor, MCPSecretValues{Env: environment, Headers: headers}, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func stringMap(value any) map[string]string {
	result := map[string]string{}
	typed, ok := value.(map[string]any)
	if !ok {
		return result
	}
	for key, raw := range typed {
		if text, ok := raw.(string); ok {
			result[key] = text
		}
	}
	return result
}

func readMCPConfig(configPath, kind string) map[string]any {
	body, err := os.ReadFile(configPath)
	if err != nil || len(body) > 2<<20 {
		return nil
	}
	value := map[string]any{}
	switch strings.ToLower(filepath.Ext(configPath)) {
	case ".json":
		err = json.Unmarshal(body, &value)
	case ".toml":
		err = toml.Unmarshal(body, &value)
	case ".yaml", ".yml":
		err = yaml.Unmarshal(body, &value)
	default:
		return nil
	}
	if err != nil {
		return nil
	}
	keys := []string{"mcpServers", "mcp_servers", "servers"}
	if kind == domain.RuntimeCodex || kind == domain.RuntimeHermes {
		keys = []string{"mcp_servers", "mcpServers"}
	}
	for _, key := range keys {
		if servers, ok := value[key].(map[string]any); ok {
			return servers
		}
	}
	return nil
}

func readJSONRedacted(path string) any {
	body, err := os.ReadFile(path)
	if err != nil || len(body) > 1<<20 {
		return nil
	}
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		return map[string]any{"present": true, "parseError": true}
	}
	return security.RedactMap(value)
}

func listNames(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Name())
	}
	sort.Strings(result)
	return result
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
