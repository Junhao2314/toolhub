package store

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/skills"
)

func TestHermesExplicitSnapshotImportsIntegration(t *testing.T) {
	ctx := context.Background()
	st, adminID := openHermesIntegrationStore(t, ctx)
	suffix := strings.SplitN(uuid.NewString(), "-", 2)[0]
	nodeID, taskKey := enrollTestNode(t, st, adminID, "hermes-import-node-"+suffix)
	root := "/tmp/hermes-import-" + suffix + "/skills"
	path := root + "/example"
	firstPackage := hermesTestSkillPackage(t, "Hermes Import "+suffix, "first snapshot")
	secretValues := map[string]string{"TOKEN": "hermes-token-" + suffix}
	headerValues := map[string]string{"Authorization": "Bearer hermes-" + suffix}
	descriptor := hermesTestMCPDescriptor(t, taskKey, "hermes-mcp-"+suffix, "hermes-command", secretValues, headerValues)
	firstInventory := hermesTestInventory(root, path, firstPackage.SHA256, descriptor)

	var secretsBefore int
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM encrypted_secrets").Scan(&secretsBefore); err != nil {
		t.Fatal(err)
	}
	requests, err := st.ProcessAgentInventory(ctx, nodeID, firstInventory, true)
	if err != nil || len(requests) != 0 {
		t.Fatalf("ordinary Hermes inventory requested capture: requests=%+v err=%v", requests, err)
	}
	var secretsAfter, skillAssets, mcpAssets, hermesDeployments, hermesCaptures int
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM encrypted_secrets),
		(SELECT count(*) FROM skills skill JOIN skill_sources source ON source.id=skill.source_id WHERE source.kind='hermes-import' AND source.subdirectory=$1),
		(SELECT count(*) FROM mcp_servers WHERE source='hermes-import' AND runtime_name=$2),
		(SELECT count(*) FROM deployments WHERE runtime_kind='hermes' AND node_id=$3)+(SELECT count(*) FROM mcp_deployments WHERE runtime_kind='hermes' AND node_id=$3),
		(SELECT count(*) FROM mcp_capture_tokens WHERE purpose='hermes_snapshot' AND node_id=$3)`, path, descriptor.Name, nodeID).
		Scan(&secretsAfter, &skillAssets, &mcpAssets, &hermesDeployments, &hermesCaptures); err != nil {
		t.Fatal(err)
	}
	if secretsAfter != secretsBefore || skillAssets != 0 || mcpAssets != 0 || hermesDeployments != 0 || hermesCaptures != 0 {
		t.Fatalf("ordinary scan mutated central state: secrets=%d/%d skills=%d mcp=%d deployments=%d captures=%d", secretsBefore, secretsAfter, skillAssets, mcpAssets, hermesDeployments, hermesCaptures)
	}

	var skillDiscoveryID, mcpDiscoveryID string
	var generation int64
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM skill_discoveries
		WHERE node_id=$1 AND runtime_kind='hermes' AND canonical_path=$2`, nodeID, path).Scan(&skillDiscoveryID); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT id::text,observed_generation FROM mcp_runtime_bindings
		WHERE node_id=$1 AND runtime_kind='hermes' AND server_name=$2`, nodeID, descriptor.Name).Scan(&mcpDiscoveryID, &generation); err != nil {
		t.Fatal(err)
	}
	if _, err := st.QueueHermesSkillImport(ctx, skillDiscoveryID, strings.Repeat("0", 64), adminID); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("stale Skill snapshot error=%v", err)
	}
	if _, err := st.pool.Exec(ctx, "UPDATE nodes SET agent_capabilities='[]'::jsonb WHERE id=$1", nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.QueueHermesSkillImport(ctx, skillDiscoveryID, firstPackage.SHA256, adminID); !errors.Is(err, ErrAgentUpgradeRequired) {
		t.Fatalf("old Agent Skill import error=%v", err)
	}
	if _, err := st.ProcessAgentInventory(ctx, nodeID, firstInventory, false); err != nil {
		t.Fatal(err)
	}

	firstImported := completeHermesSkillImport(t, st, nodeID, skillDiscoveryID, firstPackage, adminID)
	var sourceKind, reviewStatus string
	if err := st.pool.QueryRow(ctx, `SELECT source.kind,skill.review_status FROM skills skill
		JOIN skill_sources source ON source.id=skill.source_id WHERE skill.id=$1`, firstImported.SkillID).
		Scan(&sourceKind, &reviewStatus); err != nil {
		t.Fatal(err)
	}
	if sourceKind != "hermes-import" || reviewStatus != "pending" {
		t.Fatalf("Hermes Skill source=%q review=%q", sourceKind, reviewStatus)
	}
	if err := st.ReviewSkill(ctx, firstImported.SkillID, "approved", adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSkillTargets(ctx, firstImported.SkillID, adminID, []DeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeHermes, Enabled: true}}, false); err == nil {
		t.Fatal("Hermes was accepted as a Skill target")
	}
	if _, err := st.SetSkillTargets(ctx, firstImported.SkillID, adminID, []DeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}, false); err != nil {
		t.Fatal(err)
	}
	var desiredVersion string
	if err := st.pool.QueryRow(ctx, `SELECT desired_version_id::text FROM deployments
		WHERE node_id=$1 AND runtime_kind='codex' AND skill_id=$2`, nodeID, firstImported.SkillID).Scan(&desiredVersion); err != nil {
		t.Fatal(err)
	}

	secondPackage := hermesTestSkillPackage(t, "Hermes Import "+suffix, "second snapshot")
	secondInventory := hermesTestInventory(root, path, secondPackage.SHA256, descriptor)
	if _, err := st.ProcessAgentInventory(ctx, nodeID, secondInventory, false); err != nil {
		t.Fatal(err)
	}
	var sourceChanged, skillDrift bool
	if err := st.pool.QueryRow(ctx, "SELECT source_changed,drift FROM skill_discoveries WHERE id=$1", skillDiscoveryID).Scan(&sourceChanged, &skillDrift); err != nil {
		t.Fatal(err)
	}
	if !sourceChanged || skillDrift {
		t.Fatalf("Hermes Skill sourceChanged=%v drift=%v", sourceChanged, skillDrift)
	}
	secondImported := completeHermesSkillImport(t, st, nodeID, skillDiscoveryID, secondPackage, adminID)
	if secondImported.SkillID != firstImported.SkillID || secondImported.Status != "update_available" {
		t.Fatalf("Hermes Skill re-import=%+v first=%+v", secondImported, firstImported)
	}
	var candidateCount int
	var desiredAfter string
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM updates WHERE skill_id=$1 AND status='available' AND candidate_sha256=$2),
		(SELECT desired_version_id::text FROM deployments WHERE node_id=$3 AND runtime_kind='codex' AND skill_id=$1)`,
		firstImported.SkillID, secondPackage.SHA256, nodeID).Scan(&candidateCount, &desiredAfter); err != nil {
		t.Fatal(err)
	}
	if candidateCount != 1 || desiredAfter != desiredVersion {
		t.Fatalf("re-import candidates=%d desired=%s want=%s", candidateCount, desiredAfter, desiredVersion)
	}

	if _, err := st.QueueHermesMCPImport(ctx, mcpDiscoveryID, generation, false, adminID); !errors.Is(err, ErrSecretConfirmation) {
		t.Fatalf("unconfirmed Hermes MCP import error=%v", err)
	}
	mcpJob, err := st.QueueHermesMCPImport(ctx, mcpDiscoveryID, generation, true, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.HermesMCPImportForScan(ctx, mcpDiscoveryID, generation, mcpJob.ID); err != nil {
		t.Fatal(err)
	}
	requests, err = st.ProcessAgentInventory(ctx, nodeID, secondInventory, true)
	if err != nil || len(requests) != 1 {
		t.Fatalf("explicit Hermes MCP capture requests=%+v err=%v", requests, err)
	}
	badCapture := MCPSecretCapture{Token: requests[0].Token, Runtime: domain.RuntimeHermes, Name: descriptor.Name, Identity: descriptor.Identity,
		Env: map[string]string{"TOKEN": "wrong"}, Headers: headerValues}
	if _, err := st.CaptureRuntimeMCP(ctx, nodeID, badCapture); err == nil {
		t.Fatal("mismatched Hermes MCP capture was accepted")
	}
	goodCapture := badCapture
	goodCapture.Env = secretValues
	firstMCP, err := st.CaptureRuntimeMCP(ctx, nodeID, goodCapture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CaptureRuntimeMCP(ctx, nodeID, goodCapture); err == nil {
		t.Fatal("Hermes MCP capture token replay was accepted")
	}
	var profileMemberships, mcpDeploymentCount int
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM mcp_profile_servers WHERE server_id=$1)+(SELECT count(*) FROM toolhub_profile_mcp_servers WHERE server_id=$1),
		(SELECT count(*) FROM mcp_deployments deployment JOIN mcp_profile_servers member ON member.profile_id=deployment.profile_id WHERE member.server_id=$1)`, firstMCP.ServerID).
		Scan(&profileMemberships, &mcpDeploymentCount); err != nil {
		t.Fatal(err)
	}
	if profileMemberships != 0 || mcpDeploymentCount != 0 {
		t.Fatalf("Hermes MCP import auto-targeted profiles=%d deployments=%d", profileMemberships, mcpDeploymentCount)
	}
	var secretRef string
	if err := st.pool.QueryRow(ctx, "SELECT env_refs->>'TOKEN' FROM mcp_servers WHERE id=$1 AND source='hermes-import' AND enabled", firstMCP.ServerID).Scan(&secretRef); err != nil {
		t.Fatal(err)
	}
	discoveries, err := st.ListDiscoveries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secretValues["TOKEN"], headerValues["Authorization"], descriptor.SecretFingerprint, secretRef} {
		if strings.Contains(string(discoveries), forbidden) {
			t.Fatalf("discovery projection leaked %q: %s", forbidden, discoveries)
		}
	}
	var discoveryRows []struct {
		ID         string   `json:"id"`
		EnvKeys    []string `json:"envKeys"`
		HeaderKeys []string `json:"headerKeys"`
	}
	if err := json.Unmarshal(discoveries, &discoveryRows); err != nil {
		t.Fatal(err)
	}
	var projectedKeys bool
	for _, row := range discoveryRows {
		if row.ID == mcpDiscoveryID && len(row.EnvKeys) == 1 && row.EnvKeys[0] == "TOKEN" &&
			len(row.HeaderKeys) == 1 && row.HeaderKeys[0] == "Authorization" {
			projectedKeys = true
		}
	}
	if !projectedKeys {
		t.Fatalf("discovery projection omitted secret key names: %s", discoveries)
	}

	secondDescriptor := hermesTestMCPDescriptor(t, taskKey, descriptor.Name, "hermes-command-v2", secretValues, headerValues)
	secondMCPInventory := hermesTestInventory(root, path, secondPackage.SHA256, secondDescriptor)
	if _, err := st.ProcessAgentInventory(ctx, nodeID, secondMCPInventory, false); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, "SELECT observed_generation,source_changed,drift FROM mcp_runtime_bindings WHERE id=$1", mcpDiscoveryID).
		Scan(&generation, &sourceChanged, &skillDrift); err != nil {
		t.Fatal(err)
	}
	if generation != 2 || !sourceChanged || skillDrift {
		t.Fatalf("Hermes MCP generation=%d sourceChanged=%v drift=%v", generation, sourceChanged, skillDrift)
	}
	mcpJob, err = st.QueueHermesMCPImport(ctx, mcpDiscoveryID, generation, true, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.HermesMCPImportForScan(ctx, mcpDiscoveryID, generation, mcpJob.ID); err != nil {
		t.Fatal(err)
	}
	requests, err = st.ProcessAgentInventory(ctx, nodeID, secondMCPInventory, true)
	if err != nil || len(requests) != 1 {
		t.Fatalf("Hermes MCP re-import requests=%+v err=%v", requests, err)
	}
	secondMCP, err := st.CaptureRuntimeMCP(ctx, nodeID, MCPSecretCapture{Token: requests[0].Token, Runtime: domain.RuntimeHermes,
		Name: secondDescriptor.Name, Identity: secondDescriptor.Identity, Env: secretValues, Headers: headerValues})
	if err != nil {
		t.Fatal(err)
	}
	if secondMCP.ServerID == firstMCP.ServerID {
		t.Fatal("Hermes MCP re-import reused the previous central server")
	}
	var importedServers int
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM mcp_servers WHERE id=ANY($1::uuid[]) AND source='hermes-import' AND enabled", []string{firstMCP.ServerID, secondMCP.ServerID}).Scan(&importedServers); err != nil {
		t.Fatal(err)
	}
	if importedServers != 2 {
		t.Fatalf("Hermes MCP re-import preserved %d servers, want 2", importedServers)
	}

	mcpJob, err = st.QueueHermesMCPImport(ctx, mcpDiscoveryID, generation, true, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.HermesMCPImportForScan(ctx, mcpDiscoveryID, generation, mcpJob.ID); err != nil {
		t.Fatal(err)
	}
	thirdDescriptor := hermesTestMCPDescriptor(t, taskKey, descriptor.Name, "hermes-command-v3", secretValues, headerValues)
	thirdInventory := hermesTestInventory(root, path, secondPackage.SHA256, thirdDescriptor)
	requests, err = st.ProcessAgentInventory(ctx, nodeID, thirdInventory, true)
	if err != nil || len(requests) != 0 {
		t.Fatalf("changed pinned source capture requests=%+v err=%v", requests, err)
	}
	var importStatus string
	if err := st.pool.QueryRow(ctx, "SELECT import_status,source_changed,drift FROM mcp_runtime_bindings WHERE id=$1", mcpDiscoveryID).
		Scan(&importStatus, &sourceChanged, &skillDrift); err != nil {
		t.Fatal(err)
	}
	if importStatus != "failed" || !sourceChanged || skillDrift {
		t.Fatalf("changed pinned import status=%s sourceChanged=%v drift=%v", importStatus, sourceChanged, skillDrift)
	}
}

func TestMCPSecretDeltaAuthorizationIntegration(t *testing.T) {
	ctx := context.Background()
	st, adminID := openHermesIntegrationStore(t, ctx)
	suffix := strings.SplitN(uuid.NewString(), "-", 2)[0]
	nodeID, taskKey := enrollTestNode(t, st, adminID, "secret-delta-node-"+suffix)
	oldValue := "old-secret-" + suffix
	descriptor := testDescriptor(t, taskKey, "secret-delta-"+suffix, "secret-command", oldValue)
	requests, err := st.ProcessAgentInventory(ctx, nodeID, domain.AgentInventory{Runtimes: testInventory(descriptor)}, true)
	if err != nil || len(requests) != 1 {
		t.Fatalf("baseline requests=%+v err=%v", requests, err)
	}
	if _, err := st.CaptureRuntimeMCP(ctx, nodeID, MCPSecretCapture{Token: requests[0].Token, Runtime: domain.RuntimeCodex,
		Name: descriptor.Name, Identity: descriptor.Identity, Env: map[string]string{"TOKEN": oldValue}}); err != nil {
		t.Fatal(err)
	}
	var serverID, oldSecretID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text,env_refs->>'TOKEN' FROM mcp_servers
		WHERE runtime_name=$1 AND source='runtime-auto'`, descriptor.Name).Scan(&serverID, &oldSecretID); err != nil {
		t.Fatal(err)
	}
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profileID, err := ensureManagedMCPProfileTx(ctx, tx, domain.RuntimeCodex, nodeID)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var existingMembers []string
	rows, err := st.pool.Query(ctx, "SELECT server_id::text FROM mcp_profile_servers WHERE profile_id=$1 ORDER BY server_id", profileID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var memberID string
		if err := rows.Scan(&memberID); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		existingMembers = append(existingMembers, memberID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	members := append(append([]string(nil), existingMembers...), serverID)
	if err := st.SetMCPProfileServers(ctx, profileID, members); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetMCPDeployments(ctx, profileID, adminID, []MCPDeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}, false); err != nil {
		t.Fatal(err)
	}
	var deploymentID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM mcp_deployments
		WHERE profile_id=$1 AND node_id=$2 AND runtime_kind='codex'`, profileID, nodeID).Scan(&deploymentID); err != nil {
		t.Fatal(err)
	}
	_, payload, err := st.MCPDeploymentPayload(ctx, deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	oldTask, err := st.CreateNodeTask(ctx, nodeID, "", "apply_mcp", payload)
	if err != nil {
		t.Fatal(err)
	}
	newValue := "new-secret-" + suffix
	patch := MCPServerPatch{SecretChanges: &MCPSecretChanges{Env: MCPSecretDelta{Set: map[string]string{"TOKEN": newValue}}}, Actor: adminID}
	if err := st.UpdateMCPServer(ctx, serverID, patch); !errors.Is(err, ErrSecretConfirmation) {
		t.Fatalf("unconfirmed secret delta error=%v", err)
	}
	patch.ConfirmTargets = true
	if err := st.UpdateMCPServer(ctx, serverID, patch); err != nil {
		t.Fatal(err)
	}
	var newSecretID string
	if err := st.pool.QueryRow(ctx, "SELECT env_refs->>'TOKEN' FROM mcp_servers WHERE id=$1", serverID).Scan(&newSecretID); err != nil {
		t.Fatal(err)
	}
	if newSecretID == oldSecretID || newSecretID == "" {
		t.Fatalf("secret reference was not replaced: old=%s new=%s", oldSecretID, newSecretID)
	}
	oldPlaintext, err := st.AgentSecretValue(ctx, nodeID, oldSecretID)
	if err != nil || string(oldPlaintext) != oldValue {
		t.Fatalf("active task old secret=%q err=%v", oldPlaintext, err)
	}
	newPlaintext, err := st.AgentSecretValue(ctx, nodeID, newSecretID)
	if err != nil || string(newPlaintext) != newValue {
		t.Fatalf("desired deployment new secret=%q err=%v", newPlaintext, err)
	}
	if err := st.CompleteTask(ctx, nodeID, oldTask.ID, "failed", json.RawMessage(`{"error":"stale task stopped"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AgentSecretValue(ctx, nodeID, oldSecretID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("terminal task still authorized old secret: %v", err)
	}
	var oldRecordExists bool
	if err := st.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM encrypted_secrets WHERE id=$1)", oldSecretID).Scan(&oldRecordExists); err != nil {
		t.Fatal(err)
	}
	if !oldRecordExists {
		t.Fatal("old encrypted secret history was deleted")
	}
	servers, err := st.ListMCPServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{oldValue, newValue, oldSecretID, newSecretID} {
		if strings.Contains(string(servers), forbidden) {
			t.Fatalf("MCP server projection leaked %q: %s", forbidden, servers)
		}
	}
	var serverRows []struct {
		ID      string   `json:"id"`
		EnvKeys []string `json:"envKeys"`
	}
	if err := json.Unmarshal(servers, &serverRows); err != nil {
		t.Fatal(err)
	}
	var projectedKey bool
	for _, row := range serverRows {
		if row.ID == serverID && len(row.EnvKeys) == 1 && row.EnvKeys[0] == "TOKEN" {
			projectedKey = true
		}
	}
	if !projectedKey {
		t.Fatalf("MCP server projection omitted env key names: %s", servers)
	}
}

