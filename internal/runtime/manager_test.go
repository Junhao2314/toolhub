package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/skills"
)

func TestSkillApplyAndRestorePreserveProtectedSymlink(t *testing.T) {
	home := t.TempDir()
	backupRoot := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: user.Name}
	runtimeRoot := filepath.Join(home, ".claude", "skills")
	managedRoot := filepath.Join(runtimeRoot, "managed")
	writeSkillDirectory(t, managedRoot, "Managed")
	outsideRoot := filepath.Join(home, ".agents", "skills", "ast-grep")
	writeSkillDirectory(t, outsideRoot, "Protected")
	linkTarget := filepath.Join("..", "..", ".agents", "skills", "ast-grep")
	if err := os.Symlink(linkTarget, filepath.Join(runtimeRoot, "ast-grep")); err != nil {
		t.Fatal(err)
	}
	pkg, err := skills.ScanDirectory(managedRoot, skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	memberID, skillID, versionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	manifest := bridgeprotocol.DesiredManifest{
		SchemaVersion:    bridgeprotocol.ManifestSchemaVersion,
		Target:           target,
		Skills:           []bridgeprotocol.SkillMember{{MemberID: memberID, SkillID: skillID, VersionID: versionID, Slug: "managed", SHA256: pkg.SHA256, ContentHash: pkg.ContentHash}},
		MCPServers:       []bridgeprotocol.MCPMember{},
		ManagedMemberIDs: []string{memberID},
	}
	manager := NewManager(backupRoot)
	before, err := manager.Scan(user, target.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Apply(user, bridgeprotocol.CommitRequest{
		OperationID:      uuid.NewString(),
		OperationKind:    "apply",
		Target:           target,
		ExpectedRevision: before.Revision,
		Manifest:         manifest,
		Artifacts:        []bridgeprotocol.Artifact{{VersionID: versionID, SHA256: pkg.SHA256, Archive: pkg.CanonicalZIP}},
	})
	if err != nil || result.BackupID == "" {
		t.Fatalf("Apply result=%+v err=%v", result, err)
	}
	requireSymlink(t, filepath.Join(runtimeRoot, "ast-grep"), linkTarget)
	requireSymlink(t, filepath.Join(backupRoot, target.ID, result.BackupID, "ast-grep"), linkTarget)

	current, err := manager.Scan(user, target.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := manager.Restore(user, bridgeprotocol.CommitRequest{
		OperationID:      uuid.NewString(),
		OperationKind:    "restore",
		Target:           target,
		ExpectedRevision: current.Revision,
		BackupID:         result.BackupID,
		Manifest:         manifest,
	})
	if err != nil || restored.BackupID == "" {
		t.Fatalf("Restore result=%+v err=%v", restored, err)
	}
	requireSymlink(t, filepath.Join(runtimeRoot, "ast-grep"), linkTarget)
}

func TestBackupRootRemovesPartialDestinationOnFailure(t *testing.T) {
	home := t.TempDir()
	backupRoot := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: user.Name}
	runtimeRoot := filepath.Join(home, ".claude", "skills")
	writeSkillDirectory(t, filepath.Join(runtimeRoot, "a-skill"), "Safe")
	if err := syscall.Mkfifo(filepath.Join(runtimeRoot, "z-fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(backupRoot)
	if _, err := manager.backupRoot(user, target, runtimeRoot, uuid.NewString(), strings.Repeat("a", 64)); err == nil {
		t.Fatal("backup unexpectedly accepted a FIFO")
	}
	entries, err := os.ReadDir(filepath.Join(backupRoot, target.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed backup left %d partial directories", len(entries))
	}
}

func TestSkillPreflightReportsProtectedRegularFileWithMaxLinuxName(t *testing.T) {
	home := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: user.Name}
	runtimeRoot := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(runtimeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("x", 255)
	if err := os.WriteFile(filepath.Join(runtimeRoot, name), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := bridgeprotocol.DesiredManifest{
		SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: target,
		Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{},
	}

	result, err := NewManager(t.TempDir()).Preflight(user, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diff.Excluded) != 1 || result.Diff.Excluded[0].Kind != "entry" || result.Diff.Excluded[0].Name != name || result.Diff.Excluded[0].Reason != "protected" {
		t.Fatalf("protected regular file diff=%+v", result.Diff)
	}
}

func TestSkillModesRoundTripUnderRestrictiveUmask(t *testing.T) {
	home := t.TempDir()
	backupRoot := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: user.Name}
	artifactRoot := filepath.Join(home, "artifact")
	writeSkillDirectory(t, artifactRoot, "Mode fixture")
	if err := os.Chmod(filepath.Join(artifactRoot, "SKILL.md"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(artifactRoot, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "bin", "run.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	pkg, err := skills.ScanDirectory(artifactRoot, skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	memberID, skillID, versionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	manifest := bridgeprotocol.DesiredManifest{
		SchemaVersion:    bridgeprotocol.ManifestSchemaVersion,
		Target:           target,
		Skills:           []bridgeprotocol.SkillMember{{MemberID: memberID, SkillID: skillID, VersionID: versionID, Slug: "mode-fixture", SHA256: pkg.SHA256, ContentHash: pkg.ContentHash}},
		MCPServers:       []bridgeprotocol.MCPMember{},
		ManagedMemberIDs: []string{memberID},
	}
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}

	previousUmask := syscall.Umask(0077)
	t.Cleanup(func() { syscall.Umask(previousUmask) })
	manager := NewManager(backupRoot)
	before, err := manager.Scan(user, target.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Apply(user, bridgeprotocol.CommitRequest{
		OperationID:      uuid.NewString(),
		OperationKind:    "apply",
		Target:           target,
		ExpectedRevision: before.Revision,
		Manifest:         manifest,
		Artifacts:        []bridgeprotocol.Artifact{{VersionID: versionID, SHA256: pkg.SHA256, Archive: pkg.CanonicalZIP}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(home, ".claude", "skills")
	requireMode(t, runtimeRoot, 0755)
	requireMode(t, filepath.Join(runtimeRoot, "mode-fixture", "SKILL.md"), 0644)
	requireMode(t, filepath.Join(runtimeRoot, "mode-fixture", "bin", "run.sh"), 0755)
	requireZeroSkillDiff(t, manager, user, manifest)

	selected, err := manager.backupRoot(user, target, runtimeRoot, uuid.NewString(), result.TargetRevision)
	if err != nil {
		t.Fatal(err)
	}
	requireMode(t, selected.Path, 0755)
	requireMode(t, filepath.Join(selected.Path, "mode-fixture", "SKILL.md"), 0644)
	if err := os.Chmod(filepath.Join(runtimeRoot, "mode-fixture", "SKILL.md"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := manager.Scan(user, target.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Revision == result.TargetRevision {
		t.Fatal("mode-only drift did not change the target revision")
	}
	restored, err := manager.Restore(user, bridgeprotocol.CommitRequest{
		OperationID:      uuid.NewString(),
		OperationKind:    "restore",
		Target:           target,
		ExpectedRevision: changed.Revision,
		BackupID:         selected.Backup.ID,
		Manifest:         manifest,
	})
	if err != nil || restored.BackupID == "" {
		t.Fatalf("Restore result=%+v err=%v", restored, err)
	}
	requireMode(t, runtimeRoot, 0755)
	requireMode(t, filepath.Join(runtimeRoot, "mode-fixture", "SKILL.md"), 0644)
	requireMode(t, filepath.Join(runtimeRoot, "mode-fixture", "bin", "run.sh"), 0755)
	requireZeroSkillDiff(t, manager, user, manifest)
}

func TestSkillRestorePinsVerifiedManagedContent(t *testing.T) {
	home := t.TempDir()
	backupRoot := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: user.Name}
	currentRoot := filepath.Join(home, ".claude", "skills")
	writeSkillDirectory(t, filepath.Join(currentRoot, "current"), "Current")
	selected := filepath.Join(backupRoot, target.ID, "selected")
	writeSkillDirectory(t, filepath.Join(selected, "restored"), "Restored")
	pkg, err := skills.ScanDirectory(filepath.Join(selected, "restored"), skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	memberID, skillID, versionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: target, Skills: []bridgeprotocol.SkillMember{{MemberID: memberID, SkillID: skillID, VersionID: versionID, Slug: "restored", SHA256: pkg.SHA256, ContentHash: pkg.ContentHash}}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{memberID}}
	manager := NewManager(backupRoot)
	before, err := manager.Scan(user, target.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Restore(user, bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "restore", Target: target, ExpectedRevision: before.Revision, BackupID: "selected", Manifest: manifest})
	if err != nil || result.Manifest == nil || result.BackupID == "" {
		t.Fatalf("Restore result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(currentRoot, "restored", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(currentRoot, "current")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old Skill remained after restore: %v", err)
	}
}

func TestSkillRestoreRollsBackManifestMismatch(t *testing.T) {
	home := t.TempDir()
	backupRoot := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: user.Name}
	currentRoot := filepath.Join(home, ".claude", "skills")
	writeSkillDirectory(t, filepath.Join(currentRoot, "current"), "Current")
	writeSkillDirectory(t, filepath.Join(backupRoot, target.ID, "selected", "unexpected"), "Unexpected")
	memberID := uuid.NewString()
	manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: target, Skills: []bridgeprotocol.SkillMember{{MemberID: memberID, SkillID: uuid.NewString(), VersionID: uuid.NewString(), Slug: "expected", SHA256: strings.Repeat("a", 64), ContentHash: strings.Repeat("b", 64)}}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{memberID}}
	manager := NewManager(backupRoot)
	before, err := manager.Scan(user, target.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Restore(user, bridgeprotocol.CommitRequest{OperationID: uuid.NewString(), OperationKind: "restore", Target: target, ExpectedRevision: before.Revision, BackupID: "selected", Manifest: manifest})
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrBackup {
		t.Fatalf("Restore error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(currentRoot, "current", "SKILL.md")); err != nil {
		t.Fatalf("current Skill was not rolled back: %v", err)
	}
	if _, err := os.Stat(filepath.Join(currentRoot, "unexpected")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched restored Skill remained: %v", err)
	}
}

func writeSkillDirectory(t *testing.T, directory, name string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nname: " + name + "\ndescription: fixture\n---\nbody\n")
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func requireSymlink(t *testing.T, path, target string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink: info=%v err=%v", path, info, err)
	}
	actual, err := os.Readlink(path)
	if err != nil || actual != target {
		t.Fatalf("symlink %s target=%q want=%q err=%v", path, actual, target, err)
	}
}

func requireMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != want {
		t.Fatalf("mode %s=%04o want=%04o err=%v", path, infoMode(info), want, err)
	}
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

func requireZeroSkillDiff(t *testing.T, manager *Manager, user ManagedUser, manifest bridgeprotocol.DesiredManifest) {
	t.Helper()
	preflight, err := manager.Preflight(user, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(preflight.Diff.Add) != 0 || len(preflight.Diff.Replace) != 0 || len(preflight.Diff.Delete) != 0 {
		t.Fatalf("preflight diff=%+v", preflight.Diff)
	}
}
