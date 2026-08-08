package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/skills"
)

const (
	hermesMaxDepth       = 8
	hermesMaxCandidates  = 2000
	hermesMaxFiles       = 20000
	hermesMaxScanBytes   = 256 << 20
	hermesMaxMetadata    = 64 << 10
	hermesBatchMaxBytes  = 30 << 20
	hermesMaxCategoryLen = 120
)

type hermesCandidate struct {
	Member bridgeprotocol.InventoryMember
	Root   string
}

func scanHermesSkills(root string) (ScanResult, map[string]hermesCandidate, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return hashInventory(nil), map[string]hermesCandidate{}, nil
	}
	if err != nil {
		return ScanResult{}, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ScanResult{}, nil, errors.New("Hermes Skill root must be a real directory")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return ScanResult{}, nil, err
	}
	var members []bridgeprotocol.InventoryMember
	candidates := map[string]hermesCandidate{}
	seenSlugs := map[string][]string{}
	files, totalBytes := 0, int64(0)
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		parts := strings.Split(relative, "/")
		depth := len(parts)
		for _, part := range parts {
			if isHermesSkippedComponent(part) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(part, ".") {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if depth > hermesMaxDepth {
			return errors.New("Hermes Skill inventory exceeds traversal depth limit")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			if len(members) >= hermesMaxCandidates {
				return errors.New("Hermes Skill inventory exceeds candidate limit")
			}
			member := hermesOpaqueMember(relative, filepath.Base(relative), "symlink", false)
			members = append(members, member)
			return nil
		}
		if !entry.IsDir() {
			files++
			if files > hermesMaxFiles {
				return errors.New("Hermes Skill inventory exceeds file limit")
			}
			if info, err := entry.Info(); err == nil {
				totalBytes += info.Size()
				if totalBytes > hermesMaxScanBytes {
					return errors.New("Hermes Skill inventory exceeds scan byte limit")
				}
			}
			return nil
		}
		if depth > hermesMaxDepth {
			return errors.New("Hermes Skill inventory exceeds traversal depth limit")
		}
		if len(members) >= hermesMaxCandidates {
			return errors.New("Hermes Skill inventory exceeds candidate limit")
		}
		candidateRoot := current
		if _, err := os.Stat(filepath.Join(candidateRoot, "SKILL.md")); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		id := hermesCandidateID(relative)
		member := hermesOpaqueMember(relative, filepath.Base(relative), "", false)
		member.ID = id
		member.Kind = "skill"
		member.Scope = "user"
		pkg, scanErr := skills.ScanDirectory(candidateRoot, skills.DefaultLimits)
		if scanErr != nil {
			member.Name = filepath.Base(relative)
			member.Slug = strings.ToLower(filepath.Base(relative))
			member.EligibilityReason = hermesReason(scanErr)
			members = append(members, member)
			candidates[id] = hermesCandidate{Member: member, Root: candidateRoot}
			return filepath.SkipDir
		}
		totalBytes += int64(len(pkg.CanonicalZIP))
		if totalBytes > hermesMaxScanBytes {
			return errors.New("Hermes Skill inventory exceeds scan byte limit")
		}
		member.Name, member.Slug, member.Description = pkg.Name, pkg.Slug, pkg.Description
		member.ContentHash = pkg.ContentHash
		member.Importable = true
		member.Source, member.Provider, member.Trust, member.Category, member.Builtin = classifyHermesCandidate(candidateRoot, root, relative)
		member.Category = safeHermesCategory(member.Category)
		seenSlugs[member.Slug] = append(seenSlugs[member.Slug], id)
		members = append(members, member)
		candidates[id] = hermesCandidate{Member: member, Root: candidateRoot}
		return filepath.SkipDir
	})
	if err != nil {
		return ScanResult{}, nil, err
	}
	for slug, ids := range seenSlugs {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			for index := range members {
				if members[index].ID == id {
					members[index].Importable = false
					members[index].Shadowed = true
					members[index].EligibilityReason = "duplicate_slug"
					candidates[id] = hermesCandidate{Member: members[index], Root: candidates[id].Root}
				}
			}
		}
		_ = slug
	}
	return hashInventory(members), candidates, nil
}

func (m *Manager) ScanHermes(user ManagedUser) (ScanResult, error) {
	paths, err := PathsFor(user, bridgeprotocol.RuntimeHermes)
	if err != nil {
		return ScanResult{}, err
	}
	if err := rejectSymlinkComponents(user.Home, paths.SkillsRoot); err != nil {
		return ScanResult{}, err
	}
	scan, _, err := scanHermesSkills(paths.SkillsRoot)
	return scan, err
}

