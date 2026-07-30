package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func TestLocalMCPPreviewRedactsValuesAndCaptureIsRevisionBound(t *testing.T) {
	home := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: user.Name}
	config := filepath.Join(home, ".claude.json")
	body := []byte(`{"mcpServers":{"private":{"command":"tool","args":["serve"],"env":{"TOKEN":"secret-value"}}}}`)
	if err := os.WriteFile(config, body, 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(t.TempDir())
	preview, err := manager.PreviewLocalMCP(user, target)
	if err != nil || len(preview.Items) != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	encoded, _ := json.Marshal(preview)
	if bytes.Contains(encoded, []byte("secret-value")) || !bytes.Contains(encoded, []byte("TOKEN")) {
		t.Fatalf("preview leaked or omitted secret key: %s", encoded)
	}
	item := preview.Items[0]
	captured, err := manager.CaptureLocalMCP(user, bridgeprotocol.LocalMCPCaptureRequest{Target: target, ExpectedRevision: preview.TargetRevision, Name: item.Name, ContentHash: item.ContentHash})
	if err != nil || captured.Env["TOKEN"] != "secret-value" {
		t.Fatalf("captured=%+v err=%v", captured.Preview, err)
	}
	if err := os.WriteFile(config, append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = manager.CaptureLocalMCP(user, bridgeprotocol.LocalMCPCaptureRequest{Target: target, ExpectedRevision: preview.TargetRevision, Name: item.Name, ContentHash: item.ContentHash})
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrRevisionConflict {
		t.Fatalf("stale capture error=%v", err)
	}
}

func TestLocalIntakeRejectsRuntimeParentSymlink(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".codex")); err != nil {
		t.Fatal(err)
	}
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeCodex, ManagedUsername: user.Name}
	manager := NewManager(t.TempDir())
	if _, err := manager.Scan(user, target.Runtime); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Skill scan error=%v", err)
	}
	if _, err := manager.PreviewLocalMCP(user, target); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("MCP preview error=%v", err)
	}
}

func TestExportLocalSkillRejectsRevisionChange(t *testing.T) {
	home := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	target := bridgeprotocol.Target{ID: uuid.NewString(), NodeID: uuid.NewString(), NodeKind: bridgeprotocol.NodeKindLocal, Runtime: bridgeprotocol.RuntimeClaude, ManagedUsername: user.Name}
	skillRoot := filepath.Join(home, ".claude", "skills", "example")
	if err := os.MkdirAll(skillRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: Example\ndescription: test\n---\nbody\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(t.TempDir())
	scan, err := manager.Scan(user, target.Runtime)
	if err != nil || len(scan.Members) != 1 || scan.Members[0].ContentHash == "" {
		t.Fatalf("scan=%+v err=%v", scan, err)
	}
	member := scan.Members[0]
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: Example\ndescription: changed\n---\nbody\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = manager.ExportLocalSkill(user, bridgeprotocol.LocalSkillExportRequest{Target: target, ExpectedRevision: scan.Revision, Name: member.Name, ContentHash: member.ContentHash})
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrRevisionConflict {
		t.Fatalf("stale export error=%v", err)
	}
}
