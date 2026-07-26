package store

import (
	"testing"
	"time"
)

func TestCaptureGrantRejectsExpiryReplayAndIdentityMismatch(t *testing.T) {
	now := time.Now().UTC()
	valid := captureGrant{NodeID: "node-a", Runtime: "codex", Name: "example", Identity: "identity", ExpiresAt: now.Add(time.Minute)}
	if err := valid.validate(now, "node-a", "codex", "example", "identity"); err != nil {
		t.Fatalf("valid grant rejected: %v", err)
	}
	tests := []captureGrant{
		{NodeID: "node-a", Runtime: "codex", Name: "example", Identity: "identity", ExpiresAt: now},
		{NodeID: "node-a", Runtime: "codex", Name: "example", Identity: "identity", ExpiresAt: now.Add(time.Minute), Used: true},
	}
	for _, grant := range tests {
		if err := grant.validate(now, "node-a", "codex", "example", "identity"); err == nil {
			t.Fatal("invalid grant was accepted")
		}
	}
	if err := valid.validate(now, "node-b", "codex", "example", "identity"); err == nil {
		t.Fatal("cross-node capture token was accepted")
	}
	if err := valid.validate(now, "node-a", "codex", "other", "identity"); err == nil {
		t.Fatal("identity mismatch was accepted")
	}
}