func (m *Manager) ExportLocalSkillBatch(user ManagedUser, input bridgeprotocol.LocalSkillBatchExportRequest) (bridgeprotocol.LocalSkillBatchExportResponse, error) {
	if err := validateHermesBatchTarget(input.Target); err != nil {
		return bridgeprotocol.LocalSkillBatchExportResponse{}, err
	}
	if !bridgeprotocol.IsSHA256(input.ExpectedRevision) || len(input.Items) == 0 || len(input.Items) > 50 {
		return bridgeprotocol.LocalSkillBatchExportResponse{}, errors.New("Hermes Skill batch request is invalid")
	}
	paths, err := PathsFor(user, bridgeprotocol.RuntimeHermes)
	if err != nil {
		return bridgeprotocol.LocalSkillBatchExportResponse{}, err
	}
	scan, candidates, err := scanHermesSkills(paths.SkillsRoot)
	if err != nil {
		return bridgeprotocol.LocalSkillBatchExportResponse{}, err
	}
	if scan.Revision != input.ExpectedRevision {
		return bridgeprotocol.LocalSkillBatchExportResponse{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Hermes Skill inventory changed before import"}
	}
	result := bridgeprotocol.LocalSkillBatchExportResponse{TargetRevision: scan.Revision, Items: []bridgeprotocol.LocalSkillBatchExportItem{}}
	seen := map[string]bool{}
	var total int64
	for _, requested := range input.Items {
		item := bridgeprotocol.LocalSkillBatchExportItem{ID: requested.ID, Status: "Failed"}
		if seen[requested.ID] {
			item.Error = &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "duplicate Hermes Skill ID"}
			result.Items = append(result.Items, item)
			continue
		}
		seen[requested.ID] = true
		candidate, ok := candidates[requested.ID]
		if !ok || !candidate.Member.Importable || candidate.Member.ContentHash != requested.ContentHash {
			item.Status = "Stale"
			item.Error = &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Hermes Skill candidate is stale or not importable"}
			result.Items = append(result.Items, item)
			continue
		}
		pkg, scanErr := skills.ScanDirectory(candidate.Root, skills.DefaultLimits)
		if scanErr != nil || pkg.ContentHash != requested.ContentHash {
			item.Error = &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Hermes Skill changed during import"}
			result.Items = append(result.Items, item)
			continue
		}
		if total+int64(len(pkg.CanonicalZIP)) > hermesBatchMaxBytes {
			item.Error = &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "Hermes Skill batch response limit reached"}
			result.Items = append(result.Items, item)
			continue
		}
		total += int64(len(pkg.CanonicalZIP))
		item.Name, item.Slug, item.SHA256, item.ContentHash = pkg.Name, pkg.Slug, pkg.SHA256, pkg.ContentHash
		item.Source, item.Provider, item.Category = candidate.Member.Source, candidate.Member.Provider, candidate.Member.Category
		item.Archive, item.Status = pkg.CanonicalZIP, "Exported"
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func validateHermesBatchTarget(target bridgeprotocol.Target) error {
	if err := target.Validate(false); err != nil {
		return err
	}
	if target.NodeKind != bridgeprotocol.NodeKindLocal || target.Runtime != bridgeprotocol.RuntimeHermes {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrProtectedScope, Message: "Hermes Skill intake is available only from local Hermes"}
	}
	return nil
}

func hermesCandidateID(relative string) string {
	sum := sha256.Sum256([]byte("hermes-skill\x00" + filepath.ToSlash(relative)))
	return "hermes:" + hex.EncodeToString(sum[:])
}

func hermesOpaqueMember(relative, name, reason string, importable bool) bridgeprotocol.InventoryMember {
	return bridgeprotocol.InventoryMember{ID: hermesCandidateID(relative), Kind: "skill", Name: name, Slug: strings.ToLower(name), Scope: "user", Importable: importable, EligibilityReason: reason}
}

func isHermesSkippedComponent(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == ".archive" || value == ".hub" || value == "archive" || value == "archives" || value == "backup" || value == "backups" || value == "quarantine" || value == "staging" || strings.HasPrefix(value, ".toolhub-")
}

func hermesReason(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "symlink"):
		return "symlink"
	case strings.Contains(message, "size"), strings.Contains(message, "file"):
		return "limit"
	default:
		return "invalid_package"
	}
}

func classifyHermesCandidate(candidateRoot, scanRoot, relative string) (source, provider, trust, category string, builtin bool) {
	source = "local"
	category = filepath.ToSlash(filepath.Dir(relative))
	if category == "." {
		category = "root"
	}
	if body, err := boundedRead(filepath.Join(candidateRoot, ".bundled_manifest"), hermesMaxMetadata); err == nil {
		var manifest map[string]any
		if json.Unmarshal(body, &manifest) == nil {
			if name, ok := manifest["name"].(string); ok && strings.TrimSpace(name) != "" {
				builtin = true
			}
		}
	}
	if body, err := boundedRead(filepath.Join(candidateRoot, ".hub", "lock.json"), hermesMaxMetadata); err == nil {
		var lock map[string]any
		if json.Unmarshal(body, &lock) == nil {
			source = "hub"
			provider, _ = lock["provider"].(string)
			if provider == "" {
				provider, _ = lock["hub"].(string)
			}
			trust, _ = lock["trust"].(string)
		}
	}
	_ = scanRoot
	return strings.TrimSpace(source), strings.TrimSpace(provider), strings.TrimSpace(trust), category, builtin
}

func safeHermesCategory(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || value == "." || len(value) > hermesMaxCategoryLen || strings.HasPrefix(value, "/") || strings.Contains(value, "..") || strings.ContainsAny(value, "\\\x00") {
		return "root"
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return "root"
		}
	}
	return value
}

func boundedRead(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, errors.New("metadata unavailable")
	}
	body, err := os.ReadFile(path)
	if err != nil || int64(len(body)) > limit {
		return nil, errors.New("metadata unavailable")
	}
	return body, nil
}
