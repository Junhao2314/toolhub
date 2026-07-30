package runtime

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/skills"
)

const (
	maxManagedRootBytes = 256 << 20
	maxManagedFiles     = 10000
)

type Manager struct {
	BackupRoot string
	now        func() time.Time
}

func NewManager(backupRoot string) *Manager {
	return &Manager{BackupRoot: backupRoot, now: time.Now}
}

type ScanResult struct {
	Revision string
	Members  []bridgeprotocol.InventoryMember
}

func (m *Manager) Scan(user ManagedUser, runtimeKind string) (ScanResult, error) {
	paths, err := PathsFor(user, runtimeKind)
	if err != nil {
		return ScanResult{}, err
	}
	if runtimeKind == bridgeprotocol.RuntimeSharedRelay {
		return m.scanRelay(paths)
	}
	if err := rejectSymlinkComponents(user.Home, paths.SkillsRoot); err != nil {
		return ScanResult{}, err
	}
	return scanSkillRoot(paths.SkillsRoot)
}

func scanSkillRoot(root string) (ScanResult, error) {
	if info, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return hashInventory(nil), nil
	} else if err != nil {
		return ScanResult{}, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ScanResult{}, errors.New("runtime Skill root must be a real directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ScanResult{}, err
	}
	members := make([]bridgeprotocol.InventoryMember, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		protected := bridgeprotocol.IsProtectedSkillEntry(name)
		info, err := entry.Info()
		if err != nil {
			return ScanResult{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeType != 0 && !info.IsDir() {
			members = append(members, bridgeprotocol.InventoryMember{ID: "skill:" + name, Kind: "skill", Name: name, Protected: true, Scope: "user"})
			continue
		}
		if !entry.IsDir() {
			members = append(members, bridgeprotocol.InventoryMember{ID: "entry:" + name, Kind: "entry", Name: name, Protected: true, Scope: "user"})
			continue
		}
		hash := ""
		if !protected {
			pkg, scanErr := skills.ScanDirectory(filepath.Join(root, name), skills.DefaultLimits)
			if scanErr != nil {
				protected = true
			} else {
				hash = pkg.ContentHash
			}
		}
		members = append(members, bridgeprotocol.InventoryMember{ID: "skill:" + name, Kind: "skill", Name: name, ContentHash: hash, Protected: protected, Scope: "user"})
	}
	return hashInventory(members), nil
}

func hashInventory(members []bridgeprotocol.InventoryMember) ScanResult {
	if members == nil {
		members = []bridgeprotocol.InventoryMember{}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	body, _ := json.Marshal(members)
	sum := sha256.Sum256(body)
	return ScanResult{Revision: hex.EncodeToString(sum[:]), Members: members}
}

func (m *Manager) Preflight(user ManagedUser, manifest bridgeprotocol.DesiredManifest) (bridgeprotocol.PreflightResponse, error) {
	scan, err := m.Scan(user, manifest.Target.Runtime)
	if err != nil {
		return bridgeprotocol.PreflightResponse{}, err
	}
	_, hash, err := manifest.Canonical()
	if err != nil {
		return bridgeprotocol.PreflightResponse{}, err
	}
	diff := bridgeprotocol.Diff{Add: []bridgeprotocol.DiffItem{}, Replace: []bridgeprotocol.DiffItem{}, Delete: []bridgeprotocol.DiffItem{}, Excluded: []bridgeprotocol.DiffItem{}}
	desired := map[string]bridgeprotocol.SkillMember{}
	for _, skill := range manifest.Skills {
		desired[skill.Slug] = skill
	}
	for _, member := range scan.Members {
		if member.Protected {
			diff.Excluded = append(diff.Excluded, bridgeprotocol.DiffItem{Kind: member.Kind, Name: member.Name, Reason: "protected"})
			continue
		}
		if member.Kind == "skill" {
			if skill, ok := desired[member.Name]; !ok {
				diff.Delete = append(diff.Delete, bridgeprotocol.DiffItem{Kind: "skill", Name: member.Name})
			} else if skill.ContentHash != member.ContentHash {
				diff.Replace = append(diff.Replace, bridgeprotocol.DiffItem{Kind: "skill", MemberID: skill.MemberID, Name: member.Name})
			}
			delete(desired, member.Name)
		}
	}
	for _, skill := range desired {
		diff.Add = append(diff.Add, bridgeprotocol.DiffItem{Kind: "skill", MemberID: skill.MemberID, Name: skill.Slug})
	}
	sortDiff(&diff)
	return bridgeprotocol.PreflightResponse{TargetRevision: scan.Revision, ManifestHash: hash, Diff: diff}, nil
}

func (m *Manager) Apply(user ManagedUser, request bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	if request.Target.Runtime == bridgeprotocol.RuntimeSharedRelay {
		return bridgeprotocol.TargetResult{}, errors.New("shared relay Apply is handled by the relay manager")
	}
	paths, err := PathsFor(user, request.Target.Runtime)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	current, err := scanSkillRoot(paths.SkillsRoot)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	if current.Revision != request.ExpectedRevision {
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Target changed after preflight"}
	}
	backup, err := m.backupRoot(user, request.Target, paths.SkillsRoot, request.OperationID, current.Revision)
	if err != nil {
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrBackup, Message: "Could not back up target before Apply"}
	}
	if err := mirrorSkillRoot(user, paths.SkillsRoot, request.Manifest, request.Artifacts, false); err != nil {
		_ = m.restoreRoot(user, paths.SkillsRoot, backup.Path)
		return bridgeprotocol.TargetResult{}, err
	}
	after, err := scanSkillRoot(paths.SkillsRoot)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: after.Revision, BackupID: backup.Backup.ID, Manifest: &request.Manifest}, nil
}

func (m *Manager) Reconcile(user ManagedUser, request bridgeprotocol.ReconcileRequest) (bridgeprotocol.TargetResult, error) {
	if request.Target.Runtime == bridgeprotocol.RuntimeSharedRelay {
		return bridgeprotocol.TargetResult{}, errors.New("shared relay reconcile is handled by the relay manager")
	}
	paths, err := PathsFor(user, request.Target.Runtime)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	current, err := scanSkillRoot(paths.SkillsRoot)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	drifted := managedDrift(current.Members, request.Manifest.Skills)
	if !drifted {
		return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: current.Revision, Repaired: false}, nil
	}
	backup, err := m.backupRoot(user, request.Target, paths.SkillsRoot, request.OperationID, current.Revision)
	if err != nil {
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrBackup, Message: "Could not back up target before repair"}
	}
	if err := mirrorSkillRoot(user, paths.SkillsRoot, request.Manifest, request.Artifacts, true); err != nil {
		_ = m.restoreRoot(user, paths.SkillsRoot, backup.Path)
		return bridgeprotocol.TargetResult{}, err
	}
	after, err := scanSkillRoot(paths.SkillsRoot)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: after.Revision, BackupID: backup.Backup.ID, Repaired: true}, nil
}

