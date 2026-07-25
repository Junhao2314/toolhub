package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"

	"github.com/toolhub-dev/toolhub/internal/domain"
	"github.com/toolhub-dev/toolhub/internal/security"
)

type Paths struct {
	Home         string            `json:"home"`
	RuntimeRoots map[string]string `json:"runtimeRoots"`
}

func DefaultPaths(home string) Paths {
	return Paths{Home: home, RuntimeRoots: map[string]string{
		"codex":  filepath.Join(home, ".codex", "skills"),
		"claude": filepath.Join(home, ".claude", "skills"),
		"hermes": filepath.Join(home, ".hermes", "skills"),
	}}
}

func ScanAll(paths Paths) ([]domain.InventoryRuntime, error) {
	if paths.Home == "" {
		return nil, errors.New("home directory is required")
	}
	var result []domain.InventoryRuntime
	for _, kind := range []string{"codex", "claude", "hermes"} {
		root := paths.RuntimeRoots[kind]
		if root == "" {
			root = DefaultPaths(paths.Home).RuntimeRoots[kind]
		}
		inventory := map[string]any{"skills": []any{}, "symlinks": []any{}, "managed": 0, "protected": 0}
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			skills, symlinks, managed, protected, err := scanSkillRoot(root)
			if err != nil {
				return nil, err
			}
			inventory["skills"] = skills
			inventory["symlinks"] = symlinks
			inventory["managed"] = managed
			inventory["protected"] = protected
		}
		config := scanRuntimeConfig(paths.Home, kind)
		if kind == "hermes" {
			inventory["hubLock"] = readJSONRedacted(filepath.Join(paths.Home, ".hermes", ".hub", "lock.json"))
			inventory["taps"] = listNames(filepath.Join(paths.Home, ".hermes", "taps"))
		}
		result = append(result, domain.InventoryRuntime{Kind: kind, RootPath: root, Version: "", Config: config, Inventory: inventory})
	}
	return result, nil
}

func scanSkillRoot(root string) ([]any, []any, int, int, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	var skills []any
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
		hash, _ := hashDirectory(directory)
		skills = append(skills, map[string]any{"name": filepath.Base(directory), "path": directory, "managed": isManaged, "protected": isProtected, "disabled": strings.Contains(filepath.ToSlash(directory), "/.toolhub-disabled/"), "sha256": hash})
		return nil
	})
	return skills, symlinks, managed, protected, err
}

func scanRuntimeConfig(home, kind string) map[string]any {
	paths := map[string][]string{
		"codex":  {filepath.Join(home, ".codex", "config.toml")},
		"claude": {filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "settings.json")},
		"hermes": {filepath.Join(home, ".hermes", "config.yaml")},
	}
	result := map[string]any{"platform": runtime.GOOS, "configFiles": []any{}}
	var files []any
	mcpServers := map[string]any{}
	for _, candidate := range paths[kind] {
		if info, err := os.Stat(candidate); err == nil {
			files = append(files, map[string]any{"path": candidate, "size": info.Size(), "modifiedAt": info.ModTime()})
			for name, config := range readMCPConfig(candidate, kind) {
				mcpServers[name] = config
			}
		}
	}
	result["configFiles"] = files
	result["mcpServers"] = security.RedactMap(mcpServers)
	result["mcpm"] = readJSONRedacted(filepath.Join(home, ".config", "mcpm", "config.json"))
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
	keys := []string{"mcpServers", "mcp_servers"}
	if kind == "codex" || kind == "hermes" {
		keys = []string{"mcp_servers", "mcpServers"}
	}
	for _, key := range keys {
		if servers, ok := value[key].(map[string]any); ok {
			return servers
		}
	}
	return nil
}

func hashDirectory(root string) (string, error) {
	hash := sha256.New()
	var files []string
	err := filepath.WalkDir(root, func(current string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !item.IsDir() {
			files = append(files, current)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, file := range files {
		relative, _ := filepath.Rel(root, file)
		_, _ = io.WriteString(hash, filepath.ToSlash(relative))
		body, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write(body)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
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
