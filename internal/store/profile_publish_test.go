package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func TestPublishedProfilePointerIsPerProfile(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	first, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "claude-first", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "codex-second", ClientKind: "codex", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	firstOperation := succeededGovernanceOperation(t, st, "apply", first.ID, map[string]any{"profileRevisionId": first.CurrentRevisionID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeProfilePublish(ctx, firstOperation, first.ID, first.CurrentRevisionID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	secondOperation := succeededGovernanceOperation(t, st, "apply", second.ID, map[string]any{"profileRevisionId": second.CurrentRevisionID, "routingHash": strings.Repeat("b", 64)})
	if err := st.FinalizeProfilePublish(ctx, secondOperation, second.ID, second.CurrentRevisionID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	published, err := st.ListPublishedProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 2 {
		t.Fatalf("published=%+v", published)
	}
	unpublishOperation := succeededGovernanceOperation(t, st, "apply", first.ID, map[string]any{})
	if err := st.FinalizeProfileUnpublish(ctx, unpublishOperation, first.ID); err != nil {
		t.Fatal(err)
	}
	published, err = st.ListPublishedProfiles(ctx)
	if err != nil || len(published) != 1 || published[0].ProfileID != second.ID {
		t.Fatalf("after unpublish=%+v err=%v", published, err)
	}
}

func TestArchiveProfileRemovesPublishedPointer(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "archive-published", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "apply", profile.ID, map[string]any{"profileRevisionId": profile.CurrentRevisionID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeProfilePublish(ctx, opID, profile.ID, profile.CurrentRevisionID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := st.ArchiveProfile(ctx, profile.ID, profile.Revision); err != nil {
		t.Fatal(err)
	}
	published, err := st.ListPublishedProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 0 {
		t.Fatalf("published pointer survived archive: %+v", published)
	}
}

func TestPublishProfileRequiresAcceptedPinAndAppliedRelayMembership(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "publish-pin", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-publish-pin", ClientKind: "claude", Category: "coding",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, VisibilityMode: "all_accepted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "apply", profile.ID, map[string]any{"profileRevisionId": profile.CurrentRevisionID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeProfilePublish(ctx, opID, profile.ID, profile.CurrentRevisionID, strings.Repeat("a", 64)); err != ErrConflict {
		t.Fatalf("profile without accepted contract published: %v", err)
	}
	observed, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, observed.Revision.ID); err != nil {
		t.Fatal(err)
	}
	profile, err = st.SaveProfile(ctx, profile.ID, ProfileInput{
		Name: profile.Name, ClientKind: "claude", Category: "coding", Revision: profile.Revision,
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: observed.Revision.ID, VisibilityMode: "all_accepted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	opID = succeededGovernanceOperation(t, st, "apply", profile.ID, map[string]any{"profileRevisionId": profile.CurrentRevisionID, "routingHash": strings.Repeat("b", 64)})
	if err := st.FinalizeProfilePublish(ctx, opID, profile.ID, profile.CurrentRevisionID, strings.Repeat("b", 64)); err != ErrConflict {
		t.Fatalf("profile outside applied relay published: %v", err)
	}
}

func TestFinalizeProfileApplyPublishesOnlyAfterOrderedTargetsSucceed(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "publish-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	skillTarget := integrationTarget(t, st, "local/claude")
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "claude-apply", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	skillManifest, err := st.ResolveProfileManifest(ctx, profile.ID, skillTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	relayManifest, err := st.ResolveProfileManifest(ctx, profile.ID, relayTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	if relayManifest.RelayGovernance == nil {
		t.Fatal("relay manifest did not include governance")
	}
	relayHash := relayManifest.RelayGovernance.RoutingHash
	skillToken, _, err := st.CreatePreflightConfirmation(ctx, profile.ID, skillTarget.ID, strings.Repeat("a", 64), skillManifest, bridgeprotocol.Diff{}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	relayToken, _, err := st.CreatePreflightConfirmation(ctx, profile.ID, relayTarget.ID, strings.Repeat("b", 64), relayManifest, bridgeprotocol.Diff{}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := st.CreateProfileApplyOperation(ctx, profile.ID, []string{skillToken, relayToken}, "ordered-apply")
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.ClaimOperationTarget(ctx)
	if err != nil || first.Target.ID != skillTarget.ID {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if err := st.FinishOperationTarget(ctx, first.OperationTarget.ID, "succeeded", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	second, err := st.ClaimOperationTarget(ctx)
	if err != nil || second.Target.ID != relayTarget.ID {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if err := st.FinishOperationTarget(ctx, second.OperationTarget.ID, "succeeded", map[string]any{"routingReloaded": true, "routingHash": relayHash}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeProfileApply(ctx, operation.ID, profile.ID, profile.CurrentRevisionID); err != nil {
		t.Fatal(err)
	}
	var publishedRevision string
	if err := st.pool.QueryRow(ctx, `SELECT profile_revision_id::text FROM published_profiles WHERE profile_id=$1`, profile.ID).Scan(&publishedRevision); err != nil {
		t.Fatal(err)
	}
	if publishedRevision != profile.CurrentRevisionID {
		t.Fatalf("published revision=%s", publishedRevision)
	}
}

func TestFinalizeProfileApplyRequiresExactlyOneRelayReloadTarget(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "publish-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	skillTarget := integrationTarget(t, st, "local/claude")
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "claude-no-relay", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := st.ResolveProfileManifest(ctx, profile.ID, skillTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := st.CreatePreflightConfirmation(ctx, profile.ID, skillTarget.ID, strings.Repeat("a", 64), manifest, bridgeprotocol.Diff{}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := st.CreateProfileApplyOperation(ctx, profile.ID, []string{token}, "apply-without-relay")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimOperationTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishOperationTarget(ctx, claimed.OperationTarget.ID, bridgeprotocol.OperationSucceeded, map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeProfileApply(ctx, operation.ID, profile.ID, profile.CurrentRevisionID); err != ErrConflict {
		t.Fatalf("Profile Apply without relay reload returned %v, want conflict", err)
	}
}