func managedDrift(current []bridgeprotocol.InventoryMember, desired []bridgeprotocol.SkillMember) bool {
	byName := map[string]bridgeprotocol.InventoryMember{}
	for _, item := range current {
		byName[item.Name] = item
	}
	for _, skill := range desired {
		item, ok := byName[skill.Slug]
		if !ok || item.Protected || item.ContentHash != skill.ContentHash {
			return true
		}
	}
	return false
}

func mirrorSkillRoot(user ManagedUser, root string, manifest bridgeprotocol.DesiredManifest, artifacts []bridgeprotocol.Artifact, preserveUnmanaged bool) error {
	artifactByVersion := map[string]bridgeprotocol.Artifact{}
	for _, artifact := range artifacts {
		artifactByVersion[artifact.VersionID] = artifact
	}
	parent := filepath.Dir(root)
	if err := rejectSymlinkComponents(user.Home, parent); err != nil {
		return err
	}
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	if err := rejectSymlinkComponents(user.Home, parent); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".toolhub-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := os.Chown(stage, user.UID, user.GID); err != nil && os.Geteuid() == 0 {
		return err
	}
	if currentInfo, statErr := os.Stat(root); statErr == nil {
		if err := copyRootForStage(root, stage, preserveUnmanaged); err != nil {
			return err
		}
		if err := os.Chmod(stage, currentInfo.Mode().Perm()); err != nil {
			return err
		}
	}
	desiredNames := map[string]bool{}
	for _, skill := range manifest.Skills {
		desiredNames[skill.Slug] = true
		artifact, ok := artifactByVersion[skill.VersionID]
		if !ok || artifact.SHA256 != skill.SHA256 {
			return fmt.Errorf("artifact for Skill %q is missing or mismatched", skill.Slug)
		}
		target := filepath.Join(stage, skill.Slug)
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := extractSkillArtifact(target, artifact, user); err != nil {
			return err
		}
	}
	if !preserveUnmanaged {
		entries, _ := os.ReadDir(stage)
		for _, entry := range entries {
			if bridgeprotocol.IsProtectedSkillEntry(entry.Name()) || desiredNames[entry.Name()] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(stage, entry.Name())); err != nil {
				return err
			}
		}
	}
	if _, err := scanSkillRoot(stage); err != nil {
		return fmt.Errorf("validate staged Skill root: %w", err)
	}
	return atomicReplaceDirectory(root, stage)
}

