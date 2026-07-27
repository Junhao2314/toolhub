package runtime

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/skills"
)

type Deployer struct {
	DataDir       string
	Paths         Paths
	SharedSources []SharedSourceConfig
}

type DeployRequest struct {
	Runtime   string
	SkillSlug string
	VersionID string
	SHA256    string
	Enabled   bool
	Artifact  []byte
}

type DeployResult struct {
	ActualHash string `json:"actualHash"`
	BackupPath string `json:"backupPath,omitempty"`
	Changed    bool   `json:"changed"`
}

func (d *Deployer) Deploy(request DeployRequest) (DeployResult, error) {
	root := d.Paths.RuntimeRoots[request.Runtime]
	if root == "" {
		return DeployResult{}, errors.New("unknown runtime")
	}
	if request.SkillSlug == "" || request.SkillSlug == "." || request.SkillSlug == ".." || strings.ContainsAny(request.SkillSlug, `/\\`) || request.SkillSlug == ".system" {
		return DeployResult{}, errors.New("invalid or protected skill slug")
	}
	target := filepath.Join(root, request.SkillSlug)
	if request.Enabled {
		return d.enable(target, request)
	}
	return d.disable(target, request)
}

func (d *Deployer) enable(target string, request DeployRequest) (DeployResult, error) {
	pkg, err := skills.ScanZIP(request.Artifact, skills.DefaultLimits)
	if err != nil {
		return DeployResult{}, fmt.Errorf("validate artifact: %w", err)
	}
	if pkg.SHA256 != request.SHA256 {
		return DeployResult{}, errors.New("artifact SHA-256 does not match signed task")
	}
	cache := filepath.Join(d.DataDir, "artifacts", request.SHA256)
	if !fileExists(filepath.Join(cache, ".complete")) {
		if err := extractCache(pkg.CanonicalZIP, cache); err != nil {
			return DeployResult{}, err
		}
	}
	targetExists := false
	takeoverSharedLink := false
	if info, statErr := os.Lstat(target); statErr == nil {
		targetExists = true
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if !d.ownsLegacySharedLink(target, request.Runtime, request.SkillSlug) {
				return DeployResult{}, errors.New("target is an unowned symlink; refusing to replace it")
			}
			takeoverSharedLink = true
		case info.IsDir():
			if !fileExists(filepath.Join(target, ".toolhub-managed.json")) {
				return DeployResult{}, errors.New("target already contains an unmanaged skill; onboarding remains read-only")
			}
		default:
			return DeployResult{}, errors.New("target is not a managed skill directory")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return DeployResult{}, statErr
	}
	if !takeoverSharedLink && fileExists(filepath.Join(target, ".toolhub-managed.json")) {
		var marker struct {
			SHA256 string `json:"sha256"`
		}
		body, _ := os.ReadFile(filepath.Join(target, ".toolhub-managed.json"))
		if json.Unmarshal(body, &marker) == nil && marker.SHA256 == request.SHA256 {
			return DeployResult{ActualHash: request.SHA256, Changed: false}, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return DeployResult{}, err
	}
	staging := target + ".toolhub-new-" + uuid.NewString()
	if err := copyDirectory(cache, staging, map[string]bool{".complete": true}); err != nil {
		return DeployResult{}, err
	}
	marker, _ := json.Marshal(map[string]any{"sha256": request.SHA256, "versionId": request.VersionID, "managedAt": time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(staging, ".toolhub-managed.json"), marker, 0600); err != nil {
		_ = os.RemoveAll(staging)
		return DeployResult{}, err
	}
	backup := ""
	if targetExists {
		backup = filepath.Join(d.DataDir, "backups", request.Runtime, request.SkillSlug, time.Now().UTC().Format("20060102T150405Z")+"-"+uuid.NewString())
		if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
			_ = os.RemoveAll(staging)
			return DeployResult{}, err
		}
		if err := movePath(target, backup); err != nil {
			_ = os.RemoveAll(staging)
			return DeployResult{}, fmt.Errorf("backup current deployment: %w", err)
		}
	}
	if err := os.Rename(staging, target); err != nil {
		if backup != "" {
			_ = movePath(backup, target)
		}
		_ = os.RemoveAll(staging)
		return DeployResult{}, fmt.Errorf("activate deployment: %w", err)
	}
	return DeployResult{ActualHash: request.SHA256, BackupPath: backup, Changed: true}, nil
}

func (d *Deployer) ownsLegacySharedLink(target, runtimeKind, skillSlug string) bool {
	linkTarget, err := os.Readlink(target)
	if err != nil {
		return false
	}
	actualTarget := cleanLinkTarget(target, linkTarget)
	for _, source := range d.SharedSources {
		consumer, ok := source.Consumers[runtimeKind]
		if !ok || !samePath(consumer.SkillsPath, filepath.Dir(target)) {
			continue
		}
		state, err := loadSharedState(d.DataDir, source.Name)
		if err != nil {
			return false
		}
		record, ok := state.Links[runtimeKind][skillSlug]
		expectedTarget := filepath.Join(source.SkillsRoot, skillSlug)
		if ok && samePath(record.TargetPath, target) && samePath(record.ExpectedTarget, expectedTarget) && samePath(actualTarget, expectedTarget) {
			return true
		}
	}
	return false
}

func (d *Deployer) disable(target string, request DeployRequest) (DeployResult, error) {
	if len(request.SHA256) < 12 {
		return DeployResult{}, errors.New("invalid artifact SHA-256")
	}
	if !fileExists(target) {
		return DeployResult{ActualHash: request.SHA256, Changed: false}, nil
	}
	if !fileExists(filepath.Join(target, ".toolhub-managed.json")) {
		return DeployResult{}, errors.New("refusing to disable an unmanaged skill")
	}
	disabled := filepath.Join(filepath.Dir(target), ".toolhub-disabled", request.SkillSlug+"-"+request.SHA256[:12])
	if err := os.MkdirAll(filepath.Dir(disabled), 0700); err != nil {
		return DeployResult{}, err
	}
	if fileExists(disabled) {
		return DeployResult{}, errors.New("disabled target already exists")
	}
	if err := os.Rename(target, disabled); err != nil {
		return DeployResult{}, err
	}
	return DeployResult{ActualHash: request.SHA256, BackupPath: disabled, Changed: true}, nil
}

func extractCache(archive []byte, destination string) error {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return err
	}
	staging := destination + ".new-" + uuid.NewString()
	if err := os.MkdirAll(staging, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, file := range reader.File {
		name := filepath.FromSlash(file.Name)
		if filepath.IsAbs(name) || strings.HasPrefix(filepath.Clean(name), "..") || file.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe artifact entry")
		}
		target := filepath.Join(staging, name)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		source, err := file.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, file.Mode().Perm()&0755)
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(output, io.LimitReader(source, skills.DefaultLimits.MaxFileBytes+1))
		closeErr := output.Close()
		source.Close()
		if copyErr != nil || closeErr != nil {
			return errors.New("extract artifact")
		}
	}
	if err := os.WriteFile(filepath.Join(staging, ".complete"), []byte("ok\n"), 0600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	if fileExists(destination) {
		return nil
	}
	return os.Rename(staging, destination)
}

