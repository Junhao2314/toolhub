package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func TestHermesScanRecursesClassifiesAndHidesPaths(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".hermes", "skills")
	writeHermesSkill(t, filepath.Join(root, "hub", "one"), "Hub Skill")
	writeHermesSkill(t, filepath.Join(root, "local"), "Same Skill")
	writeHermesSkill(t, filepath.Join(root, "other"), "Same Skill")
	if err := os.MkdirAll(filepath.Join(root, "hub", "one", ".hub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".archive", "ignored"), 0700); err != nil {
		t.Fatal(err)
	}
	writeHermesSkill(t, filepath.Join(root, ".archive", "ignored"), "Hidden")
	if err := os.WriteFile(filepath.Join(root, "hub", "one", ".hub", "lock.json"), []byte(`{"provider":"Skills Hub","trust":"verified"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "local", ".bundled_manifest"), []byte(`{"name":"builtin"}`), 0600); err != nil {
		t.Fatal(err)
	}
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	scan, err := NewManager(t.TempDir()).ScanHermes(user)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Members) != 3 {
		t.Fatalf("members=%+v", scan.Members)
	}
	for _, member := range scan.Members {
		if strings.Contains(member.ID, "/") || strings.Contains(member.ID, "local") || strings.Contains(member.ID, "hub") {
			t.Fatalf("Hermes member ID exposes location: %+v", member)
		}
		if member.Slug == "hub-skill" && (member.Source != "hub" || member.Provider != "Skills Hub" || member.Trust != "verified") {
			t.Fatalf("hub metadata=%+v", member)
		}
		if member.Slug == "same-skill" && !member.Shadowed {
			t.Fatalf("duplicate Skill was not shadowed: %+v", scan.Members)
		}
	}
}

func TestHermesScanDoesNotFollowSymlinkAndBatchRechecksRevision(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".hermes", "skills")
	writeHermesSkill(t, filepath.Join(root, "valid"), "Valid")
	outside := t.TempDir()
	writeHermesSkill(t, filepath.Join(outside, "outside"), "Outside")
	if err := os.Symlink(filepath.Join(outside, "outside"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	manager := NewManager(t.TempDir())
	scan, err := manager.ScanHermes(user)
	if err != nil || len(scan.Members) != 2 {
		t.Fatalf("scan=%+v err=%v", scan, err)
	}
	var valid bridgeprotocol.InventoryMember
	for _, member := range scan.Members {
		if member.Slug == "valid" {
			valid = member
		}
		if member.Name == "linked" && member.Importable {
			t.Fatal("symlink candidate became importable")
		}
	}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeHermes, ManagedUsername: user.Name}
	response, err := manager.ExportLocalSkillBatch(user, bridgeprotocol.LocalSkillBatchExportRequest{Target: target, ExpectedRevision: scan.Revision, Items: []bridgeprotocol.LocalSkillBatchItem{{ID: valid.ID, ContentHash: valid.ContentHash}}})
	if err != nil || len(response.Items) != 1 || response.Items[0].Status != "Exported" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if err := os.WriteFile(filepath.Join(root, "valid", "SKILL.md"), []byte("---\nname: Valid\ndescription: changed\n---\nbody\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = manager.ExportLocalSkillBatch(user, bridgeprotocol.LocalSkillBatchExportRequest{Target: target, ExpectedRevision: scan.Revision, Items: []bridgeprotocol.LocalSkillBatchItem{{ID: valid.ID, ContentHash: valid.ContentHash}}})
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrRevisionConflict {
		t.Fatalf("stale batch error=%v", err)
	}
}

func writeHermesSkill(t *testing.T, root, name string) {
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + strings.ToLower(strings.ReplaceAll(name, " ", "-")) + "\ndescription: test\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
