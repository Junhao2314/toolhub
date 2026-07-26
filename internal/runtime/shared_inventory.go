package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/skills"
)

func ScanSharedSources(sources []SharedSourceConfig, dataDir string, fingerprintKey []byte) []domain.SharedSourceInventory {
	result := make([]domain.SharedSourceInventory, 0, len(sources))
	for _, source := range sources {
		inventory, err := ScanSharedSource(source, dataDir, fingerprintKey)
		if err != nil {
			inventory = domain.SharedSourceInventory{
				Name:              source.Name,
				Mode:              source.Mode,
				AutoSync:          source.AutoSync,
				SkillsRoot:        source.SkillsRoot,
				MCPManifestPath:   source.MCPManifest,
				ConfigFingerprint: SharedSourceConfigFingerprint(source),
				Status:            "failed",
				LastError:         err.Error(),
				Skills:            []domain.SharedSkillInventory{},
				MCPServers:        []domain.SharedMCPServerInventory{},
				Consumers:         []domain.SharedConsumerInventory{},
			}
		}
		result = append(result, inventory)
	}
	return result
}

func ScanSharedSource(source SharedSourceConfig, dataDir string, fingerprintKey []byte) (domain.SharedSourceInventory, error) {
	state, err := loadSharedState(dataDir, source.Name)
	if err != nil {
		return domain.SharedSourceInventory{}, err
	}
	inventory := domain.SharedSourceInventory{
		Name:              source.Name,
		Mode:              source.Mode,
		AutoSync:          source.AutoSync,
		SkillsRoot:        source.SkillsRoot,
		MCPManifestPath:   source.MCPManifest,
		ConfigFingerprint: SharedSourceConfigFingerprint(source),
		Status:            "observed",
		Skills:            []domain.SharedSkillInventory{},
		MCPServers:        []domain.SharedMCPServerInventory{},
		Consumers:         []domain.SharedConsumerInventory{},
	}

	skillInventory, skillErr := scanSharedSkills(source, true)
	if skillErr != nil {
		inventory.LastError = appendError(inventory.LastError, skillErr)
	}
	inventory.Skills = skillInventory

	manifest, manifestErr := ReadSharedManifest(source.MCPManifest, fingerprintKey)
	if manifestErr != nil {
		inventory.LastError = appendError(inventory.LastError, manifestErr)
	} else {
		for _, server := range manifest.Servers {
			inventory.MCPServers = append(inventory.MCPServers, domain.SharedMCPServerInventory{Descriptor: server.Descriptor, Description: server.Description, Enabled: server.Enabled})
		}
		if manifest.Mode.Perm() != 0600 {
			inventory.LastError = appendError(inventory.LastError, fmt.Errorf("shared MCP manifest permissions are %04o; managed mode requires 0600", manifest.Mode.Perm()))
			inventory.Status = "blocked"
		}
	}

	consumersByKind := map[string]domain.SharedConsumerInventory{}
	kinds := make([]string, 0, len(source.Consumers))
	for kind := range source.Consumers {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		consumer := source.Consumers[kind]
		item := domain.SharedConsumerInventory{
			Kind:          kind,
			SkillsPath:    consumer.SkillsPath,
			MCPPath:       consumer.MCPPath,
			MCPFormat:     consumer.MCPFormat,
			InheritsFrom:  consumer.MCPInherits,
			SkillsEnabled: consumer.SkillsPath != "",
			MCPEnabled:    consumer.MCPPath != "" || consumer.MCPInherits != "",
			State:         "observed",
			SkillLinks:    []domain.SharedSkillLinkInventory{},
			MCPBindings:   []domain.SharedMCPBindingInventory{},
		}
		if consumer.SkillsPath != "" {
			item.SkillLinks = inspectSharedSkillLinks(source, kind, consumer, skillInventory, state)
			item.State = mergeState(item.State, stateForLinks(item.SkillLinks))
		}
		if consumer.MCPPath != "" && manifestErr == nil {
			bindings, expected, actual, mcpState, lastError := inspectSharedMCPConsumer(source, kind, consumer, manifest, state)
			item.MCPBindings = bindings
			item.ExpectedFingerprint = expected
			item.ActualFingerprint = actual
			item.State = mergeState(item.State, mcpState)
			item.LastError = lastError
		}
		consumersByKind[kind] = item
	}
	for _, kind := range kinds {
		item := consumersByKind[kind]
		if item.InheritsFrom != "" {
			parent, ok := consumersByKind[item.InheritsFrom]
			if !ok || !parent.MCPEnabled {
				item.State = mergeState(item.State, "blocked")
				item.LastError = appendError(item.LastError, errors.New("inherited MCP consumer is unavailable"))
			} else {
				item.ExpectedFingerprint = parent.ExpectedFingerprint
				item.ActualFingerprint = parent.ActualFingerprint
				item.MCPBindings = append([]domain.SharedMCPBindingInventory(nil), parent.MCPBindings...)
				item.State = mergeState(item.State, parent.State)
			}
			consumersByKind[kind] = item
		}
	}
	for _, kind := range kinds {
		item := consumersByKind[kind]
		inventory.Consumers = append(inventory.Consumers, item)
		inventory.Status = mergeState(inventory.Status, item.State)
		inventory.LastError = appendError(inventory.LastError, errorFromString(item.LastError))
	}
	for _, skill := range inventory.Skills {
		state := skill.State
		if state == "blocked" {
			// One unscannable Skill must not escalate the whole source to blocked; the
			// per-skill state and reason stay visible on its own row.
			state = "drift"
		}
		inventory.Status = mergeState(inventory.Status, state)
		if skill.LastError != "" {
			inventory.LastError = appendError(inventory.LastError, fmt.Errorf("%s: %s", skill.Name, skill.LastError))
		}
	}
	if skillErr != nil || manifestErr != nil {
		inventory.Status = mergeState(inventory.Status, "missing")
	}
	if inventory.Status == "observed" && source.Mode == SharedModeManaged && inventory.LastError == "" {
		inventory.Status = "in_sync"
	}
	inventory.SourceFingerprint = sharedSourceFingerprint(inventory, manifest.SourceFingerprint)
	return inventory, nil
}

