package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/skills"
)

type AdoptedSkillMarker struct {
	SkillID   string    `json:"skillId"`
	VersionID string    `json:"versionId"`
	SHA256    string    `json:"sha256"`
	ManagedAt time.Time `json:"managedAt"`
}

func PackageDiscoveredSkill(paths Paths, runtimeKind, discoveredPath, expectedSHA256 string) (skills.Package, error) {
	root := paths.RuntimeRoots[runtimeKind]
	if root == "" {
		return skills.Package{}, errors.New("unknown runtime")
	}
	root, target, err := validateDiscoveredSkillPath(root, discoveredPath)
	if err != nil {
		return skills.Package{}, err
	}
	if fileExists(filepath.Join(target, ".toolhub-managed.json")) {
		return skills.Package{}, errors.New("skill is already ToolHub-managed")
	}
	if err := rejectSymlinks(root, target); err != nil {
		return skills.Package{}, err
	}
	pkg, err := skills.ScanDirectory(target, skills.DefaultLimits)
	if err != nil {
		return skills.Package{}, fmt.Errorf("scan discovered skill: %w", err)
	}
	if expectedSHA256 == "" || pkg.SHA256 != expectedSHA256 {
		return skills.Package{}, errors.New("discovered skill hash changed before adoption")
	}
	return pkg, nil
}

func MarkAdoptedSkill(paths Paths, runtimeKind, discoveredPath, expectedSHA256 string, marker AdoptedSkillMarker) error {
	_, target, err := validateDiscoveredSkillPath(paths.RuntimeRoots[runtimeKind], discoveredPath)
	if err != nil {
		return err
	}
	if _, err := PackageDiscoveredSkill(paths, runtimeKind, target, expectedSHA256); err != nil {
		if body, readErr := os.ReadFile(filepath.Join(target, ".toolhub-managed.json")); readErr == nil {
			var existing AdoptedSkillMarker
			if json.Unmarshal(body, &existing) == nil && existing.SHA256 == expectedSHA256 && existing.VersionID == marker.VersionID {
				return nil
			}
		}
		return err
	}
	marker.SHA256 = expectedSHA256
	marker.ManagedAt = time.Now().UTC()
	body, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	path := filepath.Join(target, ".toolhub-managed.json")
	temporary := path + ".new-" + uuid.NewString()
	if err := os.WriteFile(temporary, body, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func validateDiscoveredSkillPath(root, discoveredPath string) (string, string, error) {
	if root == "" || discoveredPath == "" {
		return "", "", errors.New("runtime root and discovered path are required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	targetAbs, err := filepath.Abs(discoveredPath)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("discovered skill path escapes the runtime root")
	}
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if part == ".system" {
			return "", "", errors.New("protected .system skills cannot be adopted")
		}
	}
	return rootAbs, targetAbs, nil
}

func rejectSymlinks(root, target string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(resolvedRoot) != filepath.Clean(root) {
		return errors.New("runtime root symlinks are not allowed during adoption")
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil || filepath.Clean(resolvedTarget) != filepath.Clean(target) {
		return errors.New("discovered skill symlinks are not allowed")
	}
	return filepath.WalkDir(target, func(current string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed: %s", current)
		}
		return nil
	})
}
