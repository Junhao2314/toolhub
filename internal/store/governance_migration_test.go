package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
)

func TestGovernanceMigrationIsAdditiveAndDefinesDurableRecords(t *testing.T) {
	body, err := migrationFS.ReadFile("migrations/004_mcp_profile_routing_governance.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE relay_configuration_revisions",
		"CREATE TABLE relay_configuration_revision_mcp_servers",
		"CREATE TABLE relay_configuration_state",
		"CREATE TABLE mcp_contract_revisions",
		"CREATE TABLE mcp_tools",
		"CREATE TABLE mcp_contract_revision_tools",
		"CREATE TABLE mcp_contract_state",
		"CREATE TABLE mcp_tool_rename_proposals",
		"CREATE TABLE mcp_tool_renames",
		"CREATE TABLE global_policy_revisions",
		"CREATE TABLE global_policy_state",
		"CREATE TABLE profile_revision_mcp_governance",
		"CREATE TABLE profile_revision_tool_rules",
		"CREATE TABLE published_profiles",
		"CREATE TABLE relay_observation_cursors",
		"CREATE TABLE mcp_daily_aggregates",
		"ALTER TABLE profiles",
		"client_kind",
		"migration_state",
		"depends_on_target_id",
		"relay_config_apply",
		"contract_observe",
		"policy_apply",
		"relay_telemetry_pull",
		"source_kind IN ('profile_apply','target_edit','restore','relay_config_apply')",
		"validate_desired_manifest_v1",
		"relayGovernance",
		"reject_governance_revision_mutation",
		"validate_operation_target_dependency",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("governance migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"DROP TABLE",
		"001_initial.sql",
		"002_relay_projection.sql",
		"003_profile_revisions_bundles.sql",
	} {
		if strings.Contains(strings.ToUpper(sql), strings.ToUpper(forbidden)) {
			t.Fatalf("governance migration rewrites historical schema through %q", forbidden)
		}
	}
}

func TestGovernanceMigrationBackfillIsDeterministicAndCompatibilitySafe(t *testing.T) {
	body, err := migrationFS.ReadFile("migrations/004_mcp_profile_routing_governance.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"shared-mcp",
		"needs_review",
		"compatibility",
		"global-policy:1",
		"relay-configuration:1",
		"ON CONFLICT",
		"unclassified_mutating",
		"reviewed_read_only",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("governance backfill is missing %q", required)
		}
	}
}

func TestGovernanceManifestV2MigrationMatchesBridgeContract(t *testing.T) {
	body, err := migrationFS.ReadFile("migrations/005_mcp_profile_routing_governance_contract.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"routingBundle",
		"routingHash",
		"jsonb_typeof(governance->'routingBundle')",
		"validate_desired_manifest_v1",
		"profile_revision_mcp_governance_immutable_insert",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("manifest contract migration is missing %q", required)
		}
	}
	if strings.Contains(sql, "routingBundleHash") {
		t.Fatal("manifest contract migration retains the incompatible routingBundleHash field")
	}
}

func TestGovernanceReloadIntegrityMigrationDefinesStrictRoutingValidation(t *testing.T) {
	body, err := migrationFS.ReadFile("migrations/006_mcp_governance_reload_integrity.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"validate_routing_bundle_v1",
		"routing_bundle_canonical",
		"validate_desired_manifest_v1",
		"mcp_contract_tools_immutable",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("reload integrity migration is missing %q", required)
		}
	}
}

func TestGovernanceDomainModelsHaveNoSecretOrPayloadFields(t *testing.T) {
	forbidden := map[string]struct{}{
		"secretValues": {}, "ciphertext": {}, "arguments": {}, "result": {},
		"prompt": {}, "rawError": {}, "sessionId": {}, "apiKey": {},
	}
	types := []reflect.Type{
		reflect.TypeOf(domain.RelayConfigurationRevision{}),
		reflect.TypeOf(domain.ObservedContractRevision{}),
		reflect.TypeOf(domain.ContractTool{}),
		reflect.TypeOf(domain.PublishedProfile{}),
		reflect.TypeOf(domain.GlobalPolicyRevision{}),
		reflect.TypeOf(domain.ToolRule{}),
		reflect.TypeOf(domain.DailyToolAggregate{}),
	}
	for _, modelType := range types {
		for fieldIndex := 0; fieldIndex < modelType.NumField(); fieldIndex++ {
			field := modelType.Field(fieldIndex)
			jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
			if _, found := forbidden[jsonName]; found {
				t.Fatalf("%s exposes forbidden JSON field %q", modelType.Name(), jsonName)
			}
		}
	}
}

