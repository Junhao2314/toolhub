package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/domain"
)

type SharedSyncRequest struct {
	SourceName                string   `json:"sourceName"`
	Scopes                    []string `json:"scopes"`
	DryRun                    bool     `json:"dryRun"`
	ExpectedSourceFingerprint string   `json:"expectedSourceFingerprint,omitempty"`
}

type SharedReconciler struct {
	DataDir        string
	Sources        []SharedSourceConfig
	FingerprintKey []byte
}

func (reconciler SharedReconciler) Reconcile(ctx context.Context, request SharedSyncRequest) (domain.SharedSyncResult, error) {
	source, err := FindSharedSource(reconciler.Sources, request.SourceName)
	if err != nil {
		return domain.SharedSyncResult{}, err
	}
	scopes, err := normalizeSharedScopes(request.Scopes)
	if err != nil {
		return domain.SharedSyncResult{}, err
	}
	release, err := acquireSharedLock(ctx, reconciler.DataDir, source.Name)
	if err != nil {
		return domain.SharedSyncResult{}, err
	}
	defer release()

	before, err := ScanSharedSource(source, reconciler.DataDir, reconciler.FingerprintKey)
	if err != nil {
		return domain.SharedSyncResult{}, err
	}
	if request.ExpectedSourceFingerprint != "" && before.SourceFingerprint != request.ExpectedSourceFingerprint {
		return domain.SharedSyncResult{}, errors.New("shared source fingerprint changed before reconciliation")
	}
	if !request.DryRun && source.Mode != SharedModeManaged {
		return domain.SharedSyncResult{}, errors.New("shared source is observed-only; managed mode is required for writes")
	}
	state, err := loadSharedState(reconciler.DataDir, source.Name)
	if err != nil {
		return domain.SharedSyncResult{}, err
	}
	state.ConfigFingerprint = SharedSourceConfigFingerprint(source)
	result := domain.SharedSyncResult{DryRun: request.DryRun}
	stateChanged := false
	if scopes["skills"] {
		changed, actions, conflicts, err := reconcileSharedSkills(source, &state, request.DryRun)
		if err != nil {
			return domain.SharedSyncResult{}, err
		}
		result.Changed = result.Changed || changed
		result.Actions = append(result.Actions, actions...)
		result.Conflicts = append(result.Conflicts, conflicts...)
		stateChanged = stateChanged || changed || (!request.DryRun && len(actions) > 0)
	}
	if scopes["mcp"] {
		changed, actions, conflicts, err := reconcileSharedMCP(source, reconciler.DataDir, reconciler.FingerprintKey, &state, request.DryRun)
		if err != nil {
			return domain.SharedSyncResult{}, err
		}
		result.Changed = result.Changed || changed
		result.Actions = append(result.Actions, actions...)
		result.Conflicts = append(result.Conflicts, conflicts...)
		stateChanged = stateChanged || changed || (!request.DryRun && len(actions) > 0)
	}
	if !request.DryRun && stateChanged {
		if err := saveSharedState(reconciler.DataDir, state); err != nil {
			return domain.SharedSyncResult{}, err
		}
	}
	after, err := ScanSharedSource(source, reconciler.DataDir, reconciler.FingerprintKey)
	if err != nil {
		return domain.SharedSyncResult{}, err
	}
	if len(result.Conflicts) > 0 {
		after.Status = "conflict"
		after.LastError = appendError(after.LastError, fmt.Errorf("%d reconciliation conflict(s)", len(result.Conflicts)))
	}
	result.Source = after
	sort.Strings(result.Actions)
	sort.Strings(result.Conflicts)
	return result, nil
}

