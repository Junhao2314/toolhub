package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/skills"
)

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