func TestGovernanceMigrationFreshAnd003UpgradeIntegration(t *testing.T) {
	ctx := context.Background()
	fresh := newIntegrationStore(t, true)
	assertGovernanceMigrationState(t, fresh)

	upgrade := newIntegrationStore(t, false)
	applyHistoricalMigrationForTest(t, upgrade, "001_initial.sql", 1)
	applyHistoricalMigrationForTest(t, upgrade, "002_relay_projection.sql", 2)
	applyHistoricalMigrationForTest(t, upgrade, "003_profile_revisions_bundles.sql", 3)
	tx, err := upgrade.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	profileID, revisionID := uuid.NewString(), uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO profiles(id,name,description,revision,current_revision_id) VALUES($1,'claude-coding','',1,$2)`, profileID, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO profile_revisions(id,profile_id,revision,name,description,canonical_hash) VALUES($1,$2,1,'claude-coding','',$3)`, revisionID, profileID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	unknownID, unknownRevisionID := uuid.NewString(), uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO profiles(id,name,description,revision,current_revision_id) VALUES($1,'custom-profile','',1,$2)`, unknownID, unknownRevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO profile_revisions(id,profile_id,revision,name,description,canonical_hash) VALUES($1,$2,1,'custom-profile','',$3)`, unknownRevisionID, unknownID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	legacyID, legacyRevisionID := uuid.NewString(), uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO profiles(id,name,description,revision,current_revision_id) VALUES($1,'shared-mcp','',1,$2)`, legacyID, legacyRevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO profile_revisions(id,profile_id,revision,name,description,canonical_hash) VALUES($1,$2,1,'shared-mcp','',$3)`, legacyRevisionID, legacyID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := upgrade.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertGovernanceMigrationState(t, upgrade)
	var clientKind, category, state string
	if err := upgrade.pool.QueryRow(ctx, `SELECT client_kind,category,migration_state FROM profiles WHERE id=$1`, profileID).Scan(&clientKind, &category, &state); err != nil {
		t.Fatal(err)
	}
	if clientKind != "claude" || category != "coding" || state != "ready" {
		t.Fatalf("deterministic profile backfill=%q/%q/%q", clientKind, category, state)
	}
	if err := upgrade.pool.QueryRow(ctx, `SELECT migration_state FROM profiles WHERE id=$1`, unknownID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "needs_review" {
		t.Fatalf("unknown profile migration state=%q", state)
	}
	var capturedLegacyID string
	if err := upgrade.pool.QueryRow(ctx, `SELECT legacy_profile_id::text FROM relay_configuration_state WHERE singleton`).Scan(&capturedLegacyID); err != nil {
		t.Fatal(err)
	}
	if capturedLegacyID != legacyID {
		t.Fatalf("migration captured legacy Profile %s want %s", capturedLegacyID, legacyID)
	}
	if err := upgrade.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var versions int
	if err := upgrade.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 12 {
		t.Fatalf("migration reran or skipped: versions=%d", versions)
	}
}

func TestCompatibilityStartupReadinessProjectionIsNonMutating(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "compatibility-readiness", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "compatibility-server", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	relayRevision, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_state SET applied_revision_id=$1 WHERE singleton`, relayRevision.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "shared-mcp", ClientKind: "shared", Category: "relay", MigrationState: "compatibility", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "claude-coding", ClientKind: "claude", Category: "coding", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "custom-profile"}); err != nil {
		t.Fatal(err)
	}

	var contractRevisions, publishedProfiles, operations int
	if err := st.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM mcp_contract_revisions),(SELECT count(*) FROM published_profiles),(SELECT count(*) FROM operations)`).Scan(&contractRevisions, &publishedProfiles, &operations); err != nil {
		t.Fatal(err)
	}
	if err := st.InitializeRelayMigrationReadiness(ctx); err != nil {
		t.Fatal(err)
	}
	projection, err := st.RelayConfigurationProjection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Migration.State != "waiting_contract_review" || projection.Migration.PendingContractReviews != 1 || projection.Migration.AmbiguousProfiles != 1 {
		t.Fatalf("migration readiness=%+v", projection.Migration)
	}
	if projection.Migration.LegacyProfileID != "" || projection.Migration.LegacyProfileState != "pending" {
		t.Fatalf("startup captured late-created legacy Profile %s: %+v", legacy.ID, projection.Migration)
	}
	var afterContracts, afterPublished, afterOperations int
	if err := st.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM mcp_contract_revisions),(SELECT count(*) FROM published_profiles),(SELECT count(*) FROM operations)`).Scan(&afterContracts, &afterPublished, &afterOperations); err != nil {
		t.Fatal(err)
	}
	if afterContracts != contractRevisions || afterPublished != publishedProfiles || afterOperations != operations {
		t.Fatalf("startup mutated governance contracts=%d/%d published=%d/%d operations=%d/%d", contractRevisions, afterContracts, publishedProfiles, afterPublished, operations, afterOperations)
	}
}

func TestBootstrapFirstContractCreatesCandidateProfilesWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "bootstrap-contract", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	relayRevision, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_state SET applied_revision_id=$1 WHERE singleton`, relayRevision.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "shared-mcp", ClientKind: "shared", Category: "relay", MigrationState: "compatibility", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_state SET legacy_profile_id=$1,legacy_profile_state='pending' WHERE singleton`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	claude, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "claude-coding", ClientKind: "claude", Category: "coding", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, PendingBindings: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO pending_secret_bindings(profile_revision_id,mcp_revision_id,namespace,key,slot_hash) VALUES($1,$2,'env','API_TOKEN',$3)`, claude.CurrentRevisionID, server.CurrentRevisionID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	codex, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "codex-coding", ClientKind: "codex", Category: "coding", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	ambiguous, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "custom-profile"})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "status", InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, observation.Revision.ID); err != nil {
		t.Fatal(err)
	}

	for _, original := range []domain.Profile{claude, codex} {
		candidate, err := st.Profile(ctx, original.ID)
		if err != nil {
			t.Fatal(err)
		}
		if candidate.Revision != original.Revision+1 || candidate.CurrentRevisionID == original.CurrentRevisionID {
			t.Fatalf("Profile %s candidate revision=%d id=%s", candidate.Name, candidate.Revision, candidate.CurrentRevisionID)
		}
		if len(candidate.MCPGovernance) != 1 || candidate.MCPGovernance[0].MCPRevisionID != server.CurrentRevisionID || candidate.MCPGovernance[0].AcceptedContractRevisionID != observation.Revision.ID || candidate.MCPGovernance[0].VisibilityMode != "all_accepted" {
			t.Fatalf("Profile %s governance=%+v", candidate.Name, candidate.MCPGovernance)
		}
		if candidate.PublishedRevisionID != "" {
			t.Fatalf("Profile %s was published during bootstrap", candidate.Name)
		}
	}
	var copiedBindings int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM pending_secret_bindings WHERE profile_revision_id=(SELECT current_revision_id FROM profiles WHERE id=$1)`, claude.ID).Scan(&copiedBindings); err != nil {
		t.Fatal(err)
	}
	if copiedBindings != 1 {
		t.Fatalf("bootstrap copied pending bindings=%d want 1", copiedBindings)
	}
	unchanged, err := st.Profile(ctx, ambiguous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.CurrentRevisionID != ambiguous.CurrentRevisionID || unchanged.Revision != ambiguous.Revision {
		t.Fatalf("ambiguous Profile advanced to revision %d", unchanged.Revision)
	}
	var publishedProfiles, operations int
	if err := st.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM published_profiles),(SELECT count(*) FROM operations)`).Scan(&publishedProfiles, &operations); err != nil {
		t.Fatal(err)
	}
	if publishedProfiles != 0 || operations != 0 {
		t.Fatalf("bootstrap published=%d operations=%d", publishedProfiles, operations)
	}
	if err := st.AcceptContract(ctx, server.ID, observation.Revision.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err := st.Profile(ctx, claude.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != claude.Revision+1 {
		t.Fatalf("replayed accept created revision %d", replayed.Revision)
	}
}

func TestAcceptContractAuditFailureRollsBackPointerAndCandidateProfiles(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "contract-audit-rollback", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	relayRevision, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_state SET applied_revision_id=$1 WHERE singleton`, relayRevision.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "shared-mcp", ClientKind: "shared", Category: "relay", MigrationState: "compatibility"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_state SET legacy_profile_id=$1,legacy_profile_state='pending' WHERE singleton`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{
		Name: "claude-contract-audit-rollback", ClientKind: "claude", Category: "coding",
		MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "status", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `DROP TABLE audit_events`); err != nil {
		t.Fatal(err)
	}

	acceptErr := st.AcceptContract(ctx, server.ID, observation.Revision.ID)
	var acceptedRevisionID string
	if err := st.pool.QueryRow(ctx, `SELECT coalesce(accepted_revision_id::text,'') FROM mcp_contract_state WHERE server_id=$1`, server.ID).Scan(&acceptedRevisionID); err != nil {
		t.Fatal(err)
	}
	unchanged, err := st.Profile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}

	if acceptErr == nil || acceptedRevisionID != "" || unchanged.CurrentRevisionID != profile.CurrentRevisionID || unchanged.Revision != profile.Revision {
		t.Fatalf("accept error=%v pointer=%q profile revision=%d/%s want rollback to %d/%s", acceptErr, acceptedRevisionID, unchanged.Revision, unchanged.CurrentRevisionID, profile.Revision, profile.CurrentRevisionID)
	}
}