func copyDirectory(source, destination string, skip map[string]bool) error {
	return filepath.WalkDir(source, func(current string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, _ := filepath.Rel(source, current)
		if relative == "." {
			return os.MkdirAll(destination, 0700)
		}
		if skip[filepath.ToSlash(relative)] {
			return nil
		}
		target := filepath.Join(destination, relative)
		info, err := item.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("cache contains symlink")
		}
		if item.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		body, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, info.Mode().Perm())
	})
}

func movePath(source, destination string) error {
	return movePathWithRename(source, destination, os.Rename)
}

func movePathWithRename(source, destination string, rename func(string, string) error) error {
	if err := rename(source, destination); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	staging := destination + ".new-" + uuid.NewString()
	defer os.RemoveAll(staging)
	if err := copyPath(source, staging); err != nil {
		return fmt.Errorf("copy across filesystems: %w", err)
	}
	if err := os.Rename(staging, destination); err != nil {
		return fmt.Errorf("activate cross-filesystem copy: %w", err)
	}
	if err := removePath(source); err != nil {
		return fmt.Errorf("remove source after cross-filesystem copy (backup preserved at %s): %w", destination, err)
	}
	return nil
}

func copyPath(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(target, destination)
	}
	if info.IsDir() {
		if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPath(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return errors.New("deployment backup contains an unsupported file type")
	}

	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func removePath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}
