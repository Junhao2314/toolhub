package worker

import (
	"errors"
	"strings"
	"testing"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func TestProjectScanHealthDetectsPinnedDriftAndIgnoresUnmanagedExtras(t *testing.T) {
	matchingHash := strings.Repeat("a", 64)
	desiredMCPHash := strings.Repeat("b", 64)
	manifest := bridgeprotocol.DesiredManifest{
		Skills: []bridgeprotocol.SkillMember{
			{MemberID: "skill-match", Slug: "matching", ContentHash: matchingHash},
			{MemberID: "skill-missing", Slug: "missing", ContentHash: strings.Repeat("c", 64)},
		},
		MCPServers: []bridgeprotocol.MCPMember{{MemberID: "mcp-replace", Name: "search", ContentHash: desiredMCPHash}},
	}
	members := []bridgeprotocol.InventoryMember{
		{Kind: "skill", Name: "matching", ContentHash: matchingHash},
		{Kind: "skill", Name: "missing", ContentHash: strings.Repeat("c", 64), Protected: true},
		{Kind: "mcp", Name: "search", ContentHash: strings.Repeat("d", 64)},
		{Kind: "skill", Name: "unmanaged-extra", ContentHash: strings.Repeat("e", 64)},
	}

	health, drift := projectScanHealth(manifest, members)
	if health != bridgeprotocol.HealthDrifted {
		t.Fatalf("health=%q", health)
	}
	if len(drift.Add) != 1 || drift.Add[0].Name != "missing" || drift.Add[0].Reason != "protected" {
		t.Fatalf("add=%+v", drift.Add)
	}
	if len(drift.Replace) != 1 || drift.Replace[0].Name != "search" || drift.Replace[0].Reason != "content_hash_mismatch" {
		t.Fatalf("replace=%+v", drift.Replace)
	}
	if len(drift.Delete) != 0 || len(drift.Excluded) != 0 {
		t.Fatalf("unmanaged inventory must not be projected as drift: %+v", drift)
	}
}

func TestProjectScanHealthMarksMatchingPinnedMembersHealthy(t *testing.T) {
	hash := strings.Repeat("a", 64)
	manifest := bridgeprotocol.DesiredManifest{Skills: []bridgeprotocol.SkillMember{{MemberID: "managed", Slug: "formatter", ContentHash: hash}}}
	members := []bridgeprotocol.InventoryMember{
		{Kind: "skill", Name: "formatter", ContentHash: hash},
		{Kind: "skill", Name: "later-unmanaged", ContentHash: strings.Repeat("b", 64)},
	}

	health, drift := projectScanHealth(manifest, members)
	if health != bridgeprotocol.HealthHealthy {
		t.Fatalf("health=%q drift=%+v", health, drift)
	}
	if len(drift.Add) != 0 || len(drift.Replace) != 0 || len(drift.Delete) != 0 || len(drift.Excluded) != 0 {
		t.Fatalf("drift=%+v", drift)
	}
}

func TestValidateTargetBindingRejectsManagedUsernameChange(t *testing.T) {
	target := bridgeprotocol.Target{
		ID:              "11111111-1111-4111-8111-111111111111",
		NodeID:          "22222222-2222-4222-8222-222222222222",
		NodeKind:        bridgeprotocol.NodeKindLocal,
		Runtime:         bridgeprotocol.RuntimeClaude,
		ManagedUsername: "new-user",
	}
	manifest := bridgeprotocol.DesiredManifest{Target: target}
	manifest.Target.ManagedUsername = "old-user"

	assertRevisionConflict(t, validateTargetBinding(target, manifest, 0))
}

func TestValidateTargetBindingRejectsRelayPortChange(t *testing.T) {
	target := bridgeprotocol.Target{
		ID:              "11111111-1111-4111-8111-111111111111",
		NodeID:          "22222222-2222-4222-8222-222222222222",
		NodeKind:        bridgeprotocol.NodeKindLocal,
		Runtime:         bridgeprotocol.RuntimeSharedRelay,
		ManagedUsername: "runner",
	}
	manifest := bridgeprotocol.DesiredManifest{Target: target, RelayPort: 6276}

	if err := validateTargetBinding(target, manifest, 6276); err != nil {
		t.Fatalf("unchanged binding was rejected: %v", err)
	}
	assertRevisionConflict(t, validateTargetBinding(target, manifest, 6277))
}

func assertRevisionConflict(t *testing.T, err error) {
	t.Helper()
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrRevisionConflict {
		t.Fatalf("expected revision conflict, got %v", err)
	}
}