func TestFirstContractBootstrapRequiresPendingLegacyTransition(t *testing.T) {
	for _, test := range []struct {
		name        string
		legacyState string
	}{
		{name: "fresh install"},
		{name: "completed migration rollback", legacyState: "migrated_relay"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			st := newIntegrationStore(t, true)
			server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "bootstrap-gate-" + strings.ReplaceAll(test.name, " ", "-"), Transport: "http", URL: "https://example.invalid/mcp"})
			if err != nil {
				t.Fatal(err)
			}
			relayRevision, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_state SET applied_revision_id=$1 WHERE singleton`, relayRevision.ID); err != nil {
				t.Fatal(err)
			}
			if test.legacyState != "" {
				legacy, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "shared-mcp", ClientKind: "shared", Category: "relay", MigrationState: "compatibility"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_state SET legacy_profile_id=$1,legacy_profile_state=$2 WHERE singleton`, legacy.ID, test.legacyState); err != nil {
					t.Fatal(err)
				}
			}
			profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "claude-bootstrap-gate", ClientKind: "claude", Category: "coding", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
			if err != nil {
				t.Fatal(err)
			}
			observation, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "status", ReadOnlyHint: true}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.AcceptContract(ctx, server.ID, observation.Revision.ID); err != nil {
				t.Fatal(err)
			}
			unchanged, err := st.Profile(ctx, profile.ID)
			if err != nil {
				t.Fatal(err)
			}
			if unchanged.Revision != profile.Revision || unchanged.CurrentRevisionID != profile.CurrentRevisionID {
				t.Fatalf("first Contract bootstrap advanced Profile outside pending legacy transition: %d -> %d", profile.Revision, unchanged.Revision)
			}
		})
	}
}

func TestRelayConfigurationFinalizationRequiresExplicitModeMetadata(t *testing.T) {
	for _, missing := range []string{"mode", "expectedMode"} {
		t.Run(missing, func(t *testing.T) {
			ctx := context.Background()
			st := newIntegrationStore(t, true)
			_, _, _, relayRevisionID := setupPublishedRelayProfile(t, st, "missing-"+missing)
			prepared, operation := prepareSucceededRelayModeApplyForTest(t, st, relayRevisionID, "compatibility", "missing-"+missing)
			if _, err := st.pool.Exec(ctx, `UPDATE operations SET metadata=metadata-$2 WHERE id=$1`, operation.ID, missing); err != nil {
				t.Fatal(err)
			}
			if err := st.FinalizeRelayConfigurationApply(ctx, operation.ID, relayRevisionID, prepared.RoutingHash); !errors.Is(err, ErrConflict) {
				t.Fatalf("finalization without %s returned %v, want conflict", missing, err)
			}
		})
	}
}

func TestRelayConfigurationApplyLocksContractsBeforeProfiles(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "relay-lock-order", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "relay-lock-order", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "status", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptContract(ctx, server.ID, contract.Revision.ID); err != nil {
		t.Fatal(err)
	}
	relayRevision, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	relayOperationID := succeededGovernanceOperation(t, st, "relay_config_apply", "", map[string]any{"revisionId": relayRevision.ID})
	if err := st.FinalizeRelayConfigurationApply(ctx, relayOperationID, relayRevision.ID, governanceRoutingHash(t, st, relayOperationID)); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "claude-lock-order", ClientKind: "claude", Category: "coding", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: contract.Revision.ID, VisibilityMode: "all_accepted"}}})
	if err != nil {
		t.Fatal(err)
	}
	profileOperationID := succeededGovernanceOperation(t, st, "apply", profile.ID, map[string]any{"profileRevisionId": profile.CurrentRevisionID})
	if err := st.FinalizeProfilePublish(ctx, profileOperationID, profile.ID, profile.CurrentRevisionID, governanceRoutingHash(t, st, profileOperationID)); err != nil {
		t.Fatal(err)
	}
	prepared, err := st.PrepareRelayConfigurationModeApply(ctx, relayRevision.ID, []string{profile.ID}, "compatibility")
	if err != nil {
		t.Fatal(err)
	}

	barrierPool, applyStore, observerStore, closePools := integrationLockOrderStores(t, st)
	defer closePools()
	barrier, err := barrierPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = barrier.Rollback(ctx) }()
	if _, err := barrier.Exec(ctx, `SELECT server_id FROM mcp_contract_state WHERE server_id=$1 FOR UPDATE`, server.ID); err != nil {
		t.Fatal(err)
	}

	applyContext, cancelApply := context.WithTimeout(ctx, 5*time.Second)
	defer cancelApply()
	applyResult := make(chan error, 1)
	go func() {
		_, err := applyStore.CreateRelayConfigurationModeApplyOperation(applyContext, relayRevision.ID, []string{profile.ID}, "compatibility", strings.Repeat("a", 64), prepared.RoutingHash, "relay-lock-order")
		applyResult <- err
	}()
	waitForBackendLock(t, st, "toolhub-finish-lock-order")

	observer, err := observerStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, profileLockErr := observer.Exec(ctx, `SELECT p.id FROM profiles p JOIN published_profiles published ON published.profile_id=p.id WHERE p.id=$1 FOR UPDATE OF p,published NOWAIT`, profile.ID)
	_ = observer.Rollback(ctx)
	if err := barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-applyResult; err != nil {
		t.Fatal(err)
	}
	if profileLockErr != nil {
		t.Fatalf("Apply held a Profile lock while waiting for the Contract lock: %v", profileLockErr)
	}
}

func TestContractAcceptLocksRelayStateBeforeContractState(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "contract-lock-order", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "status", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}

	barrierPool, acceptStore, observerStore, closePools := integrationLockOrderStores(t, st)
	defer closePools()
	barrier, err := barrierPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = barrier.Rollback(ctx) }()
	if _, err := barrier.Exec(ctx, `SELECT singleton FROM relay_configuration_state WHERE singleton FOR UPDATE`); err != nil {
		t.Fatal(err)
	}

	acceptContext, cancelAccept := context.WithTimeout(ctx, 5*time.Second)
	defer cancelAccept()
	acceptResult := make(chan error, 1)
	go func() { acceptResult <- acceptStore.AcceptContract(acceptContext, server.ID, contract.Revision.ID) }()
	waitForBackendLock(t, st, "toolhub-finish-lock-order")

	observer, err := observerStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, contractLockErr := observer.Exec(ctx, `SELECT server_id FROM mcp_contract_state WHERE server_id=$1 FOR UPDATE NOWAIT`, server.ID)
	_ = observer.Rollback(ctx)
	if err := barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-acceptResult; err != nil {
		t.Fatal(err)
	}
	if contractLockErr != nil {
		t.Fatalf("Contract Accept held the Contract lock while waiting for the Relay state lock: %v", contractLockErr)
	}
}