func copyRootForStage(root, stage string, preserveAll bool) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var files int
	var bytesCopied int64
	for _, entry := range entries {
		if !preserveAll && !bridgeprotocol.IsProtectedSkillEntry(entry.Name()) {
			continue
		}
		if err := copyTree(filepath.Join(root, entry.Name()), filepath.Join(stage, entry.Name()), &files, &bytesCopied); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(source, target string, files *int, bytesCopied *int64) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeType != 0 && !info.IsDir() {
		return fmt.Errorf("unsafe file type in managed root: %s", filepath.Base(source))
	}
	if info.IsDir() {
		if err := os.Mkdir(target, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name()), files, bytesCopied); err != nil {
				return err
			}
		}
		return nil
	}
	*files++
	*bytesCopied += info.Size()
	if *files > maxManagedFiles || *bytesCopied > maxManagedRootBytes {
		return errors.New("managed root exceeds copy safety limits")
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	targetFile, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(targetFile, io.LimitReader(sourceFile, skills.DefaultLimits.MaxFileBytes+1)); err != nil {
		targetFile.Close()
		return err
	}
	if err := targetFile.Sync(); err != nil {
		targetFile.Close()
		return err
	}
	return targetFile.Close()
}

func extractSkillArtifact(target string, artifact bridgeprotocol.Artifact, user ManagedUser) error {
	pkg, err := skills.ScanZIP(artifact.Archive, skills.DefaultLimits)
	if err != nil {
		return err
	}
	if pkg.SHA256 != artifact.SHA256 {
		return errors.New("artifact canonical SHA-256 mismatch")
	}
	reader, err := zip.NewReader(bytes.NewReader(pkg.CanonicalZIP), int64(len(pkg.CanonicalZIP)))
	if err != nil {
		return err
	}
	if err := os.Mkdir(target, 0755); err != nil {
		return err
	}
	for _, item := range reader.File {
		if item.FileInfo().IsDir() {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(item.Name))
		if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || item.Mode()&os.ModeSymlink != 0 {
			return errors.New("artifact contains an unsafe path or symlink")
		}
		destination := filepath.Join(target, clean)
		if err := ensureWithin(target, destination); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		reader, err := item.Open()
		if err != nil {
			return err
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, item.Mode().Perm())
		if err != nil {
			reader.Close()
			return err
		}
		written, copyErr := io.Copy(file, io.LimitReader(reader, skills.DefaultLimits.MaxFileBytes+1))
		reader.Close()
		if copyErr != nil || written > skills.DefaultLimits.MaxFileBytes {
			file.Close()
			return errors.New("artifact file exceeds safety limit")
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	if err := chownTree(target, user.UID, user.GID); err != nil && os.Geteuid() == 0 {
		return err
	}
	return nil
}

type backupRecord struct {
	Backup bridgeprotocol.Backup
	Path   string
}

func (m *Manager) backupRoot(user ManagedUser, target bridgeprotocol.Target, root, operationID, revision string) (backupRecord, error) {
	if strings.TrimSpace(m.BackupRoot) == "" {
		return backupRecord{}, errors.New("backup root is required")
	}
	id := uuid.NewString()
	destination := filepath.Join(m.BackupRoot, target.ID, id)
	if err := ensureWithin(m.BackupRoot, destination); err != nil {
		return backupRecord{}, err
	}
	if err := rejectSymlinkComponents(user.Home, root); err != nil {
		return backupRecord{}, err
	}
	if err := rejectSymlinkParent(destination); err != nil {
		return backupRecord{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return backupRecord{}, err
	}
	if info, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(destination, 0700); err != nil {
			return backupRecord{}, err
		}
	} else if err != nil {
		return backupRecord{}, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return backupRecord{}, errors.New("cannot back up unsafe runtime root")
	} else {
		var files int
		var copied int64
		if err := copyTree(root, destination, &files, &copied); err != nil {
			return backupRecord{}, err
		}
	}
	backup := bridgeprotocol.Backup{ID: id, TargetID: target.ID, NodeKind: target.NodeKind, SaltMinionID: target.SaltMinionID, Runtime: target.Runtime, SourceOperationID: operationID, Revision: revision, CreatedAt: m.now().UTC()}
	return backupRecord{Backup: backup, Path: destination}, nil
}

func (m *Manager) Restore(user ManagedUser, request bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	target := request.Target
	paths, err := PathsFor(user, target.Runtime)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	current, err := m.Scan(user, target.Runtime)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	if request.ExpectedRevision != "" && current.Revision != request.ExpectedRevision {
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Target changed before restore"}
	}
	backupPath := filepath.Join(m.BackupRoot, target.ID, request.BackupID)
	if err := ensureWithin(filepath.Join(m.BackupRoot, target.ID), backupPath); err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	recovery, err := m.backupRoot(user, target, paths.SkillsRoot, request.OperationID, current.Revision)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	if err := m.restoreRoot(user, paths.SkillsRoot, backupPath); err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	after, err := m.Scan(user, target.Runtime)
	if err != nil {
		_ = m.restoreRoot(user, paths.SkillsRoot, recovery.Path)
		return bridgeprotocol.TargetResult{}, err
	}
	if managedDrift(after.Members, request.Manifest.Skills) {
		_ = m.restoreRoot(user, paths.SkillsRoot, recovery.Path)
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrBackup, Message: "Restored Skills do not match the pinned backup manifest"}
	}
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: after.Revision, BackupID: recovery.Backup.ID, Manifest: &request.Manifest}, nil
}

