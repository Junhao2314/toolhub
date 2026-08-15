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

func TestProfileBrowserProjectionRoundTripsGovernanceAndPublishedRevision(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "profile-round-trip", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, contract.Revision.ID); err != nil {
		t.Fatal(err)
	}
	toolID := integrationToolID(t, st, server.ID, "read_item")
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-round-trip", ClientKind: "claude", Category: "coding", Variant: "focused",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: contract.Revision.ID, VisibilityMode: "selected"}},
		ToolRules:     []ProfileToolRuleInput{{ToolID: toolID, Visible: true, Decision: "confirm", ReasonCodes: []string{"operator_review"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRevisionID := profile.CurrentRevisionID
	if _, err := st.pool.Exec(ctx, `INSERT INTO published_profiles(profile_id,profile_revision_id) VALUES($1,$2)`, profile.ID, firstRevisionID); err != nil {
		t.Fatal(err)
	}

	projected, err := st.Profile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.MCPGovernance) != 1 || projected.MCPGovernance[0].VisibilityMode != "selected" || len(projected.ToolRules) != 1 || projected.ToolRules[0].Decision != "confirm" {
		t.Fatalf("current governance projection=%+v rules=%+v", projected.MCPGovernance, projected.ToolRules)
	}
	updated, err := st.SaveProfile(ctx, profile.ID, ProfileInput{
		Name: projected.Name, Description: "draft", ClientKind: projected.ClientKind, Category: projected.Category, Variant: projected.Variant,
		MigrationState: projected.MigrationState, SkillIDs: projected.SkillIDs, MCPServerIDs: projected.MCPServerIDs,
		SkillVersionIDs: profileSkillVersions(projected), MCPRevisionIDs: profileMCPRevisions(projected),
		MCPGovernance: projected.MCPGovernance, ToolRules: projected.ToolRules, Revision: projected.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.PublishedRevisionID != firstRevisionID || updated.PublishedRevision != 1 || updated.PublishedAt == nil {
		t.Fatalf("published projection id=%q revision=%d at=%v", updated.PublishedRevisionID, updated.PublishedRevision, updated.PublishedAt)
	}
	if updated.CurrentRevisionID == updated.PublishedRevisionID {
		t.Fatal("draft current revision was reported as Published")
	}
	history, err := st.ProfileHistory(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history length=%d want 2", len(history))
	}
	for _, revision := range history {
		if len(revision.MCPGovernance) != 1 || revision.MCPGovernance[0].AcceptedContractRevisionID != contract.Revision.ID || len(revision.ToolRules) != 1 || revision.ToolRules[0].ToolID != toolID {
			t.Fatalf("revision %d lost governance=%+v rules=%+v", revision.Revision, revision.MCPGovernance, revision.ToolRules)
		}
	}
}

func TestSaveProfileRequiresCurrentAcceptedContractPin(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "accepted-pin", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, accepted.Revision.ID); err != nil {
		t.Fatal(err)
	}
	latest, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}, {Name: "new_item", Mutating: true}}})
	if err != nil {
		t.Fatal(err)
	}
	base := ProfileInput{Name: "claude-pin", ClientKind: "claude", Category: "coding", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}}
	for name, contractID := range map[string]string{"missing": "", "latest-unaccepted": latest.Revision.ID} {
		t.Run(name, func(t *testing.T) {
			input := base
			input.Name += "-" + name
			input.MCPGovernance = []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: contractID, VisibilityMode: "all_accepted"}}
			if _, err := st.SaveProfile(ctx, uuid.NewString(), input); err != ErrConflict {
				t.Fatalf("contract pin %q returned %v, want conflict", contractID, err)
			}
		})
	}
	base.MCPGovernance = []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: accepted.Revision.ID, VisibilityMode: "all_accepted"}}
	if _, err := st.SaveProfile(ctx, uuid.NewString(), base); err != nil {
		t.Fatalf("current accepted contract pin rejected: %v", err)
	}
}

func TestSaveProfileRejectsToolRuleOutsideAcceptedContract(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "accepted-tools", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, accepted.Revision.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}, {Name: "pending_item", Mutating: true}}}); err != nil {
		t.Fatal(err)
	}
	var pendingToolID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM mcp_tools WHERE server_id=$1 AND name='pending_item'`, server.ID).Scan(&pendingToolID); err != nil {
		t.Fatal(err)
	}
	_, err = st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-accepted-tools", ClientKind: "claude", Category: "coding",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: accepted.Revision.ID, VisibilityMode: "all_accepted"}},
		ToolRules:     []ProfileToolRuleInput{{ToolID: pendingToolID, Visible: true, Decision: "confirm", ReasonCodes: []string{"operator_review"}}},
	})
	if err != ErrConflict {
		t.Fatalf("tool outside accepted contract returned %v, want conflict", err)
	}
}
