package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestRelayConfigurationDraftAndAppliedPointers(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "relay-config", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, Metadata: map[string]any{"source": "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Revision != 2 || len(draft.MCPServers) != 1 {
		t.Fatalf("draft=%+v", draft)
	}
	var current, applied int64
	if err := st.pool.QueryRow(ctx, `SELECT r.revision,ar.revision FROM relay_configuration_state s JOIN relay_configuration_revisions r ON r.id=s.current_revision_id JOIN relay_configuration_revisions ar ON ar.id=s.applied_revision_id WHERE s.singleton`).Scan(&current, &applied); err != nil {
		t.Fatal(err)
	}
	if current != 2 || applied != 1 {
		t.Fatalf("pointers=%d/%d", current, applied)
	}
	same, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{
		Revision: draft.Revision, MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, Metadata: map[string]any{"source": "test"},
	})
	if err != nil || same.ID != draft.ID {
		t.Fatalf("same draft=%+v err=%v", same, err)
	}
	if err := st.ApplyRelayConfiguration(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT r.revision,ar.revision FROM relay_configuration_state s JOIN relay_configuration_revisions r ON r.id=s.current_revision_id JOIN relay_configuration_revisions ar ON ar.id=s.applied_revision_id WHERE s.singleton`).Scan(&current, &applied); err != nil {
		t.Fatal(err)
	}
	if current != 2 || applied != 2 {
		t.Fatalf("applied pointers=%d/%d", current, applied)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_revisions SET metadata='{}' WHERE id=$1`, draft.ID); err == nil {
		t.Fatal("relay configuration revision accepted mutation")
	}
}

func TestRoutingBundlePreservesPublishedProfilesAndIsCanonical(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "routing-server", Transport: "http", URL: "https://example.invalid/mcp"})
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
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "claude-routing", ClientKind: "claude", Category: "coding", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: contract.Revision.ID, VisibilityMode: "all_accepted"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PublishProfile(ctx, profile.ID, profile.CurrentRevisionID); err != nil {
		t.Fatal(err)
	}
	relayDraft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyRelayConfiguration(ctx, relayDraft.ID); err != nil {
		t.Fatal(err)
	}
	bundle, hash, err := st.RenderRoutingBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Profiles) != 1 || len(bundle.Servers) != 1 || hash == "" {
		t.Fatalf("bundle=%+v hash=%q", bundle, hash)
	}
	if bundle.Profiles[0].ProfileRevisionID != profile.CurrentRevisionID || bundle.Profiles[0].ClientKind != "claude" {
		t.Fatalf("published profile=%+v", bundle.Profiles[0])
	}
	_, secondHash, err := st.RenderRoutingBundle(ctx)
	if err != nil || secondHash != hash {
		t.Fatalf("bundle hash changed: %q/%q err=%v", hash, secondHash, err)
	}
}
