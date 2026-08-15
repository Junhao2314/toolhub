package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestCanonicalContractIgnoresToolAndPresentationOrder(t *testing.T) {
	first := []ObservedToolInput{
		{Name: "z_tool", InputSchema: json.RawMessage(`{"type":"object"}`), Presentation: map[string]any{"title": "Z"}},
		{Name: "a_tool", InputSchema: json.RawMessage(`{"type":"object"}`), Presentation: map[string]any{"title": "A"}},
	}
	second := []ObservedToolInput{first[1], first[0]}
	first[0].Presentation = map[string]any{"title": "Z", "order": 2}
	second[1].Presentation = map[string]any{"order": 2, "title": "Z"}
	one, hashOne, err := CanonicalContract(first)
	if err != nil {
		t.Fatal(err)
	}
	two, hashTwo, err := CanonicalContract(second)
	if err != nil {
		t.Fatal(err)
	}
	if hashOne != hashTwo || string(one) != string(two) {
		t.Fatalf("order changed canonical contract: %s/%s %s/%s", hashOne, hashTwo, one, two)
	}
}

func TestCanonicalContractAllowsLegitimateSchemaPropertyNames(t *testing.T) {
	_, _, err := CanonicalContract([]ObservedToolInput{{
		Name: "submit", InputSchema: json.RawMessage(`{"type":"object","properties":{"arguments":{"type":"string"},"result":{"type":"string"}}}`),
	}})
	if err != nil {
		t.Fatalf("legitimate schema property was rejected: %v", err)
	}
}

func TestObserveContractsCreatesImmutableRevisionAndStatusesChanges(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "observed", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	input := ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "list_items", ReadOnlyHint: true}}}
	first, err := st.ObserveContracts(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision.Revision != 1 || first.Revision.CanonicalHash == "" {
		t.Fatalf("first observation=%+v", first)
	}
	second, err := st.ObserveContracts(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision.ID != first.Revision.ID {
		t.Fatalf("identical contract created revision %s after %s", second.Revision.ID, first.Revision.ID)
	}
	changed := ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{
		{Name: "list_items", ReadOnlyHint: true, Annotations: map[string]any{"destructiveHint": true}},
		{Name: "new_item", Mutating: true},
	}}
	third, err := st.ObserveContracts(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if third.Revision.ID == first.Revision.ID || third.Revision.Revision != 2 {
		t.Fatalf("changed observation=%+v", third.Revision)
	}
	if third.Statuses["new_item"] != ContractToolNewHidden {
		t.Fatalf("new tool status=%q want %q", third.Statuses["new_item"], ContractToolNewHidden)
	}
	if third.Statuses["list_items"] != ContractToolPausedIncompatible {
		t.Fatalf("changed tool status=%q want %q", third.Statuses["list_items"], ContractToolPausedIncompatible)
	}
	var toolID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM mcp_tools WHERE server_id=$1 AND name='list_items'`, server.ID).Scan(&toolID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE mcp_tools SET name='mutated' WHERE id=$1`, toolID); err == nil {
		t.Fatal("immutable tool identity accepted an update")
	}
	if _, err := st.pool.Exec(ctx, `DELETE FROM mcp_contract_revisions WHERE id=$1`, third.Revision.ID); err == nil {
		t.Fatal("immutable contract revision accepted a delete")
	}
}

func TestObserveContractsRejectsPayloadFields(t *testing.T) {
	st := newIntegrationStore(t, true)
	_, err := st.ObserveContracts(context.Background(), ContractObservationInput{
		ServerID: uuid.NewString(),
		Tools:    []ObservedToolInput{{Name: "unsafe", Annotations: map[string]any{"arguments": "must-not-persist"}}},
	})
	if err == nil {
		t.Fatal("payload-like observation was accepted")
	}
}

func TestSuspectedRenameConfirmationInheritsExplicitGovernance(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "rename-server", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	oldObservation, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "old_name", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, oldObservation.Revision.ID); err != nil {
		t.Fatal(err)
	}
	var oldToolID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM mcp_tools WHERE server_id=$1 AND name='old_name'`, server.ID).Scan(&oldToolID); err != nil {
		t.Fatal(err)
	}
	global, err := st.SaveGlobalPolicy(ctx, GlobalPolicyInput{CatalogVersion: 1, ExplicitOverrides: map[string]string{oldToolID: "confirm"}, UnclassifiedMutating: "confirm", ReviewedReadOnly: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyGlobalPolicy(ctx, global.ID); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-rename", ClientKind: "claude", Category: "coding", Variant: "standard",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: oldObservation.Revision.ID, VisibilityMode: "all_accepted"}},
		ToolRules:     []ProfileToolRuleInput{{ToolID: oldToolID, Decision: "deny", Visible: true, ReasonCodes: []string{"operator_review"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	newObservation, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "new_name", InputSchema: json.RawMessage(`{"properties":{"id":{"type":"string"}},"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if newObservation.Statuses["new_name"] != ContractToolNewHidden {
		t.Fatalf("new tool status=%q", newObservation.Statuses["new_name"])
	}
	var proposalID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM mcp_tool_rename_proposals WHERE server_id=$1 AND status='suspected'`, server.ID).Scan(&proposalID); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmToolRename(ctx, proposalID); err != nil {
		t.Fatal(err)
	}
	var newToolID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM mcp_tools WHERE server_id=$1 AND name='new_name'`, server.ID).Scan(&newToolID); err != nil {
		t.Fatal(err)
	}
	var visible bool
	var decision string
	if err := st.pool.QueryRow(ctx, `SELECT visible,decision FROM profile_revision_tool_rules WHERE profile_revision_id=(SELECT current_revision_id FROM profiles WHERE id=$1) AND tool_id=$2`, profile.ID, newToolID).Scan(&visible, &decision); err != nil {
		t.Fatal(err)
	}
	if !visible || decision != "deny" {
		t.Fatalf("inherited profile rule=%v/%s", visible, decision)
	}
	var inherited string
	if err := st.pool.QueryRow(ctx, `SELECT explicit_overrides->>$1 FROM global_policy_revisions WHERE id=(SELECT current_revision_id FROM global_policy_state WHERE singleton)`, newToolID).Scan(&inherited); err != nil {
		t.Fatal(err)
	}
	if inherited != "confirm" {
		t.Fatalf("inherited global override=%q", inherited)
	}
}
