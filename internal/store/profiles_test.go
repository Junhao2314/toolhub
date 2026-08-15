package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestProfileGovernanceMetadataIsPinnedPerRevision(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	id := uuid.NewString()
	first, err := st.SaveProfile(ctx, id, ProfileInput{Name: "claude-metadata", ClientKind: "claude", Category: "coding", Variant: "focused", Description: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ClientKind != "claude" || first.Category != "coding" || first.Variant != "focused" || first.MigrationState != "ready" {
		t.Fatalf("profile metadata=%+v", first)
	}
	revision, err := st.ProfileRevision(ctx, first.CurrentRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if revision.ClientKind != "claude" || revision.Category != "coding" || revision.Variant != "focused" {
		t.Fatalf("revision metadata=%+v", revision)
	}
	second, err := st.SaveProfile(ctx, id, ProfileInput{Name: "claude-metadata", ClientKind: "claude", Category: "review", Variant: "focused", Description: "first", Revision: first.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if second.CanonicalHash == first.CanonicalHash {
		t.Fatal("governance metadata change did not change revision hash")
	}
}

func TestProfileReportsEffectiveVisibleCount(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "visible-count", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, observed.Revision.ID); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-visible", ClientKind: "claude", Category: "coding",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: observed.Revision.ID, VisibilityMode: "all_accepted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.EffectiveVisibleCount != 1 {
		t.Fatalf("effective visible count=%d want 1", profile.EffectiveVisibleCount)
	}
}