func scanSharedSkills(source SharedSourceConfig, hashContent bool) ([]domain.SharedSkillInventory, error) {
	entries, err := os.ReadDir(source.SkillsRoot)
	if err != nil {
		return []domain.SharedSkillInventory{}, err
	}
	result := make([]domain.SharedSkillInventory, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		item := domain.SharedSkillInventory{Name: name, SourcePath: filepath.Join(source.SkillsRoot, name), EntryType: "directory", State: "observed"}
		if err := validateSharedName(name); err != nil {
			item.State = "blocked"
			item.LastError = err.Error()
			result = append(result, item)
			continue
		}
		info, err := os.Lstat(item.SourcePath)
		if err != nil {
			item.State = "missing"
			item.LastError = err.Error()
			result = append(result, item)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			item.EntryType = "symlink"
		}
		resolved, err := filepath.EvalSymlinks(item.SourcePath)
		if err != nil {
			item.State = "blocked"
			item.LastError = "source symlink cannot be resolved"
			result = append(result, item)
			continue
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil || !pathWithinAny(resolved, source.AllowedSkillRoots) {
			item.State = "blocked"
			item.LastError = "source entry resolves outside allowed Skill roots"
			result = append(result, item)
			continue
		}
		resolvedInfo, err := os.Stat(resolved)
		if err != nil || !resolvedInfo.IsDir() {
			item.State = "blocked"
			item.LastError = "source entry does not resolve to a directory"
			result = append(result, item)
			continue
		}
		item.ResolvedSourcePath = filepath.Clean(resolved)
		item.Managed = fileExists(filepath.Join(item.SourcePath, ".toolhub-managed.json"))
		if hashContent {
			pkg, scanErr := skills.ScanDirectory(item.ResolvedSourcePath, skills.DefaultLimits)
			if scanErr != nil {
				item.State = "blocked"
				item.LastError = "package validation failed: " + scanErr.Error()
			} else {
				item.SHA256 = pkg.SHA256
			}
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func inspectSharedSkillLinks(source SharedSourceConfig, kind string, consumer SharedConsumerConfig, skillsInventory []domain.SharedSkillInventory, state sharedManagedState) []domain.SharedSkillLinkInventory {
	byName := map[string]domain.SharedSkillInventory{}
	for _, skill := range skillsInventory {
		if skill.State != "blocked" {
			byName[skill.Name] = skill
		}
	}
	managed := state.Links[kind]
	names := make([]string, 0, len(byName)+len(managed))
	seen := map[string]bool{}
	for name := range byName {
		seen[name] = true
		names = append(names, name)
	}
	for name := range managed {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	result := make([]domain.SharedSkillLinkInventory, 0, len(names))
	for _, name := range names {
		skill, exists := byName[name]
		record, isManaged := managed[name]
		targetPath := filepath.Join(consumer.SkillsPath, name)
		expected := skill.SourcePath
		if !exists {
			expected = record.ExpectedTarget
		}
		item := domain.SharedSkillLinkInventory{SkillName: name, SourcePath: skill.SourcePath, ResolvedSourcePath: skill.ResolvedSourcePath, TargetPath: targetPath, ExpectedTarget: expected, Managed: isManaged, State: "missing"}
		info, err := os.Lstat(targetPath)
		if errors.Is(err, os.ErrNotExist) {
			if !exists && isManaged {
				item.State = "in_sync"
			}
			result = append(result, item)
			continue
		}
		if err != nil {
			item.State = "failed"
			item.LastError = err.Error()
			result = append(result, item)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			item.State = "conflict"
			item.LastError = "consumer target is an unowned non-symlink"
			result = append(result, item)
			continue
		}
		target, err := os.Readlink(targetPath)
		if err != nil {
			item.State = "failed"
			item.LastError = err.Error()
			result = append(result, item)
			continue
		}
		item.ActualTarget = cleanLinkTarget(targetPath, target)
		if !exists {
			if isManaged && samePath(item.ActualTarget, record.ExpectedTarget) {
				item.State = "drift"
			} else {
				item.State = "conflict"
				item.LastError = "stale managed link was modified locally"
			}
		} else if samePath(item.ActualTarget, expected) {
			if isManaged {
				item.State = "in_sync"
			} else {
				item.State = "observed"
			}
		} else if isManaged && samePath(item.ActualTarget, record.ExpectedTarget) {
			item.State = "drift"
		} else {
			item.State = "conflict"
			item.LastError = "consumer symlink points at an unowned target"
		}
		result = append(result, item)
	}
	return result
}

func inspectSharedMCPConsumer(source SharedSourceConfig, kind string, consumer SharedConsumerConfig, manifest SharedManifest, state sharedManagedState) ([]domain.SharedMCPBindingInventory, string, string, string, string) {
	document, err := loadSharedMCPDocument(consumer.MCPPath, consumer.MCPFormat)
	if err != nil {
		return nil, "", "", "failed", err.Error()
	}
	managed := state.MCP[kind]
	servers := map[string]SharedMCPServer{}
	for _, server := range manifest.Servers {
		servers[server.Descriptor.Name] = server
	}
	names := make([]string, 0, len(servers)+len(managed.Entries))
	seen := map[string]bool{}
	for name := range servers {
		seen[name] = true
		names = append(names, name)
	}
	for name := range managed.Entries {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	expectedEntries := map[string]string{}
	actualEntries := map[string]string{}
	bindings := make([]domain.SharedMCPBindingInventory, 0, len(names))
	consumerState := "observed"
	lastError := ""
	for _, name := range names {
		server, exists := servers[name]
		desiredEnabled := exists && server.Enabled
		desiredFingerprint := ""
		if desiredEnabled {
			desiredFingerprint = entryFingerprint(renderSharedMCPEntry(consumer.MCPFormat, server))
			expectedEntries[name] = desiredFingerprint
		}
		actual, present := document.entry(name)
		actualFingerprint := ""
		if present {
			actualFingerprint = entryFingerprint(actual)
			actualEntries[name] = actualFingerprint
		}
		priorExpected, wasManaged := managed.Entries[name]
		binding := domain.SharedMCPBindingInventory{ServerName: name, DesiredFingerprint: desiredFingerprint, ActualFingerprint: actualFingerprint, Enabled: desiredEnabled, Missing: desiredEnabled && !present, EnvKeys: server.Descriptor.EnvKeys, HeaderKeys: server.Descriptor.HeaderKeys, State: "observed"}
		switch {
		case desiredEnabled && !present:
			binding.State = "missing"
			binding.Drift = true
		case desiredEnabled && actualFingerprint == desiredFingerprint:
			if wasManaged {
				binding.State = "in_sync"
			} else {
				binding.State = "observed"
			}
		case desiredEnabled && wasManaged && actualFingerprint == priorExpected:
			binding.State = "drift"
			binding.Drift = true
		case desiredEnabled:
			binding.State = "conflict"
			binding.Drift = true
			binding.LastError = "managed MCP entry changed outside ToolHub"
		case wasManaged && !present:
			binding.State = "in_sync"
		case wasManaged && actualFingerprint == priorExpected:
			binding.State = "drift"
			binding.Drift = true
		case wasManaged:
			binding.State = "conflict"
			binding.Drift = true
			binding.LastError = "stale managed MCP entry changed outside ToolHub"
		default:
			binding.State = "observed"
		}
		consumerState = mergeState(consumerState, binding.State)
		lastError = appendError(lastError, errorFromString(binding.LastError))
		bindings = append(bindings, binding)
	}
	if document.exists && document.mode.Perm() != 0600 {
		consumerState = mergeState(consumerState, "blocked")
		lastError = appendError(lastError, fmt.Errorf("consumer MCP configuration permissions are %04o; managed mode requires 0600", document.mode.Perm()))
	}
	return bindings, fingerprintStringMap(expectedEntries), fingerprintStringMap(actualEntries), consumerState, lastError
}

func validateSharedName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == ".system" || strings.ContainsAny(name, `/\\`) {
		return errors.New("invalid or protected shared Skill name")
	}
	return nil
}

func pathWithinAny(path string, roots []string) bool {
	path = filepath.Clean(path)
	for _, root := range roots {
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			resolvedRoot = root
		}
		resolvedRoot, err = filepath.Abs(resolvedRoot)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(filepath.Clean(resolvedRoot), path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func sharedSourceFingerprint(inventory domain.SharedSourceInventory, manifestFingerprint string) string {
	value := struct {
		Config   string                        `json:"config"`
		Manifest string                        `json:"manifest"`
		Skills   []domain.SharedSkillInventory `json:"skills"`
	}{inventory.ConfigFingerprint, manifestFingerprint, inventory.Skills}
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func fingerprintStringMap(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	body, _ := json.Marshal(values)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func stateForLinks(links []domain.SharedSkillLinkInventory) string {
	state := "observed"
	for _, link := range links {
		state = mergeState(state, link.State)
	}
	return state
}

func mergeState(current, next string) string {
	priority := map[string]int{"observed": 0, "in_sync": 1, "missing": 2, "drift": 3, "blocked": 4, "failed": 5, "conflict": 6}
	if priority[next] > priority[current] {
		return next
	}
	return current
}

func appendError(current string, err error) string {
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		return current
	}
	message := strings.TrimSpace(err.Error())
	if current == "" {
		return message
	}
	if strings.Contains(current, message) {
		return current
	}
	return current + "; " + message
}

func errorFromString(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return errors.New(value)
}