func TestHermesSharedObservationRemainsReadOnlyIntegration(t *testing.T) {
	ctx := context.Background()
	st, adminID := openHermesIntegrationStore(t, ctx)
	suffix := strings.SplitN(uuid.NewString(), "-", 2)[0]
	nodeID, _ := enrollTestNode(t, st, adminID, "hermes-shared-node-"+suffix)
	serverName := "hermes-shared-" + suffix
	descriptor, err := protocol.NormalizeMCPDescriptor(domain.RuntimeHermes, domain.MCPDescriptor{
		Name: serverName, Transport: "stdio", Command: "shared-hermes-command",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := domain.SharedSourceInventory{
		Name: "hermes-shared-source-" + suffix, Mode: "observed", SkillsRoot: "/tmp/hermes-shared-" + suffix + "/skills",
		MCPManifestPath: "/tmp/hermes-shared-" + suffix + "/mcp.json", Status: "observed",
		MCPServers: []domain.SharedMCPServerInventory{{Descriptor: descriptor, Enabled: true}},
		Consumers: []domain.SharedConsumerInventory{{
			Kind: domain.RuntimeHermes, MCPEnabled: true, State: "observed",
			MCPBindings: []domain.SharedMCPBindingInventory{{ServerName: serverName, Enabled: true, State: "observed"}},
		}},
	}
	inventory := domain.AgentInventory{
		Runtimes: []domain.InventoryRuntime{{Kind: domain.RuntimeHermes, RootPath: "/tmp/hermes-shared-" + suffix + "/runtime",
			Config: map[string]any{"capabilities": []any{domain.CapabilityHermesReadOnlyImportV1}}, Inventory: map[string]any{"skills": []any{}}}},
		SharedSources: []domain.SharedSourceInventory{source},
	}
	if _, err := st.ProcessAgentInventory(ctx, nodeID, inventory, false); err != nil {
		t.Fatal(err)
	}
	var bindingID, controlMode, importStatus string
	var desiredEnabled, drift bool
	if err := st.pool.QueryRow(ctx, `SELECT id::text,control_mode,import_status,desired_enabled,drift
		FROM mcp_runtime_bindings WHERE node_id=$1 AND runtime_kind='hermes' AND server_name=$2`, nodeID, serverName).
		Scan(&bindingID, &controlMode, &importStatus, &desiredEnabled, &drift); err != nil {
		t.Fatal(err)
	}
	if controlMode != "read_only_source" || importStatus != "not_applicable" || desiredEnabled || drift {
		t.Fatalf("shared Hermes binding control=%s import=%s desired=%v drift=%v", controlMode, importStatus, desiredEnabled, drift)
	}
	if _, err := st.QueueHermesMCPImport(ctx, bindingID, 1, true, adminID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("shared-only Hermes binding import error=%v", err)
	}
	discoveries, err := st.ListDiscoveries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(discoveries), bindingID) {
		t.Fatalf("shared-only Hermes binding appeared as an import candidate: %s", discoveries)
	}
	target, err := st.TargetView(ctx, nodeID, domain.RuntimeHermes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(target), serverName) || !strings.Contains(string(target), `"readOnly":true`) {
		t.Fatalf("Hermes Runtime View lost shared observation: %s", target)
	}
}

func TestHermesMigrationUpgradeAndCleanInstallIntegration(t *testing.T) {
	databaseURL := hermesIntegrationDatabaseURL(t)
	ctx := context.Background()

	t.Run("upgrade from 010", func(t *testing.T) {
		st := openIsolatedHermesSchema(t, ctx, databaseURL)
		applyMigrationsThrough(t, ctx, st, 10)
		if _, err := st.BootstrapAdmin(ctx, "admin", "admin@example.test", "Admin", "ToolHub-Test-Password-2026"); err != nil {
			t.Fatal(err)
		}
		var adminID string
		if err := st.pool.QueryRow(ctx, "SELECT id::text FROM users WHERE username='admin'").Scan(&adminID); err != nil {
			t.Fatal(err)
		}
		nodeID, _ := enrollTestNode(t, st, adminID, "legacy-hermes-node")
		pkg := hermesTestSkillPackage(t, "Legacy Hermes", "legacy snapshot")
		imported, err := st.ImportSkill(ctx, SourceInput{Kind: "upload", Name: "legacy-hermes.zip"}, pkg, map[string]any{}, adminID)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.ReviewSkill(ctx, imported.SkillID, "approved", adminID); err != nil {
			t.Fatal(err)
		}
		serverID, err := st.CreateMCPServer(ctx, MCPServerInput{Name: "legacy-hermes-mcp", Transport: "stdio", Command: "legacy", Env: map[string]string{"TOKEN": "legacy-secret"}, Enabled: true}, adminID)
		if err != nil {
			t.Fatal(err)
		}
		mcpProfileID, err := st.CreateMCPProfile(ctx, "legacy-hermes-profile", "", adminID, []string{serverID})
		if err != nil {
			t.Fatal(err)
		}
		toolhubProfileID, err := st.CreateToolHubProfile(ctx, "legacy-hermes-selection", "", adminID)
		if err != nil {
			t.Fatal(err)
		}
		deploymentID := uuid.NewString()
		mcpDeploymentID := uuid.NewString()
		discoveryID := uuid.NewString()
		bindingID := uuid.NewString()
		activationID := uuid.NewString()
		adoptJobID := uuid.NewString()
		profileJobID := uuid.NewString()
		adoptTaskID := uuid.NewString()
		deployTaskID := uuid.NewString()
		if _, err := st.pool.Exec(ctx, `
			INSERT INTO deployments(id,node_id,runtime_kind,skill_id,desired_version_id,desired_enabled,state)
			VALUES($1,$2,'hermes',$3,$4,true,'pending');
			INSERT INTO skill_discoveries(id,node_id,runtime_kind,canonical_path,name,directory_hash,managed,adoption_status,adopted_skill_id,adopted_version_id)
			VALUES($5,$2,'hermes','/tmp/legacy-hermes/skill','Legacy Hermes',$6,true,'adopted',$3,$4);
			INSERT INTO mcp_deployments(id,profile_id,node_id,runtime_kind,desired_enabled,desired_hash,state)
			VALUES($7,$8,$2,'hermes',true,'legacy-hash','pending');
			INSERT INTO mcp_runtime_bindings(id,node_id,runtime_kind,server_name,identity,server_id,profile_id,deployment_id,
				desired_enabled,observed_config_fingerprint,observed_secret_fingerprint,missing,drift)
			VALUES($9,$2,'hermes','legacy-hermes-mcp','legacy-identity',$10,$8,$7,true,'legacy-config','legacy-secret-fingerprint',false,true);
			INSERT INTO toolhub_profile_activations(id,node_id,runtime_kind,profile_id,state,activated_by)
			VALUES($11,$2,'hermes',$12,'active',$13);
			INSERT INTO jobs(id,kind,status,payload,created_by) VALUES
				($14,'skill_adopt','running',jsonb_build_object('discoveryId',$5::text),$13),
				($15,'profile_activate','pending',jsonb_build_object('runtime','hermes'),$13);
			INSERT INTO node_tasks(id,node_id,job_id,kind,payload,signature,status) VALUES
				($16,$2,$14,'adopt_skill',jsonb_build_object('discoveryId',$5::text,'runtime','hermes'),'legacy-signature','pending'),
				($17,$2,NULL,'deploy_skill',jsonb_build_object('runtime','hermes'),'legacy-signature','running')`,
			deploymentID, nodeID, imported.SkillID, imported.VersionID, discoveryID, strings.Repeat("f", 64), mcpDeploymentID,
			mcpProfileID, bindingID, serverID, activationID, toolhubProfileID, adminID, adoptJobID, profileJobID, adoptTaskID, deployTaskID); err != nil {
			t.Fatal(err)
		}
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}

		var skillState, mcpState, activationState, skillControl, bindingControl, adoptJobState, profileJobState, adoptTaskState, deployTaskState string
		var skillDesired, mcpDesired, skillDrift, bindingDrift bool
		if err := st.pool.QueryRow(ctx, `SELECT
			(SELECT state FROM deployments WHERE id=$1),(SELECT desired_enabled FROM deployments WHERE id=$1),
			(SELECT state FROM mcp_deployments WHERE id=$2),(SELECT desired_enabled FROM mcp_deployments WHERE id=$2),
			(SELECT state FROM toolhub_profile_activations WHERE id=$3),
			(SELECT control_mode FROM skill_discoveries WHERE id=$4),(SELECT drift FROM skill_discoveries WHERE id=$4),
			(SELECT control_mode FROM mcp_runtime_bindings WHERE id=$5),(SELECT drift FROM mcp_runtime_bindings WHERE id=$5),
			(SELECT status FROM jobs WHERE id=$6),(SELECT status FROM jobs WHERE id=$7),
			(SELECT status FROM node_tasks WHERE id=$8),(SELECT status FROM node_tasks WHERE id=$9)`, deploymentID, mcpDeploymentID,
			activationID, discoveryID, bindingID, adoptJobID, profileJobID, adoptTaskID, deployTaskID).
			Scan(&skillState, &skillDesired, &mcpState, &mcpDesired, &activationState, &skillControl, &skillDrift,
				&bindingControl, &bindingDrift, &adoptJobState, &profileJobState, &adoptTaskState, &deployTaskState); err != nil {
			t.Fatal(err)
		}
		if skillState != "legacy_read_only" || skillDesired || mcpState != "legacy_read_only" || mcpDesired || activationState != "legacy_read_only" ||
			skillControl != "read_only_source" || skillDrift || bindingControl != "read_only_source" || bindingDrift ||
			adoptJobState != "cancelled" || profileJobState != "cancelled" || adoptTaskState != "cancelled" || deployTaskState != "cancelled" {
			t.Fatalf("legacy projection skill=%s/%v mcp=%s/%v activation=%s discoveries=%s/%v,%s/%v jobs=%s,%s tasks=%s,%s",
				skillState, skillDesired, mcpState, mcpDesired, activationState, skillControl, skillDrift, bindingControl, bindingDrift,
				adoptJobState, profileJobState, adoptTaskState, deployTaskState)
		}
		var preserved bool
		if err := st.pool.QueryRow(ctx, `SELECT
			EXISTS(SELECT 1 FROM skills WHERE id=$1) AND EXISTS(SELECT 1 FROM mcp_servers WHERE id=$2)
			AND EXISTS(SELECT 1 FROM encrypted_secrets WHERE metadata->>'mcpServerId'=$2::text)`, imported.SkillID, serverID).Scan(&preserved); err != nil {
			t.Fatal(err)
		}
		if !preserved {
			t.Fatal("legacy central assets or encrypted secrets were not preserved")
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO deployments(id,node_id,runtime_kind,skill_id,desired_version_id,desired_enabled,state)
			VALUES($1,$2,'hermes',$3,$4,false,'legacy_read_only')`, uuid.NewString(), nodeID, imported.SkillID, imported.VersionID); err == nil {
			t.Fatal("migration allowed a new Hermes Skill deployment")
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO toolhub_profile_activations(id,node_id,runtime_kind,profile_id,state)
			VALUES($1,$2,'hermes',$3,'legacy_read_only')`, uuid.NewString(), nodeID, toolhubProfileID); err == nil {
			t.Fatal("migration allowed a new Hermes Profile activation")
		}
	})

	t.Run("clean install", func(t *testing.T) {
		st := openIsolatedHermesSchema(t, ctx, databaseURL)
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		var versionApplied, capabilitiesArray bool
		if err := st.pool.QueryRow(ctx, `SELECT
			EXISTS(SELECT 1 FROM schema_migrations WHERE version=11),
			EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='nodes'
				AND column_name='agent_capabilities' AND data_type='jsonb')`).Scan(&versionApplied, &capabilitiesArray); err != nil {
			t.Fatal(err)
		}
		if !versionApplied || !capabilitiesArray {
			t.Fatalf("clean migration version=%v capabilities=%v", versionApplied, capabilitiesArray)
		}
	})
}

