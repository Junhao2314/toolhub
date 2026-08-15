package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func succeededGovernanceOperation(t *testing.T, st *Store, kind, sourceID string, metadata map[string]any) string {
	t.Helper()
	ctx := context.Background()
	if err := st.BootstrapEnvironment(ctx, "governance-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	routingHash, _ := metadata["routingHash"].(string)
	if routingHash == "" {
		routingHash = strings.Repeat("f", 64)
		metadata["routingHash"] = routingHash
	}
	id := uuid.NewString()
	body, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO operations(id,kind,status,source_id,metadata) VALUES($1,$2,'succeeded',NULLIF($3,'')::uuid,$4)`, id, kind, sourceID, jsonText(body)); err != nil {
		t.Fatal(err)
	}
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	result, err := json.Marshal(map[string]any{"routingReloaded": true, "routingHash": routingHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,request,result,finished_at) VALUES($1,$2,$3,'succeeded','{}',$4,now())`, uuid.NewString(), id, relayTarget.ID, jsonText(result)); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGovernancePointerFinalizerRequiresMatchingRelayReload(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "reload-proof", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	routingHash := strings.Repeat("a", 64)
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": draft.ID, "routingHash": routingHash})
	wrongResult, err := json.Marshal(map[string]any{"routingReloaded": true, "routingHash": strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE operation_targets SET result=$2 WHERE operation_id=$1`, opID, jsonText(wrongResult)); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, draft.ID, routingHash); err != ErrConflict {
		t.Fatalf("finalizer accepted mismatched reload evidence: %v", err)
	}
	var applied string
	if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied == draft.ID {
		t.Fatal("relay pointer advanced after mismatched reload evidence")
	}
}

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
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": draft.ID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, draft.ID, strings.Repeat("a", 64)); err != nil {
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

func TestDirectRelayConfigurationApplyCannotAdvanceTheAppliedPointer(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "relay-guard", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyRelayConfiguration(ctx, draft.ID); err == nil {
		t.Fatal("direct relay apply advanced a pointer without a successful reload operation")
	}
	var applied string
	if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied == draft.ID {
		t.Fatal("applied relay pointer advanced without an operation")
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
	relayDraft, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": relayDraft.ID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, relayDraft.ID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	opID = succeededGovernanceOperation(t, st, "apply", profile.ID, map[string]any{"profileRevisionId": profile.CurrentRevisionID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeProfilePublish(ctx, opID, profile.ID, profile.CurrentRevisionID, strings.Repeat("a", 64)); err != nil {
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
	body, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var strict bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(body, &strict); err != nil {
		t.Fatalf("rendered routing bundle is not consumable by Bridge DTO: %v\n%s", err, body)
	}
	_, secondHash, err := st.RenderRoutingBundle(ctx)
	if err != nil || secondHash != hash {
		t.Fatalf("bundle hash changed: %q/%q err=%v", hash, secondHash, err)
	}
	if _, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true, Annotations: map[string]any{"destructiveHint": true}}}}); err != nil {
		t.Fatal(err)
	}
	bundle, _, err = st.RenderRoutingBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Servers[0].Tools) != 1 || !bundle.Servers[0].Tools[0].Paused {
		t.Fatalf("later incompatible observation did not pause accepted tool: %+v", bundle.Servers[0].Tools)
	}
}

func TestRoutingBundleHidesNewlyAcceptedToolsWithoutExplicitProfileOverride(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "new-hidden", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, first.Revision.ID); err != nil {
		t.Fatal(err)
	}
	second, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "read_item", ReadOnlyHint: true}, {Name: "write_item", Mutating: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, second.Revision.ID); err != nil {
		t.Fatal(err)
	}
	relay, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": relay.ID, "routingHash": strings.Repeat("a", 64)})
	if err := st.FinalizeRelayConfigurationApply(ctx, opID, relay.ID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-new-hidden", ClientKind: "claude", Category: "coding",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: second.Revision.ID, VisibilityMode: "all_accepted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	opID = succeededGovernanceOperation(t, st, "apply", profile.ID, map[string]any{"profileRevisionId": profile.CurrentRevisionID, "routingHash": strings.Repeat("b", 64)})
	if err := st.FinalizeProfilePublish(ctx, opID, profile.ID, profile.CurrentRevisionID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	bundle, _, err := st.RenderRoutingBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var newToolID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM mcp_tools WHERE server_id=$1 AND name='write_item'`, server.ID).Scan(&newToolID); err != nil {
		t.Fatal(err)
	}
	overrides := bundle.Profiles[0].Servers[0].ToolOverrides
	if len(overrides) != 1 || overrides[0].ToolID != newToolID || overrides[0].Visible {
		t.Fatalf("new hidden overrides=%+v", overrides)
	}
}
