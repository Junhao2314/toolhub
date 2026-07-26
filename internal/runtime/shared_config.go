package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Junhao2314/toolhub/internal/domain"
)

const (
	SharedModeObserved = "observed"
	SharedModeManaged  = "managed"

	MCPFormatClaudeSettingsJSON = "claude-settings-json"
	MCPFormatCodexPluginJSON    = "codex-plugin-json"
	MCPFormatHermesYAML         = "hermes-yaml"
	MCPFormatOpenClawJSON       = "openclaw-mcporter-json"
)

var sharedSourceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

type SharedSourceConfig struct {
	Name              string                          `json:"name"`
	Mode              string                          `json:"mode"`
	AutoSync          bool                            `json:"autoSync"`
	SkillsRoot        string                          `json:"skillsRoot"`
	MCPManifest       string                          `json:"mcpManifest"`
	AllowedSkillRoots []string                        `json:"allowedSkillRoots"`
	Consumers         map[string]SharedConsumerConfig `json:"consumers"`
}

type SharedConsumerConfig struct {
	SkillsPath  string `json:"skillsPath,omitempty"`
	MCPPath     string `json:"mcpPath,omitempty"`
	MCPFormat   string `json:"mcpFormat,omitempty"`
	MCPInherits string `json:"mcpInherits,omitempty"`
}

func DefaultSharedSource(paths Paths) SharedSourceConfig {
	home := filepath.Clean(paths.Home)
	name := strings.Trim(strings.ToLower(filepath.Base(home)), ".-_ ")
	if name == "" || name == string(filepath.Separator) {
		name = "home"
	}
	name += "-shared"
	return SharedSourceConfig{
		Name:        name,
		Mode:        SharedModeObserved,
		SkillsRoot:  filepath.Join(home, ".shared", "skills"),
		MCPManifest: filepath.Join(home, ".shared", "mcp", "servers.json"),
		AllowedSkillRoots: []string{
			filepath.Join(home, ".shared", "skills"),
			filepath.Join(home, ".agents", "skills"),
			filepath.Join(home, ".shared", "vibe-skills"),
		},
		Consumers: map[string]SharedConsumerConfig{
			domain.RuntimeCodex: {
				SkillsPath: filepath.Join(home, ".codex", "skills"),
				MCPPath:    filepath.Join(home, ".codex", ".tmp", "plugins", "plugins", "shared-mcp", ".mcp.json"),
				MCPFormat:  MCPFormatCodexPluginJSON,
			},
			domain.RuntimeClaude: {
				SkillsPath: filepath.Join(home, ".claude", "skills"),
				MCPPath:    filepath.Join(home, ".claude", "settings.json"),
				MCPFormat:  MCPFormatClaudeSettingsJSON,
			},
			domain.RuntimeHermes: {
				SkillsPath: filepath.Join(home, ".hermes", "skills"),
				MCPPath:    filepath.Join(home, ".hermes", "config.yaml"),
				MCPFormat:  MCPFormatHermesYAML,
			},
			domain.RuntimeGrok: {
				SkillsPath:  filepath.Join(home, ".grok", "skills"),
				MCPInherits: domain.RuntimeClaude,
			},
			domain.RuntimeOpenClaw: {
				SkillsPath: filepath.Join(home, ".openclaw", "workspace", "skills"),
				MCPPath:    filepath.Join(home, ".openclaw", "workspace", "config", "mcporter.json"),
				MCPFormat:  MCPFormatOpenClawJSON,
			},
		},
	}
}

func AutoProbeSharedSources(paths Paths) []SharedSourceConfig {
	candidate := DefaultSharedSource(paths)
	if directoryExists(candidate.SkillsRoot) || fileExists(candidate.MCPManifest) {
		return []SharedSourceConfig{candidate}
	}
	return nil
}

