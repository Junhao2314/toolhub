package store

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/policy"
)

func TestSaveGlobalPolicyOnlyMovesDraftPointerUntilApply(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	created, err := st.SaveGlobalPolicy(ctx, GlobalPolicyInput{
		CatalogVersion:       policy.CatalogVersion,
		ExplicitOverrides:    map[string]string{"tool-id": policy.DecisionDeny},
		UnclassifiedMutating: policy.DecisionConfirm,
		ReviewedReadOnly:     policy.DecisionAllow,
	})
	if err != nil {
		t.Fatal(err)
	}
	var current, applied int64
	if err := st.pool.QueryRow(ctx, `SELECT r.revision,ar.revision FROM global_policy_state s JOIN global_policy_revisions r ON r.id=s.current_revision_id JOIN global_policy_revisions ar ON ar.id=s.applied_revision_id WHERE s.singleton`).Scan(&current, &applied); err != nil {
		t.Fatal(err)
	}
	if current != 2 || applied != 1 {
		t.Fatalf("policy pointers=%d/%d", current, applied)
	}
	opID := succeededGovernanceOperation(t, st, "policy_apply", "", map[string]any{"revisionId": created.ID})
	if err := st.FinalizeGlobalPolicyApply(ctx, opID, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT r.revision,ar.revision FROM global_policy_state s JOIN global_policy_revisions r ON r.id=s.current_revision_id JOIN global_policy_revisions ar ON ar.id=s.applied_revision_id WHERE s.singleton`).Scan(&current, &applied); err != nil {
		t.Fatal(err)
	}
	if current != 2 || applied != 2 {
		t.Fatalf("applied policy pointers=%d/%d", current, applied)
	}
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	_, manifest, err := st.ActiveDesiredManifest(ctx, relayTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	var routing bridgeprotocol.RoutingBundle
	if manifest.RelayGovernance == nil || bridgeprotocol.DecodeGovernanceBody(manifest.RelayGovernance.RoutingBundle, &routing) != nil || routing.GlobalPolicyRevisionID != created.ID {
		t.Fatalf("active desired routing did not pin applied policy %s: %+v", created.ID, manifest.RelayGovernance)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE global_policy_revisions SET reviewed_read_only='deny' WHERE id=$1`, created.ID); err == nil {
		t.Fatal("global policy revision accepted mutation")
	}
}

func TestSaveGlobalPolicyRejectsInvalidDecision(t *testing.T) {
	_, err := newIntegrationStore(t, true).SaveGlobalPolicy(context.Background(), GlobalPolicyInput{
		CatalogVersion: 1, UnclassifiedMutating: "invalid", ReviewedReadOnly: policy.DecisionAllow,
	})
	if err == nil {
		t.Fatal("invalid global policy decision accepted")
	}
}

func TestGlobalPolicyFinalizerRejectsASecondOperationWithStalePredecessor(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	first, err := st.SaveGlobalPolicy(ctx, GlobalPolicyInput{CatalogVersion: policy.CatalogVersion, ExplicitOverrides: map[string]string{"first": policy.DecisionDeny}, UnclassifiedMutating: policy.DecisionConfirm, ReviewedReadOnly: policy.DecisionAllow})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.SaveGlobalPolicy(ctx, GlobalPolicyInput{Revision: first.Revision, CatalogVersion: policy.CatalogVersion, ExplicitOverrides: map[string]string{"second": policy.DecisionDeny}, UnclassifiedMutating: policy.DecisionConfirm, ReviewedReadOnly: policy.DecisionAllow})
	if err != nil {
		t.Fatal(err)
	}
	var predecessor string
	if err := st.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM global_policy_state WHERE singleton`).Scan(&predecessor); err != nil {
		t.Fatal(err)
	}
	firstOperation := succeededGovernanceOperation(t, st, "policy_apply", "", map[string]any{"revisionId": first.ID})
	if err := st.FinalizeGlobalPolicyApply(ctx, firstOperation, first.ID); err != nil {
		t.Fatal(err)
	}
	secondOperation := succeededGovernanceOperation(t, st, "policy_apply", "", map[string]any{"revisionId": second.ID, "expectedAppliedGlobalPolicyRevisionId": predecessor})
	if err := st.FinalizeGlobalPolicyApply(ctx, secondOperation, second.ID); err != ErrConflict {
		t.Fatalf("stale Global Policy operation returned %v, want conflict", err)
	}
}

func TestProfileToolRuleCannotLowerGlobalCeiling(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "governed", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "write_item", Mutating: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, observed.Revision.ID); err != nil {
		t.Fatal(err)
	}
	var toolID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM mcp_tools WHERE server_id=$1 AND name='write_item'`, server.ID).Scan(&toolID); err != nil {
		t.Fatal(err)
	}
	global, err := st.SaveGlobalPolicy(ctx, GlobalPolicyInput{CatalogVersion: 1, ExplicitOverrides: map[string]string{toolID: policy.DecisionDeny}, UnclassifiedMutating: policy.DecisionConfirm, ReviewedReadOnly: policy.DecisionAllow})
	if err != nil {
		t.Fatal(err)
	}
	opID := succeededGovernanceOperation(t, st, "policy_apply", "", map[string]any{"revisionId": global.ID})
	if err := st.FinalizeGlobalPolicyApply(ctx, opID, global.ID); err != nil {
		t.Fatal(err)
	}
	_, err = st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-governed", ClientKind: "claude", Category: "coding", Variant: "standard",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
		MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: observed.Revision.ID, VisibilityMode: "all_accepted"}},
		ToolRules:     []ProfileToolRuleInput{{ToolID: toolID, Decision: policy.DecisionAllow, Visible: true}},
	})
	if err == nil {
		t.Fatal("profile lowered deny ceiling")
	}
	if err != ErrConflict {
		t.Fatalf("profile ceiling error=%v want %v", err, ErrConflict)
	}
}