func TestEnforcementPreflightRequiresReviewedHealthyAppliedRelay(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	_, _, _, relayRevisionID := setupPublishedRelayProfile(t, st, "enforcement")

	prepared, err := st.PrepareRelayConfigurationModeApply(ctx, relayRevisionID, nil, "enforced")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Manifest.RelayGovernance == nil {
		t.Fatal("enforcement preflight omitted routing governance")
	}
	var bundle bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(prepared.Manifest.RelayGovernance.RoutingBundle, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Mode != "enforced" {
		t.Fatalf("candidate mode=%q", bundle.Mode)
	}

	relayTarget := integrationTarget(t, st, "local/shared-relay")
	if _, err := st.pool.Exec(ctx, `UPDATE target_desired_snapshots SET health='drifted' WHERE target_id=$1`, relayTarget.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PrepareRelayConfigurationModeApply(ctx, relayRevisionID, nil, "enforced"); !errors.Is(err, ErrConflict) {
		t.Fatalf("drifted relay enforcement returned %v, want conflict", err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE target_desired_snapshots SET health='healthy' WHERE target_id=$1`, relayTarget.ID); err != nil {
		t.Fatal(err)
	}
	serverID := prepared.Manifest.MCPServers[0].ServerID
	if _, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: serverID, Tools: []ObservedToolInput{{Name: "changed_status", InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnlyHint: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PrepareRelayConfigurationModeApply(ctx, relayRevisionID, nil, "enforced"); !errors.Is(err, ErrConflict) {
		t.Fatalf("unreviewed Contract enforcement returned %v, want conflict", err)
	}
}

func TestLegacySharedMCPTransitionAndCompatibilityRollbackPreserveHistory(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, contract, _, relayRevisionID := setupPublishedRelayProfile(t, st, "rollback")
	legacy, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "shared-mcp", ClientKind: "shared", Category: "relay", MigrationState: "compatibility", MCPServerIDs: []string{server.ID}, MCPRevisionIDs: map[string]string{server.ID: server.CurrentRevisionID}, MCPGovernance: []ProfileMCPGovernanceInput{{ServerID: server.ID, MCPRevisionID: server.CurrentRevisionID, AcceptedContractRevisionID: contract.Revision.ID, VisibilityMode: "all_accepted"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InitializeRelayMigrationReadiness(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_state SET legacy_profile_id=$1,legacy_profile_state='pending' WHERE singleton`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	var contractHistoryBefore int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_contract_revisions`).Scan(&contractHistoryBefore); err != nil {
		t.Fatal(err)
	}

	enforced := completeRelayModeApplyForTest(t, st, relayRevisionID, "enforced", "enforcement-transition")
	if enforced.Mode != "enforced" || enforced.Migration.LegacyProfileState != "migrated_relay" || enforced.Migration.LegacyProfileID != legacy.ID || !enforced.Migration.RestorableSnapshot {
		t.Fatalf("enforced projection=%+v", enforced)
	}
	if _, err := st.Profile(ctx, legacy.ID); err != nil {
		t.Fatalf("legacy Profile was deleted: %v", err)
	}

	compatibility := completeRelayModeApplyForTest(t, st, relayRevisionID, "compatibility", "compatibility-rollback")
	if compatibility.Mode != "compatibility" || compatibility.Migration.LegacyProfileState != "migrated_relay" {
		t.Fatalf("rollback projection=%+v", compatibility)
	}
	bundle, _, err := st.RenderRoutingBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Mode != "compatibility" || len(bundle.Servers) != 1 || bundle.Servers[0].ServerID != server.ID {
		t.Fatalf("rollback bundle=%+v", bundle)
	}
	var contractHistoryAfter int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_contract_revisions`).Scan(&contractHistoryAfter); err != nil {
		t.Fatal(err)
	}
	if contractHistoryAfter != contractHistoryBefore {
		t.Fatalf("rollback changed Contract history %d -> %d", contractHistoryBefore, contractHistoryAfter)
	}
}

func TestLegacyMarkerRequiresCurrentRestorableCompatibilityBackup(t *testing.T) {
	for _, test := range []struct {
		name       string
		sourceMode string
		expires    string
		metadata   bool
	}{
		{name: "unrelated operation", sourceMode: "unrelated", expires: "30 days", metadata: true},
		{name: "expired", sourceMode: "current", expires: "-1 minute", metadata: true},
		{name: "missing desired manifest", sourceMode: "current", expires: "30 days"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			st := newIntegrationStore(t, true)
			_, _, _, relayRevisionID := setupPublishedRelayProfile(t, st, "backup-"+strings.ReplaceAll(test.name, " ", "-"))
			legacy, err := st.SaveProfile(ctx, uuid.NewString(), ProfileInput{Name: "shared-mcp", ClientKind: "shared", Category: "relay", MigrationState: "compatibility"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_state SET legacy_profile_id=$1,legacy_profile_state='pending' WHERE singleton`, legacy.ID); err != nil {
				t.Fatal(err)
			}
			prepared, operation := prepareSucceededRelayModeApplyForTest(t, st, relayRevisionID, "enforced", "invalid-backup-"+test.name)
			recordCompatibilityBackupForTest(t, st, operation.ID, test.sourceMode, test.expires, test.metadata)
			if err := st.FinalizeRelayConfigurationApply(ctx, operation.ID, relayRevisionID, prepared.RoutingHash); !errors.Is(err, ErrConflict) {
				t.Fatalf("finalization with %s backup returned %v, want conflict", test.name, err)
			}
			projection, err := st.RelayConfigurationProjection(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if projection.Mode != "compatibility" || projection.Migration.LegacyProfileState != "pending" || projection.Migration.RestorableSnapshot {
				t.Fatalf("invalid backup changed migration state: %+v", projection)
			}
		})
	}
}

func completeRelayModeApplyForTest(t *testing.T, st *Store, relayRevisionID, mode, key string) RelayConfigurationProjection {
	t.Helper()
	ctx := context.Background()
	prepared, operation := prepareSucceededRelayModeApplyForTest(t, st, relayRevisionID, mode, key)
	recordCompatibilityBackupForTest(t, st, operation.ID, "current", "30 days", true)
	if err := st.FinalizeRelayConfigurationApply(ctx, operation.ID, relayRevisionID, prepared.RoutingHash); err != nil {
		t.Fatal(err)
	}
	projection, err := st.RelayConfigurationProjection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func prepareSucceededRelayModeApplyForTest(t *testing.T, st *Store, relayRevisionID, mode, key string) (GovernanceApplyPreparation, domain.Operation) {
	t.Helper()
	ctx := context.Background()
	prepared, err := st.PrepareRelayConfigurationModeApply(ctx, relayRevisionID, nil, mode)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := st.CreateRelayConfigurationModeApplyOperation(ctx, relayRevisionID, nil, mode, strings.Repeat("a", 64), prepared.RoutingHash, key)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(map[string]any{"routingReloaded": true, "routingHash": prepared.RoutingHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE operations SET status='running' WHERE id=$1; UPDATE operation_targets SET status='succeeded',result=$2,finished_at=now(),governance_finalization_pending=true WHERE operation_id=$1`, operation.ID, jsonText(result)); err != nil {
		t.Fatal(err)
	}
	return prepared, operation
}

func recordCompatibilityBackupForTest(t *testing.T, st *Store, operationID, sourceMode, expires string, includeManifest bool) {
	t.Helper()
	ctx := context.Background()
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	var manifestBody []byte
	var manifestHash string
	if err := st.pool.QueryRow(ctx, `SELECT snapshot.manifest,snapshot.manifest_hash FROM target_desired_snapshots active JOIN desired_snapshots snapshot ON snapshot.id=active.snapshot_id WHERE active.target_id=$1`, relayTarget.ID).Scan(&manifestBody, &manifestHash); err != nil {
		t.Fatal(err)
	}
	metadata := json.RawMessage(`{}`)
	if includeManifest {
		body, err := json.Marshal(map[string]any{"desiredManifest": json.RawMessage(manifestBody)})
		if err != nil {
			t.Fatal(err)
		}
		metadata = body
	}
	var sourceOperationID any
	if sourceMode == "current" {
		sourceOperationID = operationID
	}
	_, err := st.pool.Exec(ctx, `INSERT INTO backups(id,bridge_backup_id,target_id,source_operation_id,target_revision,manifest_hash,created_at,expires_at,metadata) VALUES($1,$2,$3,$4,$5,$6,now(),now()+$7::interval,$8)`, uuid.NewString(), "backup-"+uuid.NewString(), relayTarget.ID, sourceOperationID, strings.Repeat("a", 64), manifestHash, expires, jsonText(metadata))
	if err != nil {
		t.Fatal(err)
	}
}

func TestGovernanceMigration005To006RepairsContractStatusesIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, false)
	for version, name := range []string{"001_initial.sql", "002_relay_projection.sql", "003_profile_revisions_bundles.sql", "004_mcp_profile_routing_governance.sql"} {
		applyHistoricalMigrationForTest(t, st, name, version+1)
	}
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "migration-status", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	firstContractID, secondContractID := uuid.NewString(), uuid.NewString()
	if _, err := st.pool.Exec(ctx, `INSERT INTO mcp_contract_revisions(id,server_id,revision,canonical_hash,normalized_contract) VALUES($1,$3,1,$4,'{}'),($2,$3,2,$5,'{}')`, firstContractID, secondContractID, server.ID, strings.Repeat("1", 64), strings.Repeat("2", 64)); err != nil {
		t.Fatal(err)
	}
	changedToolID, presentationToolID, newToolID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := st.pool.Exec(ctx, `INSERT INTO mcp_tools(id,server_id,name) VALUES($1,$4,'changed_tool'),($2,$4,'presentation_tool'),($3,$4,'new_tool')`, changedToolID, presentationToolID, newToolID, server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO mcp_contract_revision_tools(contract_revision_id,tool_id,position,input_schema,output_schema,annotations,presentation) VALUES
		($1,$3,0,'{"type":"object"}','{}','{}','{}'),
		($1,$4,1,'{}','{}','{}','{"title":"old"}'),
		($2,$3,0,'{"type":"string"}','{}','{}','{}'),
		($2,$4,1,'{}','{}','{}','{"title":"new"}'),
		($2,$5,2,'{}','{}','{}','{}')`, firstContractID, secondContractID, changedToolID, presentationToolID, newToolID); err != nil {
		t.Fatal(err)
	}
	applyHistoricalMigrationForTest(t, st, "005_mcp_profile_routing_governance_contract.sql", 5)
	var unchangedBeforeRepair int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_contract_revision_tools WHERE contract_revision_id=$1 AND status='unchanged'`, secondContractID).Scan(&unchangedBeforeRepair); err != nil {
		t.Fatal(err)
	}
	if unchangedBeforeRepair != 3 {
		t.Fatalf("005 fixture statuses unexpectedly repaired before 006: unchanged=%d", unchangedBeforeRepair)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := st.pool.Query(ctx, `SELECT t.name,crt.status FROM mcp_contract_revision_tools crt JOIN mcp_tools t ON t.id=crt.tool_id WHERE crt.contract_revision_id=$1 ORDER BY t.name`, secondContractID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	statuses := map[string]string{}
	for rows.Next() {
		var name, status string
		if err := rows.Scan(&name, &status); err != nil {
			t.Fatal(err)
		}
		statuses[name] = status
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"changed_tool": ContractToolPausedIncompatible, "presentation_tool": ContractToolChangedPresentation, "new_tool": ContractToolNewHidden}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("005->006 status repair=%v want=%v", statuses, want)
	}
}

func TestGovernanceMigration005To006PreservesHistoricalManifestV2Integration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, false)
	for version, name := range []string{
		"001_initial.sql",
		"002_relay_projection.sql",
		"003_profile_revisions_bundles.sql",
		"004_mcp_profile_routing_governance.sql",
		"005_mcp_profile_routing_governance_contract.sql",
	} {
		applyHistoricalMigrationForTest(t, st, name, version+1)
	}
	if err := st.BootstrapEnvironment(ctx, "governance-upgrade", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	relayTarget := integrationTarget(t, st, "local/shared-relay")
	legacy := bridgeprotocol.DesiredManifest{
		SchemaVersion:    bridgeprotocol.ManifestSchemaVersion,
		Target:           bridgeTarget(relayTarget),
		Skills:           []bridgeprotocol.SkillMember{},
		MCPServers:       []bridgeprotocol.MCPMember{},
		ManagedMemberIDs: []string{},
		RelayPort:        6276,
	}
	var relayRevisionID, relayHash, policyRevisionID, policyHash string
	if err := st.pool.QueryRow(ctx, `SELECT rs.applied_revision_id::text,rr.canonical_hash,ps.applied_revision_id::text,pr.canonical_hash FROM relay_configuration_state rs JOIN relay_configuration_revisions rr ON rr.id=rs.applied_revision_id CROSS JOIN global_policy_state ps JOIN global_policy_revisions pr ON pr.id=ps.applied_revision_id WHERE rs.singleton AND ps.singleton`).Scan(&relayRevisionID, &relayHash, &policyRevisionID, &policyHash); err != nil {
		t.Fatal(err)
	}
	routing := bridgeprotocol.RoutingBundle{
		SchemaVersion: 1, Mode: "compatibility",
		RelayConfigurationRevisionID: relayRevisionID, RelayConfigurationHash: relayHash,
		GlobalPolicyRevisionID: policyRevisionID, GlobalPolicyHash: policyHash,
		Servers: []bridgeprotocol.ServerContractDTO{}, Profiles: []bridgeprotocol.PublishedProfileDTO{},
	}
	routingBody, routingHash, err := routing.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	historicalManifest := legacy
	historicalManifest.SchemaVersion = bridgeprotocol.ManifestSchemaVersionV2
	historicalManifest.RelayGovernance = &bridgeprotocol.RelayGovernanceManifest{
		RelayConfigurationRevisionID: relayRevisionID,
		RelayConfigurationHash:       relayHash,
		RoutingBundle:                routingBody,
		RoutingHash:                  routingHash,
	}
	historicalBody, historicalHash, err := historicalManifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	historicalID := uuid.NewString()
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,1,'relay_config_apply',2,$3,$4)`, historicalID, relayTarget.ID, historicalHash, jsonText(historicalBody)); err != nil {
		t.Fatalf("insert 005 historical v2 snapshot: %v", err)
	}

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("upgrade 005 database with historical v2 snapshot: %v", err)
	}
	var canonicalBytes []byte
	if err := st.pool.QueryRow(ctx, `SELECT routing_bundle_canonical FROM desired_snapshots WHERE id=$1`, historicalID).Scan(&canonicalBytes); err != nil {
		t.Fatal(err)
	}
	if canonicalBytes != nil {
		t.Fatalf("006 fabricated canonical bytes for immutable historical snapshot: %q", canonicalBytes)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,2,'relay_config_apply',2,$3,$4)`, uuid.NewString(), relayTarget.ID, historicalHash, jsonText(historicalBody)); err == nil {
		t.Fatal("post-006 v2 snapshot without canonical routing bytes was accepted")
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest,routing_bundle_canonical) VALUES($1,$2,2,'relay_config_apply',2,$3,$4,$5)`, uuid.NewString(), relayTarget.ID, historicalHash, jsonText(historicalBody), routingBody); err != nil {
		t.Fatalf("post-006 v2 snapshot with matching canonical routing bytes was rejected: %v", err)
	}
}

func applyHistoricalMigrationForTest(t *testing.T, st *Store, name string, version int) {
	t.Helper()
	body, err := migrationFS.ReadFile("migrations/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(context.Background(), string(body)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
	if version == 1 {
		if _, err := st.pool.Exec(context.Background(), `CREATE TABLE schema_migrations (version bigint PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.pool.Exec(context.Background(), `INSERT INTO schema_migrations(version) VALUES($1)`, version); err != nil {
		t.Fatal(err)
	}
}

func assertGovernanceMigrationState(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	var versions int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 12 {
		t.Fatalf("migration versions=%d want 12", versions)
	}
	var generation string
	if err := st.pool.QueryRow(ctx, `SELECT value FROM app_meta WHERE key='schema_generation'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != "2" {
		t.Fatalf("schema generation=%q", generation)
	}
	var mode string
	if err := st.pool.QueryRow(ctx, `SELECT mode FROM relay_configuration_state WHERE singleton`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "compatibility" {
		t.Fatalf("relay mode=%q", mode)
	}
	var policyRevision, appliedPolicyRevision int64
	if err := st.pool.QueryRow(ctx, `SELECT r.revision, ar.revision FROM global_policy_state s JOIN global_policy_revisions r ON r.id=s.current_revision_id JOIN global_policy_revisions ar ON ar.id=s.applied_revision_id WHERE s.singleton`).Scan(&policyRevision, &appliedPolicyRevision); err != nil {
		t.Fatal(err)
	}
	if policyRevision != 1 || appliedPolicyRevision != 1 {
		t.Fatalf("global policy pointers=%d/%d", policyRevision, appliedPolicyRevision)
	}
}

func TestGovernanceSchemaInvariantsIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	if err := st.BootstrapEnvironment(ctx, "governance-integration", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	var manifestSchemaConstraintValidated bool
	if err := st.pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conrelid='desired_snapshots'::regclass AND conname='desired_snapshots_manifest_schema_match_check'`).Scan(&manifestSchemaConstraintValidated); err != nil {
		t.Fatal(err)
	}
	if !manifestSchemaConstraintValidated {
		t.Fatal("manifest schema match constraint did not validate existing snapshots")
	}
	if _, err := st.pool.Exec(ctx, `UPDATE relay_configuration_revisions SET metadata='{}' WHERE revision=1`); err == nil {
		t.Fatal("relay configuration revision accepted an update")
	}
	if _, err := st.pool.Exec(ctx, `DELETE FROM global_policy_revisions WHERE revision=1`); err == nil {
		t.Fatal("global policy revision accepted a delete")
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO relay_configuration_state(singleton,current_revision_id,applied_revision_id) SELECT true,current_revision_id,applied_revision_id FROM relay_configuration_state WHERE singleton`); err == nil {
		t.Fatal("relay configuration singleton accepted a second row")
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO global_policy_revisions(id,revision,canonical_hash,catalog_version,unclassified_mutating,reviewed_read_only) VALUES($1,2,$2,1,'invalid','allow')`, uuid.NewString(), strings.Repeat("c", 64)); err == nil {
		t.Fatal("invalid global policy decision accepted")
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO mcp_daily_aggregates(day,client_kind,decision,outcome,call_count) VALUES(current_date,'claude','allow','executed',-1)`); err == nil {
		t.Fatal("negative aggregate count accepted")
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO profile_revision_mcp_governance(profile_revision_id,server_id,mcp_revision_id,visibility_mode) VALUES($1,$2,$3,'invalid')`, uuid.NewString(), uuid.NewString(), uuid.NewString()); err == nil {
		t.Fatal("invalid profile visibility accepted")
	}

	var targetIDs []string
	rows, err := st.pool.Query(ctx, `SELECT id::text FROM targets ORDER BY target_key LIMIT 3`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		targetIDs = append(targetIDs, id)
	}
	rows.Close()
	if len(targetIDs) != 3 {
		t.Fatalf("fixture targets=%d", len(targetIDs))
	}
	op1, op2 := uuid.NewString(), uuid.NewString()
	for _, id := range []string{op1, op2} {
		if _, err := st.pool.Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,'edit','queued','{}')`, id); err != nil {
			t.Fatal(err)
		}
	}
	firstTarget, secondTarget, thirdTarget := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := st.pool.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id) VALUES($1,$2,$3)`, firstTarget, op1, targetIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,depends_on_target_id) VALUES($1,$2,$3,$4)`, secondTarget, op1, targetIDs[1], firstTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,depends_on_target_id) VALUES($1,$2,$3,$4)`, thirdTarget, op2, targetIDs[2], firstTarget); err == nil {
		t.Fatal("cross-operation dependency accepted")
	}
	if _, err := st.pool.Exec(ctx, `UPDATE operation_targets SET depends_on_target_id=$2 WHERE id=$1`, firstTarget, secondTarget); err == nil {
		t.Fatal("operation dependency cycle accepted")
	}

	relayTarget := integrationTarget(t, st, "local/shared-relay")
	legacy := bridgeprotocol.DesiredManifest{
		SchemaVersion:    bridgeprotocol.ManifestSchemaVersion,
		Target:           bridgeTarget(relayTarget),
		Skills:           []bridgeprotocol.SkillMember{},
		MCPServers:       []bridgeprotocol.MCPMember{},
		ManagedMemberIDs: []string{},
		RelayPort:        6276,
	}
	legacyBody, legacyHash, err := legacy.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,20,'target_edit',1,$3,$4)`, uuid.NewString(), relayTarget.ID, legacyHash, jsonText(legacyBody)); err != nil {
		t.Fatalf("v1 manifest was rejected: %v", err)
	}
	var relayRevisionID, relayHash, policyRevisionID, policyHash string
	if err := st.pool.QueryRow(ctx, `SELECT rs.applied_revision_id::text,rr.canonical_hash,ps.applied_revision_id::text,pr.canonical_hash FROM relay_configuration_state rs JOIN relay_configuration_revisions rr ON rr.id=rs.applied_revision_id CROSS JOIN global_policy_state ps JOIN global_policy_revisions pr ON pr.id=ps.applied_revision_id WHERE rs.singleton AND ps.singleton`).Scan(&relayRevisionID, &relayHash, &policyRevisionID, &policyHash); err != nil {
		t.Fatal(err)
	}
	routing := bridgeprotocol.RoutingBundle{
		SchemaVersion: 1, Mode: "compatibility",
		RelayConfigurationRevisionID: relayRevisionID, RelayConfigurationHash: relayHash,
		GlobalPolicyRevisionID: policyRevisionID, GlobalPolicyHash: policyHash,
		Servers: []bridgeprotocol.ServerContractDTO{}, Profiles: []bridgeprotocol.PublishedProfileDTO{},
	}
	routingBody, routingHash, err := routing.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	v2Manifest := legacy
	v2Manifest.SchemaVersion = bridgeprotocol.ManifestSchemaVersionV2
	v2Manifest.RelayGovernance = &bridgeprotocol.RelayGovernanceManifest{RelayConfigurationRevisionID: relayRevisionID, RelayConfigurationHash: relayHash, RoutingBundle: routingBody, RoutingHash: routingHash}
	v2Body, v2Hash, err := v2Manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	var v1Valid, v2Valid bool
	if err := st.pool.QueryRow(ctx, `SELECT validate_desired_manifest_v1($1::jsonb),validate_desired_manifest($1::jsonb)`, jsonText(legacyBody)).Scan(&v1Valid, &v2Valid); err != nil {
		t.Fatal(err)
	}
	if !v1Valid || !v2Valid {
		t.Fatalf("unexpected legacy validator results v1=%v v2=%v body=%s", v1Valid, v2Valid, legacyBody)
	}
	if err := st.pool.QueryRow(ctx, `SELECT validate_desired_manifest($1::jsonb)`, jsonText(v2Body)).Scan(&v2Valid); err != nil {
		t.Fatal(err)
	}
	if !v2Valid {
		t.Fatalf("v2 validator rejected a valid shared-relay manifest: %s", v2Body)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest,routing_bundle_canonical) VALUES($1,$2,21,'relay_config_apply',2,$3,$4,$5)`, uuid.NewString(), relayTarget.ID, v2Hash, jsonText(v2Body), routingBody); err != nil {
		t.Fatalf("v2 shared-relay manifest was rejected: %v", err)
	}
	badStructure := decodeObject(t, v2Body)
	governance := badStructure["relayGovernance"].(map[string]any)
	routingObject := governance["routingBundle"].(map[string]any)
	delete(routingObject, "globalPolicyHash")
	badV2, err := json.Marshal(badStructure)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest,routing_bundle_canonical) VALUES($1,$2,22,'relay_config_apply',2,$3,$4,$5)`, uuid.NewString(), relayTarget.ID, strings.Repeat("a", 64), jsonText(badV2), routingBody); err == nil {
		t.Fatal("structurally invalid v2 routing bundle was accepted")
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest,routing_bundle_canonical) VALUES($1,$2,23,'relay_config_apply',2,$3,$4,$5)`, uuid.NewString(), relayTarget.ID, strings.Repeat("b", 64), jsonText(v2Body), []byte(`{"schemaVersion":1}`)); err == nil {
		t.Fatal("v2 routing hash accepted unrelated canonical bytes")
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,24,'relay_config_apply',1,$3,$4)`, uuid.NewString(), relayTarget.ID, v2Hash, jsonText(v2Body)); err == nil {
		t.Fatal("v2 manifest bypassed canonical routing bytes through a v1 schema-version column")
	}
}

func TestRelayConfigurationRevisionRejectsMemberAppendAfterSeal(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	first, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "sealed-relay-first", Transport: "http", URL: "https://example.invalid/first"})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := st.SaveRelayConfiguration(ctx, RelayConfigurationInput{MCPServerIDs: []string{first.ID}, MCPRevisionIDs: map[string]string{first.ID: first.CurrentRevisionID}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "sealed-relay-second", Transport: "http", URL: "https://example.invalid/second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO relay_configuration_revision_mcp_servers(relay_configuration_revision_id,server_id,mcp_revision_id,position) VALUES($1,$2,$3,1)`, revision.ID, second.ID, second.CurrentRevisionID); err == nil {
		t.Fatal("sealed Relay Configuration revision accepted a member append")
	}
}