func reconcileSharedSkills(source SharedSourceConfig, state *sharedManagedState, dryRun bool) (bool, []string, []string, error) {
	skillsInventory, err := scanSharedSkills(source, true)
	if err != nil {
		return false, nil, nil, err
	}
	skillsByName := map[string]domain.SharedSkillInventory{}
	for _, skill := range skillsInventory {
		if skill.State == "blocked" {
			continue
		}
		skillsByName[skill.Name] = skill
	}
	changed := false
	var actions []string
	var conflicts []string
	kinds := make([]string, 0, len(source.Consumers))
	for kind := range source.Consumers {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		consumer := source.Consumers[kind]
		if consumer.SkillsPath == "" {
			continue
		}
		managed := state.Links[kind]
		if managed == nil {
			managed = map[string]managedLink{}
			state.Links[kind] = managed
		}
		names := unionNames(skillsByName, managed)
		for _, name := range names {
			skill, exists := skillsByName[name]
			record, wasManaged := managed[name]
			targetPath := filepath.Join(consumer.SkillsPath, name)
			if exists {
				expected := skill.SourcePath
				info, statErr := os.Lstat(targetPath)
				switch {
				case errors.Is(statErr, os.ErrNotExist):
					actions = append(actions, fmt.Sprintf("create skill link %s/%s", kind, name))
					if !dryRun {
						if err := os.MkdirAll(consumer.SkillsPath, 0755); err != nil {
							return false, nil, nil, err
						}
						if err := os.Symlink(expected, targetPath); err != nil {
							return false, nil, nil, err
						}
						managed[name] = managedLink{TargetPath: targetPath, ExpectedTarget: expected}
						changed = true
					}
				case statErr != nil:
					return false, nil, nil, statErr
				case info.Mode()&os.ModeSymlink == 0:
					conflicts = append(conflicts, fmt.Sprintf("%s/%s is an unowned non-symlink", kind, name))
				default:
					actualRaw, err := os.Readlink(targetPath)
					if err != nil {
						return false, nil, nil, err
					}
					actual := cleanLinkTarget(targetPath, actualRaw)
					switch {
					case samePath(actual, expected):
						if !wasManaged {
							actions = append(actions, fmt.Sprintf("adopt matching skill link %s/%s", kind, name))
							if !dryRun {
								managed[name] = managedLink{TargetPath: targetPath, ExpectedTarget: expected}
							}
						} else if !samePath(record.ExpectedTarget, expected) && !dryRun {
							managed[name] = managedLink{TargetPath: targetPath, ExpectedTarget: expected}
						}
					case wasManaged && samePath(actual, record.ExpectedTarget):
						actions = append(actions, fmt.Sprintf("update skill link %s/%s", kind, name))
						if !dryRun {
							if err := replaceOwnedSymlink(targetPath, expected); err != nil {
								return false, nil, nil, err
							}
							managed[name] = managedLink{TargetPath: targetPath, ExpectedTarget: expected}
							changed = true
						}
					default:
						conflicts = append(conflicts, fmt.Sprintf("%s/%s symlink target changed outside ToolHub", kind, name))
					}
				}
				continue
			}
			if !wasManaged {
				continue
			}
			info, statErr := os.Lstat(targetPath)
			switch {
			case errors.Is(statErr, os.ErrNotExist):
				actions = append(actions, fmt.Sprintf("forget absent stale skill link %s/%s", kind, name))
				if !dryRun {
					delete(managed, name)
				}
			case statErr != nil:
				return false, nil, nil, statErr
			case info.Mode()&os.ModeSymlink == 0:
				conflicts = append(conflicts, fmt.Sprintf("stale skill link %s/%s became a non-symlink", kind, name))
			default:
				actualRaw, err := os.Readlink(targetPath)
				if err != nil {
					return false, nil, nil, err
				}
				if !samePath(cleanLinkTarget(targetPath, actualRaw), record.ExpectedTarget) {
					conflicts = append(conflicts, fmt.Sprintf("stale skill link %s/%s changed outside ToolHub", kind, name))
					continue
				}
				actions = append(actions, fmt.Sprintf("remove stale skill link %s/%s", kind, name))
				if !dryRun {
					if err := os.Remove(targetPath); err != nil {
						return false, nil, nil, err
					}
					delete(managed, name)
					changed = true
				}
			}
		}
	}
	return changed, actions, conflicts, nil
}

