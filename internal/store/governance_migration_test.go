package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

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
	if err := upgrade.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var versions int
	if err := upgrade.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 5 {
		t.Fatalf("migration reran or skipped: versions=%d", versions)
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
	if versions != 5 {
		t.Fatalf("migration versions=%d want 5", versions)
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
	v2 := decodeObject(t, legacyBody)
	v2["schemaVersion"] = 2
	v2["relayGovernance"] = map[string]any{
		"relayConfigurationRevisionId": uuid.NewString(),
		"relayConfigurationHash":       strings.Repeat("d", 64),
		"routingBundle":                map[string]any{"schemaVersion": 1},
		"routingHash":                  strings.Repeat("e", 64),
	}
	v2Body, err := json.Marshal(v2)
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
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,21,'relay_config_apply',2,$3,$4)`, uuid.NewString(), relayTarget.ID, strings.Repeat("f", 64), jsonText(v2Body)); err != nil {
		t.Fatalf("v2 shared-relay manifest was rejected: %v", err)
	}
	v2["target"] = map[string]any{
		"id": relayTarget.ID, "nodeId": relayTarget.NodeID, "nodeKind": relayTarget.NodeKind,
		"runtime": domain.RuntimeClaude, "managedUsername": relayTarget.ManagedUsername,
	}
	badV2, err := json.Marshal(v2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,22,'relay_config_apply',2,$3,$4)`, uuid.NewString(), relayTarget.ID, strings.Repeat("a", 64), jsonText(badV2)); err == nil {
		t.Fatal("v2 governance was accepted for a non-shared relay target")
	}
}