func TestContractRevisionRejectsToolAppendAfterSeal(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "sealed-contract", Transport: "http", URL: "https://example.invalid/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := st.ObserveContracts(ctx, ContractObservationInput{ServerID: server.ID, Tools: []ObservedToolInput{{Name: "first_tool", ReadOnlyHint: true}}})
	if err != nil {
		t.Fatal(err)
	}
	toolID := uuid.NewString()
	if _, err := st.pool.Exec(ctx, `INSERT INTO mcp_tools(id,server_id,name) VALUES($1,$2,'second_tool')`, toolID, server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO mcp_contract_revision_tools(contract_revision_id,tool_id,position,input_schema,output_schema,annotations,presentation,status) VALUES($1,$2,1,'{}','{}','{}','{}','new_hidden')`, observed.Revision.ID, toolID); err == nil {
		t.Fatal("sealed Contract revision accepted a tool append")
	}
}

func TestGovernanceFinalizationOwnershipMigrationFailsClosedForUnfinalizedSuccess(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, false)
	for version, name := range []string{
		"001_initial.sql",
		"002_relay_projection.sql",
		"003_profile_revisions_bundles.sql",
		"004_mcp_profile_routing_governance.sql",
		"005_mcp_profile_routing_governance_contract.sql",
		"006_mcp_governance_reload_integrity.sql",
		"007_mcp_governance_consistency.sql",
		"008_mcp_governance_revision_seals.sql",
	} {
		applyHistoricalMigrationForTest(t, st, name, version+1)
	}
	if err := st.BootstrapEnvironment(ctx, "ownership-upgrade-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	target := integrationTarget(t, st, "local/shared-relay")
	operationID := uuid.NewString()
	if _, err := st.pool.Exec(ctx, `INSERT INTO operations(id,kind,status,metadata,finished_at) VALUES($1,'apply','succeeded',$2,now())`, operationID, jsonText([]byte(`{"routingHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,finished_at) VALUES($1,$2,$3,'succeeded',now())`, uuid.NewString(), operationID, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var status, errorCode string
	var pending bool
	if err := st.pool.QueryRow(ctx, `SELECT o.status,o.error_code,bool_or(ot.governance_finalization_pending) FROM operations o JOIN operation_targets ot ON ot.operation_id=o.id WHERE o.id=$1 GROUP BY o.id`, operationID).Scan(&status, &errorCode, &pending); err != nil {
		t.Fatal(err)
	}
	if status != bridgeprotocol.OperationFailed || errorCode != "governance_finalization_interrupted" || pending {
		t.Fatalf("upgraded unfinalized operation status=%s code=%s pending=%v", status, errorCode, pending)
	}
	if _, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "scan", TargetIDs: []string{target.ID}, TargetRequests: map[string]any{target.ID: map[string]any{}}}); err != nil {
		t.Fatalf("upgrade retained ownership for unrecoverable finalization: %v", err)
	}
}