func reconcileSharedMCP(source SharedSourceConfig, dataDir string, fingerprintKey []byte, state *sharedManagedState, dryRun bool) (bool, []string, []string, error) {
	manifest, err := ReadSharedManifest(source.MCPManifest, fingerprintKey)
	if err != nil {
		return false, nil, nil, err
	}
	if !dryRun && manifest.Mode.Perm() != 0600 {
		return false, nil, nil, fmt.Errorf("shared MCP manifest permissions are %04o; managed mode requires 0600", manifest.Mode.Perm())
	}
	servers := map[string]SharedMCPServer{}
	for _, server := range manifest.Servers {
		servers[server.Descriptor.Name] = server
	}
	changed := false
	var actions []string
	var conflicts []string
	kinds := make([]string, 0, len(source.Consumers))
	for kind := range source.Consumers {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		consumer := source.Consumers[kind]
		if consumer.MCPInherits != "" {
			if parent, ok := source.Consumers[consumer.MCPInherits]; !ok || parent.MCPPath == "" {
				conflicts = append(conflicts, fmt.Sprintf("%s inherits MCP from unavailable consumer %s", kind, consumer.MCPInherits))
			}
			continue
		}
		if consumer.MCPPath == "" {
			continue
		}
		document, err := loadSharedMCPDocument(consumer.MCPPath, consumer.MCPFormat)
		if err != nil {
			return false, nil, nil, fmt.Errorf("load %s MCP configuration: %w", kind, err)
		}
		if !dryRun && document.exists && document.mode.Perm() != 0600 {
			return false, nil, nil, fmt.Errorf("%s MCP configuration permissions are %04o; managed mode requires 0600", kind, document.mode.Perm())
		}
		managed := cloneManagedMCPState(state.MCP[kind])
		if managed.Entries == nil {
			managed = managedMCPState{Path: consumer.MCPPath, Format: consumer.MCPFormat, Entries: map[string]string{}}
		}
		names := unionNames(servers, managed.Entries)
		documentChanged := false
		stateOnlyChanged := false
		for _, name := range names {
			server, exists := servers[name]
			desired := exists && server.Enabled
			actual, present := document.entry(name)
			actualFingerprint := ""
			if present {
				actualFingerprint = entryFingerprint(actual)
			}
			priorExpected, wasManaged := managed.Entries[name]
			if desired {
				desiredEntry := renderSharedMCPEntry(consumer.MCPFormat, server)
				desiredFingerprint := entryFingerprint(desiredEntry)
				switch {
				case !wasManaged && !present:
					actions = append(actions, fmt.Sprintf("create MCP entry %s/%s", kind, name))
					if !dryRun {
						if err := document.setEntry(name, desiredEntry); err != nil {
							return false, nil, nil, err
						}
						managed.Entries[name] = desiredFingerprint
						documentChanged = true
					}
				case !wasManaged && actualFingerprint == desiredFingerprint:
					actions = append(actions, fmt.Sprintf("adopt matching MCP entry %s/%s", kind, name))
					if !dryRun {
						managed.Entries[name] = desiredFingerprint
						stateOnlyChanged = true
					}
				case !wasManaged:
					conflicts = append(conflicts, fmt.Sprintf("MCP entry %s/%s is locally owned", kind, name))
				case !present:
					actions = append(actions, fmt.Sprintf("restore missing MCP entry %s/%s", kind, name))
					if !dryRun {
						if err := document.setEntry(name, desiredEntry); err != nil {
							return false, nil, nil, err
						}
						managed.Entries[name] = desiredFingerprint
						documentChanged = true
					}
				case actualFingerprint != priorExpected:
					conflicts = append(conflicts, fmt.Sprintf("managed MCP entry %s/%s changed outside ToolHub", kind, name))
				case actualFingerprint != desiredFingerprint:
					actions = append(actions, fmt.Sprintf("update MCP entry %s/%s", kind, name))
					if !dryRun {
						if err := document.setEntry(name, desiredEntry); err != nil {
							return false, nil, nil, err
						}
						managed.Entries[name] = desiredFingerprint
						documentChanged = true
					}
				default:
					if !dryRun && managed.Entries[name] != desiredFingerprint {
						managed.Entries[name] = desiredFingerprint
						stateOnlyChanged = true
					}
				}
				continue
			}
			if !wasManaged {
				continue
			}
			switch {
			case !present:
				actions = append(actions, fmt.Sprintf("forget absent stale MCP entry %s/%s", kind, name))
				if !dryRun {
					delete(managed.Entries, name)
					stateOnlyChanged = true
				}
			case actualFingerprint != priorExpected:
				conflicts = append(conflicts, fmt.Sprintf("stale managed MCP entry %s/%s changed outside ToolHub", kind, name))
			default:
				actions = append(actions, fmt.Sprintf("remove stale MCP entry %s/%s", kind, name))
				if !dryRun {
					document.deleteEntry(name)
					delete(managed.Entries, name)
					documentChanged = true
				}
			}
		}
		if !dryRun && documentChanged {
			if err := writeSharedMCPDocument(document, dataDir, source.Name, kind); err != nil {
				if errors.Is(err, errSharedMCPConcurrentChange) {
					conflicts = append(conflicts, fmt.Sprintf("%s MCP configuration changed during reconciliation", kind))
					continue
				}
				return false, nil, nil, err
			}
			encoded, _ := document.encode()
			managed.DocumentFingerprint = documentFingerprint(encoded)
			changed = true
			if consumer.MCPFormat == MCPFormatCodexPluginJSON {
				if err := ensureCodexPluginMetadata(consumer.MCPPath); err != nil {
					return false, nil, nil, err
				}
			}
		}
		if !dryRun && (documentChanged || stateOnlyChanged) {
			managed.Path = consumer.MCPPath
			managed.Format = consumer.MCPFormat
			state.MCP[kind] = managed
		}
	}
	return changed, actions, conflicts, nil
}