func openHermesIntegrationStore(t *testing.T, ctx context.Context) (*Store, string) {
	t.Helper()
	databaseURL := hermesIntegrationDatabaseURL(t)
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(ctx, databaseURL, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = st.BootstrapAdmin(ctx, "admin", "admin@example.test", "Admin", "ToolHub-Test-Password-2026")
	var adminID string
	if err := st.pool.QueryRow(ctx, `SELECT user_account.id::text FROM users user_account
		JOIN user_roles membership ON membership.user_id=user_account.id JOIN roles role ON role.id=membership.role_id
		WHERE role.name='admin' ORDER BY user_account.created_at LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	return st, adminID
}

func hermesIntegrationDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "toolhub_discovery_test") {
		t.Fatal("integration test database URL must target toolhub_discovery_test")
	}
	return databaseURL
}

func completeHermesSkillImport(t *testing.T, st *Store, nodeID, discoveryID string, pkg skills.Package, actor string) ImportedSkill {
	t.Helper()
	ctx := context.Background()
	job, err := st.QueueHermesSkillImport(ctx, discoveryID, pkg.SHA256, actor)
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.HermesSkillSnapshotForImport(ctx, discoveryID, pkg.SHA256, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload := protocol.ImportSkillSnapshotPayload{DiscoveryID: discoveryID, Runtime: domain.RuntimeHermes, Path: target.Path, SHA256: pkg.SHA256}
	task, err := st.CreateNodeTaskWithOptions(ctx, nodeID, job.ID, "import_skill_snapshot", payload, NodeTaskOptions{
		TargetKind: "skill_discovery", TargetID: discoveryID, SemanticKey: "integration-hermes-snapshot:" + discoveryID + ":" + pkg.SHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := st.ImportHermesSkillSnapshot(ctx, nodeID, discoveryID, task.ID, pkg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(protocol.ImportSkillSnapshotResult{DiscoveryID: discoveryID, SkillID: imported.SkillID,
		VersionID: imported.VersionID, SHA256: imported.SHA256, MarkerWritten: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteTask(ctx, nodeID, task.ID, "succeeded", result); err != nil {
		t.Fatal(err)
	}
	return imported
}

func hermesTestInventory(root, path, skillSHA string, descriptor domain.MCPDescriptor) domain.AgentInventory {
	return domain.AgentInventory{Runtimes: []domain.InventoryRuntime{{
		Kind: domain.RuntimeHermes, RootPath: root,
		Config: map[string]any{"capabilities": []any{domain.CapabilityHermesReadOnlyImportV1}},
		Inventory: map[string]any{"skills": []any{map[string]any{
			"name": "example", "path": path, "sha256": skillSHA, "managed": false, "protected": false, "disabled": false,
		}}},
		MCPServers: []domain.MCPDescriptor{descriptor},
	}}}
}

func hermesTestMCPDescriptor(t *testing.T, key []byte, name, command string, environment, headers map[string]string) domain.MCPDescriptor {
	t.Helper()
	descriptor, err := protocol.NormalizeMCPDescriptor(domain.RuntimeHermes, domain.MCPDescriptor{
		Name: name, Transport: "stdio", Command: command, Args: []string{"--stdio"}, EnvKeys: mapKeys(environment), HeaderKeys: mapKeys(headers),
	})
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(environment)+len(headers))
	for name, value := range environment {
		values["env:"+name] = value
	}
	for name, value := range headers {
		values["header:"+name] = value
	}
	descriptor.SecretFingerprint = security.FingerprintSecretMap(key, values)
	return descriptor
}

func hermesTestSkillPackage(t *testing.T, name, body string) skills.Package {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("---\nname: " + name + "\nlicense: MIT\n---\n" + body + "\n"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	pkg, err := skills.ScanZIP(buffer.Bytes(), skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func openIsolatedHermesSchema(t *testing.T, ctx context.Context, databaseURL string) *Store {
	t.Helper()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "hermes_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	st, err := Open(ctx, parsed.String(), cipher)
	if err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		_, _ = adminPool.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
		adminPool.Close()
	})
	return st
}

func applyMigrationsThrough(t *testing.T, ctx context.Context, st *Store, through int64) {
	t.Helper()
	if _, err := st.pool.Exec(ctx, "CREATE TABLE schema_migrations (version bigint PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := strconv.ParseInt(strings.SplitN(entry.Name(), "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if version > through {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		tx, err := st.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, migrationExecutionSQL(entry.Name(), body)); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", version); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}
