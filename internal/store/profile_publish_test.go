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
	if err := st.PublishProfile(ctx, first.ID, first.CurrentRevisionID); err != nil {
		t.Fatal(err)
	}
	if err := st.PublishProfile(ctx, second.ID, second.CurrentRevisionID); err != nil {
		t.Fatal(err)
	}
	published, err := st.ListPublishedProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 2 {
		t.Fatalf("published=%+v", published)
	}
	if err := st.UnpublishProfile(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	published, err = st.ListPublishedProfiles(ctx)
	if err != nil || len(published) != 1 || published[0].ProfileID != second.ID {
		t.Fatalf("after unpublish=%+v err=%v", published, err)
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
	if err := st.FinishOperationTarget(ctx, second.OperationTarget.ID, "succeeded", map[string]any{}, nil); err != nil {
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