func NormalizeSharedSources(paths Paths, sources []SharedSourceConfig) ([]SharedSourceConfig, error) {
	if strings.TrimSpace(paths.Home) == "" {
		return nil, errors.New("home directory is required")
	}
	seen := map[string]bool{}
	result := make([]SharedSourceConfig, 0, len(sources))
	for _, source := range sources {
		normalized, err := normalizeSharedSource(paths, source)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(normalized.Name)
		if seen[key] {
			return nil, fmt.Errorf("duplicate shared source name %q", normalized.Name)
		}
		seen[key] = true
		result = append(result, normalized)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func normalizeSharedSource(paths Paths, source SharedSourceConfig) (SharedSourceConfig, error) {
	source.Name = strings.TrimSpace(source.Name)
	if !sharedSourceNamePattern.MatchString(source.Name) {
		return SharedSourceConfig{}, errors.New("shared source name must contain 1-100 letters, numbers, dots, underscores, or hyphens")
	}
	if source.Mode == "" {
		source.Mode = SharedModeObserved
	}
	if source.Mode != SharedModeObserved && source.Mode != SharedModeManaged {
		return SharedSourceConfig{}, fmt.Errorf("shared source %q has invalid mode", source.Name)
	}
	if source.AutoSync && source.Mode != SharedModeManaged {
		return SharedSourceConfig{}, fmt.Errorf("shared source %q enables autoSync outside managed mode", source.Name)
	}
	defaults := DefaultSharedSource(paths)
	if source.SkillsRoot == "" {
		source.SkillsRoot = defaults.SkillsRoot
	}
	if source.MCPManifest == "" {
		source.MCPManifest = defaults.MCPManifest
	}
	var err error
	if source.SkillsRoot, err = cleanAbsolutePath(source.SkillsRoot); err != nil {
		return SharedSourceConfig{}, fmt.Errorf("shared source %q skillsRoot: %w", source.Name, err)
	}
	if source.MCPManifest, err = cleanAbsolutePath(source.MCPManifest); err != nil {
		return SharedSourceConfig{}, fmt.Errorf("shared source %q mcpManifest: %w", source.Name, err)
	}
	if len(source.AllowedSkillRoots) == 0 {
		source.AllowedSkillRoots = defaults.AllowedSkillRoots
	}
	allowed := make([]string, 0, len(source.AllowedSkillRoots)+1)
	allowed = append(allowed, source.SkillsRoot)
	for _, root := range source.AllowedSkillRoots {
		root, err = cleanAbsolutePath(root)
		if err != nil {
			return SharedSourceConfig{}, fmt.Errorf("shared source %q allowedSkillRoots: %w", source.Name, err)
		}
		allowed = append(allowed, root)
	}
	source.AllowedSkillRoots = uniqueStrings(allowed)
	if source.Consumers == nil {
		source.Consumers = defaults.Consumers
	}
	consumers := make(map[string]SharedConsumerConfig, len(source.Consumers))
	skillsPaths := map[string]string{}
	mcpPaths := map[string]string{}
	for kind, consumer := range source.Consumers {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if !domain.IsConsumerRuntime(kind) {
			return SharedSourceConfig{}, fmt.Errorf("shared source %q has invalid consumer %q", source.Name, kind)
		}
		if consumer.SkillsPath != "" {
			consumer.SkillsPath, err = cleanAbsolutePath(consumer.SkillsPath)
			if err != nil {
				return SharedSourceConfig{}, fmt.Errorf("shared source %q consumer %q skillsPath: %w", source.Name, kind, err)
			}
			if pathsOverlap(consumer.SkillsPath, source.SkillsRoot) {
				return SharedSourceConfig{}, fmt.Errorf("shared source %q consumer %q overlaps the source root", source.Name, kind)
			}
			if previous := skillsPaths[consumer.SkillsPath]; previous != "" {
				return SharedSourceConfig{}, fmt.Errorf("shared source %q consumers %q and %q use the same Skills path", source.Name, previous, kind)
			}
			skillsPaths[consumer.SkillsPath] = kind
		}
		if consumer.MCPPath != "" {
			consumer.MCPPath, err = cleanAbsolutePath(consumer.MCPPath)
			if err != nil {
				return SharedSourceConfig{}, fmt.Errorf("shared source %q consumer %q mcpPath: %w", source.Name, kind, err)
			}
			if samePath(consumer.MCPPath, source.MCPManifest) {
				return SharedSourceConfig{}, fmt.Errorf("shared source %q consumer %q points at the MCP manifest", source.Name, kind)
			}
			if previous := mcpPaths[consumer.MCPPath]; previous != "" {
				return SharedSourceConfig{}, fmt.Errorf("shared source %q consumers %q and %q use the same MCP path", source.Name, previous, kind)
			}
			mcpPaths[consumer.MCPPath] = kind
		}
		consumer.MCPInherits = strings.ToLower(strings.TrimSpace(consumer.MCPInherits))
		if consumer.MCPInherits != "" {
			if !domain.IsConsumerRuntime(consumer.MCPInherits) || consumer.MCPInherits == kind {
				return SharedSourceConfig{}, fmt.Errorf("shared source %q consumer %q has invalid MCP inheritance", source.Name, kind)
			}
		}
		if err := validateMCPFormat(kind, consumer); err != nil {
			return SharedSourceConfig{}, fmt.Errorf("shared source %q consumer %q: %w", source.Name, kind, err)
		}
		consumers[kind] = consumer
	}
	for kind, consumer := range consumers {
		if consumer.MCPInherits == "" {
			continue
		}
		parent, ok := consumers[consumer.MCPInherits]
		if !ok || parent.MCPPath == "" || parent.MCPInherits != "" {
			return SharedSourceConfig{}, fmt.Errorf("shared source %q consumer %q inherits from a consumer without a concrete MCP output", source.Name, kind)
		}
	}
	source.Consumers = consumers
	return source, nil
}

func validateMCPFormat(kind string, consumer SharedConsumerConfig) error {
	if consumer.MCPInherits != "" {
		if consumer.MCPPath != "" || consumer.MCPFormat != "" {
			return errors.New("inherited MCP consumers cannot also define an output path")
		}
		return nil
	}
	if consumer.MCPPath == "" && consumer.MCPFormat == "" {
		return nil
	}
	valid := map[string]string{
		domain.RuntimeCodex:    MCPFormatCodexPluginJSON,
		domain.RuntimeClaude:   MCPFormatClaudeSettingsJSON,
		domain.RuntimeHermes:   MCPFormatHermesYAML,
		domain.RuntimeOpenClaw: MCPFormatOpenClawJSON,
	}
	if consumer.MCPPath == "" || consumer.MCPFormat != valid[kind] {
		return errors.New("MCP path and renderer format do not match the consumer")
	}
	return nil
}

func SharedSourceConfigFingerprint(source SharedSourceConfig) string {
	body, _ := json.Marshal(source)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func FindSharedSource(sources []SharedSourceConfig, name string) (SharedSourceConfig, error) {
	for _, source := range sources {
		if source.Name == name {
			return source, nil
		}
	}
	return SharedSourceConfig{}, fmt.Errorf("shared source %q is not configured", name)
}

func cleanAbsolutePath(value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if value == "." || !filepath.IsAbs(value) {
		return "", errors.New("path must be absolute")
	}
	return value, nil
}

func pathsOverlap(first, second string) bool {
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = filepath.Clean(value)
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