func TestGovernanceFinalizationOwnershipMigrationResolvesPreexistingTargetOverlap(t *testing.T) {
	for _, test := range []struct {
		name              string
		relayStatus       string
		wantOldStatus     string
		wantPendingTarget bool
	}{
		{name: "undispatched relay", relayStatus: bridgeprotocol.OperationQueued, wantOldStatus: bridgeprotocol.OperationFailed},
		{name: "running relay", relayStatus: bridgeprotocol.OperationRunning, wantOldStatus: bridgeprotocol.OperationRunning, wantPendingTarget: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			st := newIntegrationStore(t, false)
			for version, name := range []string{
				"001_initial.sql",
				"002_relay_projection.sql",
				"003_profile_revisions_bundles.sql",
				"004_mcp_profile_routing_governance.sql",
				"005_mcp_profile_routing_governance_contract.sql",
				"006_mcp_governance_reload_integrity.sql",
				"007_mcp_governance_consistency.sql",
				"008_mcp_governance_revision_seals.sql",
			} {
				applyHistoricalMigrationForTest(t, st, name, version+1)
			}
			if err := st.BootstrapEnvironment(ctx, "ownership-overlap-upgrade-host", "runner", "UTC", 6276); err != nil {
				t.Fatal(err)
			}
			skillTarget := integrationTarget(t, st, "local/claude")
			relayTarget := integrationTarget(t, st, "local/shared-relay")
			oldOperationID := uuid.NewString()
			if _, err := st.pool.Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,'apply','running',$2)`, oldOperationID, jsonText([]byte(`{"routingHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","profileRevisionId":"11111111-1111-4111-8111-111111111111"}`))); err != nil {
				t.Fatal(err)
			}
			if _, err := st.pool.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,finished_at) VALUES($1,$2,$3,'succeeded',now())`, uuid.NewString(), oldOperationID, skillTarget.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.pool.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status,started_at) VALUES($1,$2,$3,$4,CASE WHEN $4='running' THEN now() END)`, uuid.NewString(), oldOperationID, relayTarget.ID, test.relayStatus); err != nil {
				t.Fatal(err)
			}
			newOperationID := uuid.NewString()
			if _, err := st.pool.Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,'scan','queued','{}')`, newOperationID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.pool.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,status) VALUES($1,$2,$3,'queued')`, uuid.NewString(), newOperationID, skillTarget.ID); err != nil {
				t.Fatal(err)
			}

			if err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			var oldStatus, newStatus string
			var pendingSkill, pendingRelay bool
			if err := st.pool.QueryRow(ctx, `SELECT status FROM operations WHERE id=$1`, oldOperationID).Scan(&oldStatus); err != nil {
				t.Fatal(err)
			}
			if err := st.pool.QueryRow(ctx, `SELECT status FROM operations WHERE id=$1`, newOperationID).Scan(&newStatus); err != nil {
				t.Fatal(err)
			}
			if err := st.pool.QueryRow(ctx, `SELECT governance_finalization_pending FROM operation_targets WHERE operation_id=$1 AND target_id=$2`, oldOperationID, skillTarget.ID).Scan(&pendingSkill); err != nil {
				t.Fatal(err)
			}
			if err := st.pool.QueryRow(ctx, `SELECT governance_finalization_pending FROM operation_targets WHERE operation_id=$1 AND target_id=$2`, oldOperationID, relayTarget.ID).Scan(&pendingRelay); err != nil {
				t.Fatal(err)
			}
			if oldStatus != test.wantOldStatus || newStatus != bridgeprotocol.OperationQueued || pendingSkill || pendingRelay != test.wantPendingTarget {
				t.Fatalf("upgraded overlap old=%s new=%s pendingSkill=%v pendingRelay=%v", oldStatus, newStatus, pendingSkill, pendingRelay)
			}
			if _, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "scan", TargetIDs: []string{skillTarget.ID}, TargetRequests: map[string]any{skillTarget.ID: map[string]any{}}}); !errors.Is(err, ErrOperationActive) {
				t.Fatalf("replacement active owner was not preserved: %v", err)
			}
		})
	}
}

func TestGovernanceRoutingValidatorMatchesRuntimeAmbiguityRulesIntegration(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	serverID, mcpRevisionID, contractRevisionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	firstToolID, secondToolID := uuid.NewString(), uuid.NewString()
	profileID, profileRevisionID := uuid.NewString(), uuid.NewString()
	bundle := map[string]any{
		"schemaVersion":                1,
		"mode":                         "enforced",
		"relayConfigurationRevisionId": uuid.NewString(),
		"relayConfigurationHash":       strings.Repeat("1", 64),
		"globalPolicyRevisionId":       uuid.NewString(),
		"globalPolicyHash":             strings.Repeat("2", 64),
		"defaultProfileId":             profileID,
		"servers": []any{map[string]any{
			"serverId": serverID, "serverName": "server-one", "mcpConfigRevisionId": mcpRevisionID,
			"acceptedContractRevisionId": contractRevisionID, "acceptedContractHash": strings.Repeat("3", 64),
			"tools": []any{
				map[string]any{"toolId": firstToolID, "name": "first-tool", "inputSchema": map[string]any{}, "outputSchema": map[string]any{}, "annotations": map[string]any{}, "globalDecision": "confirm", "reasonCodes": []any{}, "paused": false},
				map[string]any{"toolId": secondToolID, "name": "second-tool", "inputSchema": map[string]any{}, "outputSchema": map[string]any{}, "annotations": map[string]any{}, "globalDecision": "allow", "reasonCodes": []any{}, "paused": false},
			},
		}},
		"profiles": []any{map[string]any{
			"profileId": profileID, "profileRevisionId": profileRevisionID, "profileRevisionHash": strings.Repeat("4", 64),
			"profileName": "profile-one", "clientKind": "claude",
			"servers": []any{map[string]any{
				"serverId": serverID, "mcpConfigRevisionId": mcpRevisionID, "acceptedContractRevisionId": contractRevisionID,
				"visibilityMode": "selected",
				"toolOverrides":  []any{map[string]any{"toolId": firstToolID, "visible": true}},
				"toolRules":      []any{map[string]any{"toolId": firstToolID, "decision": "deny"}},
			}},
		}},
	}
	validate := func(candidate map[string]any) bool {
		t.Helper()
		body, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		var valid bool
		if err := st.pool.QueryRow(ctx, `SELECT validate_routing_bundle_v1($1::jsonb)`, jsonText(body)).Scan(&valid); err != nil {
			t.Fatal(err)
		}
		return valid
	}
	if !validate(bundle) {
		t.Fatal("routing validator rejected the valid baseline bundle")
	}
	displayName := cloneJSONMap(t, bundle)
	displayName["profiles"].([]any)[0].(map[string]any)["profileName"] = "Archive race"
	if !validate(displayName) {
		t.Fatal("routing validator rejected a valid Profile display name")
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "duplicate tool name", mutate: func(candidate map[string]any) {
			tools := candidate["servers"].([]any)[0].(map[string]any)["tools"].([]any)
			tools[1].(map[string]any)["name"] = tools[0].(map[string]any)["name"]
		}},
		{name: "duplicate visibility override", mutate: func(candidate map[string]any) {
			server := candidate["profiles"].([]any)[0].(map[string]any)["servers"].([]any)[0].(map[string]any)
			override := server["toolOverrides"].([]any)[0]
			server["toolOverrides"] = append(server["toolOverrides"].([]any), override)
		}},
		{name: "duplicate policy rule", mutate: func(candidate map[string]any) {
			server := candidate["profiles"].([]any)[0].(map[string]any)["servers"].([]any)[0].(map[string]any)
			rule := server["toolRules"].([]any)[0]
			server["toolRules"] = append(server["toolRules"].([]any), rule)
		}},
		{name: "profile loosens global decision", mutate: func(candidate map[string]any) {
			server := candidate["profiles"].([]any)[0].(map[string]any)["servers"].([]any)[0].(map[string]any)
			server["toolRules"].([]any)[0].(map[string]any)["decision"] = "allow"
		}},
		{name: "ambiguous server name prefix", mutate: func(candidate map[string]any) {
			servers := candidate["servers"].([]any)
			candidate["servers"] = append(servers, map[string]any{
				"serverId": uuid.NewString(), "serverName": "server-one_child", "mcpConfigRevisionId": uuid.NewString(),
				"acceptedContractRevisionId": uuid.NewString(), "acceptedContractHash": strings.Repeat("5", 64), "tools": []any{},
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := cloneJSONMap(t, bundle)
			tt.mutate(candidate)
			if validate(candidate) {
				t.Fatal("routing validator accepted an ambiguous bundle")
			}
		})
	}
}

func cloneJSONMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(body, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