func cloneManagedMCPState(value managedMCPState) managedMCPState {
	cloned := value
	cloned.Entries = make(map[string]string, len(value.Entries))
	for name, fingerprint := range value.Entries {
		cloned.Entries[name] = fingerprint
	}
	return cloned
}

func ensureCodexPluginMetadata(mcpPath string) error {
	pluginPath := filepath.Join(filepath.Dir(mcpPath), "plugin.json")
	if _, err := os.Stat(pluginPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	metadata := map[string]any{
		"name":        "shared-mcp",
		"version":     "1.0.0",
		"description": "Shared MCP servers managed from the configured shared source",
		"author":      map[string]string{"name": "ToolHub"},
		"skills":      "./skills/",
		"mcpServers":  "./.mcp.json",
		"interface": map[string]string{
			"displayName":      "Shared MCP",
			"shortDescription": "Node-local MCP servers from a shared source",
			"category":         "Coding",
		},
	}
	body, _ := json.MarshalIndent(metadata, "", "  ")
	body = append(body, '\n')
	if err := os.MkdirAll(filepath.Join(filepath.Dir(mcpPath), "skills"), 0700); err != nil {
		return err
	}
	temporary := pluginPath + ".toolhub-new-" + uuid.NewString()
	if err := os.WriteFile(temporary, body, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, pluginPath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return syncDirectory(filepath.Dir(pluginPath))
}

func replaceOwnedSymlink(path, target string) error {
	backup := path + ".toolhub-old-" + uuid.NewString()
	if err := os.Rename(path, backup); err != nil {
		return err
	}
	if err := os.Symlink(target, path); err != nil {
		_ = os.Rename(backup, path)
		return err
	}
	if err := os.Remove(backup); err != nil {
		_ = os.Remove(path)
		_ = os.Rename(backup, path)
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func normalizeSharedScopes(values []string) (map[string]bool, error) {
	result := map[string]bool{}
	if len(values) == 0 {
		result["skills"] = true
		result["mcp"] = true
		return result, nil
	}
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "all", "both":
			result["skills"] = true
			result["mcp"] = true
		case "skills":
			result["skills"] = true
		case "mcp":
			result["mcp"] = true
		default:
			return nil, fmt.Errorf("invalid shared sync scope %q", value)
		}
	}
	return result, nil
}

func acquireSharedLock(ctx context.Context, dataDir, sourceName string) (func(), error) {
	root := filepath.Join(dataDir, "shared", "locks")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, safeStateName.ReplaceAllString(sourceName, "-")+".lock")
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d\n%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 30*time.Minute {
			_ = os.Remove(path)
			continue
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func unionNames[A any, B any](first map[string]A, second map[string]B) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(first)+len(second))
	for name := range first {
		seen[name] = true
		result = append(result, name)
	}
	for name := range second {
		if !seen[name] {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}
