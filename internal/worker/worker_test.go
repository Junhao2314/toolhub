package worker

import (
	"errors"
	"testing"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

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