func (m *Manager) restoreRoot(user ManagedUser, root, backupPath string) error {
	parent := filepath.Dir(root)
	if err := rejectSymlinkComponents(user.Home, parent); err != nil {
		return err
	}
	if err := rejectSymlinkParent(filepath.Join(backupPath, "entry")); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".toolhub-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	entries, err := os.ReadDir(backupPath)
	if err != nil {
		return err
	}
	var files int
	var copied int64
	for _, entry := range entries {
		if err := copyTree(filepath.Join(backupPath, entry.Name()), filepath.Join(stage, entry.Name()), &files, &copied); err != nil {
			return err
		}
	}
	if err := chownTree(stage, user.UID, user.GID); err != nil && os.Geteuid() == 0 {
		return err
	}
	return atomicReplaceDirectory(root, stage)
}

func (m *Manager) RemoveBackup(backup bridgeprotocol.Backup) error {
	path := filepath.Join(m.BackupRoot, backup.TargetID, backup.ID)
	if err := ensureWithin(filepath.Join(m.BackupRoot, backup.TargetID), path); err != nil {
		return err
	}
	if err := rejectSymlinkParent(path); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func atomicReplaceDirectory(target, stage string) error {
	parent := filepath.Dir(target)
	old := filepath.Join(parent, ".toolhub-old-"+uuid.NewString())
	targetExists := false
	if _, err := os.Lstat(target); err == nil {
		targetExists = true
		if err := os.Rename(target, old); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		if targetExists {
			_ = os.Rename(old, target)
		}
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrAtomicWrite, Message: "Atomic target replacement failed"}
	}
	if directory, err := os.Open(parent); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	if targetExists {
		return os.RemoveAll(old)
	}
	return nil
}

func rejectSymlinkComponents(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || strings.HasPrefix(relative, "..") {
		return errors.New("target path escapes managed home")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed path contains a symlink")
		}
	}
	return nil
}

func chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink encountered during ownership update")
		}
		return os.Chown(path, uid, gid)
	})
}

func sortDiff(diff *bridgeprotocol.Diff) {
	less := func(items []bridgeprotocol.DiffItem) {
		sort.Slice(items, func(i, j int) bool {
			if items[i].Kind != items[j].Kind {
				return items[i].Kind < items[j].Kind
			}
			return items[i].Name < items[j].Name
		})
	}
	less(diff.Add)
	less(diff.Replace)
	less(diff.Delete)
	less(diff.Excluded)
}

func (m *Manager) scanRelay(paths TargetPaths) (ScanResult, error) {
	members, err := ScanSharedRelay(paths)
	if err != nil {
		return ScanResult{}, err
	}
	return hashInventory(members), nil
}

var _ = syscall.Stat_t{}
