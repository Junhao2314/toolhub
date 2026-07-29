package store

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/skills"
)

func TestRuntimeMCPAutoAdoptionIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "toolhub_discovery_test") {
		t.Fatal("integration test database URL must target toolhub_discovery_test")
	}
	ctx := context.Background()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(ctx, databaseURL, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BootstrapAdmin(ctx, "admin", "admin@example.test", "Admin", "ToolHub-Test-Password-2026"); err != nil {
		t.Fatal(err)
	}
	schedules, err := st.Schedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, schedule := range schedules {
		kinds[schedule.Kind] = true
	}
	if !kinds["update_check"] || !kinds["sync"] || !kinds["mcp_sync"] || kinds["shared_sync"] {
		t.Fatalf("default reconciliation schedules missing: %+v", schedules)
	}
	var adminID string
	if err := st.pool.QueryRow(ctx, "SELECT id::text FROM users WHERE username='admin'").Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	firstNode, firstKey := enrollTestNode(t, st, adminID, "node-one")
	firstDescriptor := testDescriptor(t, firstKey, "example", "npx", "shared-secret")
	firstDescriptor.SecretFingerprint = security.FingerprintSecretMap(firstKey, map[string]string{"TOKEN": "shared-secret"})
	requests, err := st.ProcessAgentInventory(ctx, firstNode, domain.AgentInventory{Runtimes: testInventory(firstDescriptor)}, true)
	if err != nil || len(requests) != 1 {
		t.Fatalf("first discovery: requests=%+v err=%v", requests, err)
	}
	if _, err := st.CaptureRuntimeMCP(ctx, firstNode, MCPSecretCapture{Token: requests[0].Token, Runtime: "codex", Name: "example", Identity: firstDescriptor.Identity, Secrets: map[string]string{"TOKEN": "shared-secret"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CaptureRuntimeMCP(ctx, firstNode, MCPSecretCapture{Token: requests[0].Token, Runtime: "codex", Name: "example", Identity: firstDescriptor.Identity, Secrets: map[string]string{"TOKEN": "shared-secret"}}); err == nil {
		t.Fatal("capture token replay was accepted")
	}

	secondNode, secondKey := enrollTestNode(t, st, adminID, "node-two")
	secondDescriptor := testDescriptor(t, secondKey, "example", "npx", "shared-secret")
	requests, err = st.ProcessAgentInventory(ctx, secondNode, domain.AgentInventory{Runtimes: testInventory(secondDescriptor)}, true)
	if err != nil || len(requests) != 1 {
		t.Fatalf("second discovery: requests=%+v err=%v", requests, err)
	}
	if _, err := st.CaptureRuntimeMCP(ctx, secondNode, MCPSecretCapture{Token: requests[0].Token, Runtime: "codex", Name: "example", Identity: secondDescriptor.Identity, Env: map[string]string{"TOKEN": "shared-secret"}}); err != nil {
		t.Fatal(err)
	}
	var serverCount int
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM mcp_servers WHERE source='runtime-auto'").Scan(&serverCount); err != nil || serverCount != 1 {
		t.Fatalf("same configuration was not reused: count=%d err=%v", serverCount, err)
	}

	thirdNode, thirdKey := enrollTestNode(t, st, adminID, "node-three")
	thirdDescriptor := testDescriptor(t, thirdKey, "example", "npx", "different-secret")
	requests, err = st.ProcessAgentInventory(ctx, thirdNode, domain.AgentInventory{Runtimes: testInventory(thirdDescriptor)}, true)
	if err != nil || len(requests) != 1 {
		t.Fatalf("third discovery: requests=%+v err=%v", requests, err)
	}
	if _, err := st.CaptureRuntimeMCP(ctx, thirdNode, MCPSecretCapture{Token: requests[0].Token, Runtime: "codex", Name: "example", Identity: thirdDescriptor.Identity, Env: map[string]string{"TOKEN": "different-secret"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM mcp_servers WHERE source='runtime-auto'").Scan(&serverCount); err != nil || serverCount != 2 {
		t.Fatalf("different secret did not split the server: count=%d err=%v", serverCount, err)
	}

	var desiredHash, actualHash, state string
	if err := st.pool.QueryRow(ctx, `SELECT desired_hash,actual_hash,state FROM mcp_deployments WHERE node_id=$1`, firstNode).Scan(&desiredHash, &actualHash, &state); err != nil {
		t.Fatal(err)
	}
	if desiredHash == "" || actualHash != desiredHash || state != "in_sync" {
		t.Fatalf("first baseline was not in sync: desired=%q actual=%q state=%q", desiredHash, actualHash, state)
	}
	var firstServerID string
	if err := st.pool.QueryRow(ctx, "SELECT server_id::text FROM mcp_runtime_bindings WHERE node_id=$1", firstNode).Scan(&firstServerID); err != nil {
		t.Fatal(err)
	}
	centralCommand := "centrally-managed-command"
	if err := st.UpdateMCPServer(ctx, firstServerID, MCPServerPatch{Command: &centralCommand}); err != nil {
		t.Fatal(err)
	}
	var desiredDrift bool
	if err := st.pool.QueryRow(ctx, "SELECT drift FROM mcp_runtime_bindings WHERE node_id=$1", firstNode).Scan(&desiredDrift); err != nil || !desiredDrift {
		t.Fatalf("central MCP edit did not mark desired drift: drift=%v err=%v", desiredDrift, err)
	}

	drifted := testDescriptor(t, firstKey, "example", "different-command", "shared-secret")
	if requests, err = st.ProcessAgentInventory(ctx, firstNode, domain.AgentInventory{Runtimes: testInventory(drifted)}, true); err != nil || len(requests) != 0 {
		t.Fatalf("known drift requested capture: requests=%+v err=%v", requests, err)
	}
	var drift bool
	if err := st.pool.QueryRow(ctx, "SELECT drift FROM mcp_runtime_bindings WHERE node_id=$1", firstNode).Scan(&drift); err != nil || !drift {
		t.Fatalf("known MCP edit was not drift: drift=%v err=%v", drift, err)
	}

	discoveries, err := st.ListDiscoveries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	text := string(discoveries)
	if strings.Contains(text, "shared-secret") || strings.Contains(text, firstDescriptor.SecretFingerprint) {
		t.Fatalf("discovery API projection leaked secret material: %s", text)
	}
	var leaked bool
	if err := st.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM runtimes WHERE config::text LIKE '%shared-secret%' OR inventory::text LIKE '%shared-secret%'
		UNION ALL SELECT 1 FROM jobs WHERE payload::text LIKE '%shared-secret%' OR result::text LIKE '%shared-secret%'
		UNION ALL SELECT 1 FROM audit_events WHERE metadata::text LIKE '%shared-secret%')`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("plaintext secret leaked into ordinary persisted JSON")
	}

	pkg := testDiscoveredSkillPackage(t)
	skillInventory := []domain.InventoryRuntime{{Kind: "codex", RootPath: "/tmp/codex/skills", Config: map[string]any{}, Inventory: map[string]any{"skills": []any{map[string]any{"name": "local-skill", "path": "/tmp/codex/skills/local-skill", "sha256": pkg.SHA256, "managed": false, "protected": false, "disabled": false}}}}}
	if _, err := st.ProcessAgentInventory(ctx, firstNode, domain.AgentInventory{Runtimes: skillInventory}, false); err != nil {
		t.Fatal(err)
	}
	var missing bool
	if err := st.pool.QueryRow(ctx, "SELECT missing FROM mcp_runtime_bindings WHERE node_id=$1", firstNode).Scan(&missing); err != nil || !missing {
		t.Fatalf("deleted local MCP was not marked missing: missing=%v err=%v", missing, err)
	}
	var discoveryID string
	if err := st.pool.QueryRow(ctx, "SELECT id::text FROM skill_discoveries WHERE node_id=$1 AND name='local-skill'", firstNode).Scan(&discoveryID); err != nil {
		t.Fatal(err)
	}
	target, err := st.SkillDiscoveryForAdoption(ctx, discoveryID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateNodeTask(ctx, firstNode, "", "adopt_skill", map[string]any{"discoveryId": discoveryID, "runtime": target.Runtime, "path": target.Path, "sha256": target.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	pendingTasks, err := st.PendingNodeTasks(ctx, firstNode)
	if err != nil {
		t.Fatal(err)
	}
	var pendingTask domain.AgentTask
	for _, candidate := range pendingTasks {
		if candidate.ID == task.ID {
			pendingTask = candidate
			break
		}
	}
	if pendingTask.ID == "" {
		t.Fatalf("new task %s was not pending", task.ID)
	}
	if pendingTask.Transport != "" || pendingTask.LeaseOwner != "" || pendingTask.LeaseExpiresAt != nil {
		t.Fatalf("new task unexpectedly had delivery metadata: %+v", pendingTask)
	}
	reserved, err := st.ReserveNodeTask(ctx, firstNode, task.ID, "agent_wss", "integration-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Transport != "agent_wss" || reserved.LeaseOwner != "integration-test" || reserved.LeaseExpiresAt == nil || reserved.Attempt != 1 {
		t.Fatalf("task reservation metadata is incomplete: %+v", reserved)
	}
	semanticKey := "scan_inventory:" + uuid.NewString()
	semanticTask, err := st.CreateNodeTaskWithOptions(ctx, firstNode, "", "scan_inventory", map[string]any{"readOnly": true}, NodeTaskOptions{SemanticKey: semanticKey})
	if err != nil {
		t.Fatal(err)
	}
	duplicateTask, err := st.CreateNodeTaskWithOptions(ctx, firstNode, "", "scan_inventory", map[string]any{"readOnly": true}, NodeTaskOptions{SemanticKey: semanticKey})
	if err != nil {
		t.Fatal(err)
	}
	if duplicateTask.ID != semanticTask.ID {
		t.Fatalf("semantic task dedupe created %s after %s", duplicateTask.ID, semanticTask.ID)
	}
	imported, err := st.ImportDiscoveredSkill(ctx, firstNode, discoveryID, task.ID, pkg)
	if err != nil {
		t.Fatal(err)
	}
	var sourceKind, reviewStatus string
	if err := st.pool.QueryRow(ctx, `SELECT ss.kind,s.review_status FROM skills s JOIN skill_sources ss ON ss.id=s.source_id WHERE s.id=$1`, imported.SkillID).Scan(&sourceKind, &reviewStatus); err != nil {
		t.Fatal(err)
	}
	if sourceKind != "node" || reviewStatus != "pending" {
		t.Fatalf("adopted Skill bypassed review: source=%s review=%s", sourceKind, reviewStatus)
	}
}

func TestAdoptSkillCompletionProjectsSharedOwnershipIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "toolhub_discovery_test") {
		t.Fatal("integration test database URL must target toolhub_discovery_test")
	}
	ctx := context.Background()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(ctx, databaseURL, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = st.BootstrapAdmin(ctx, "admin", "admin@example.test", "Admin", "ToolHub-Test-Password-2026")
	var adminID string
	if err := st.pool.QueryRow(ctx, "SELECT id::text FROM users WHERE username='admin'").Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	nodeID, _ := enrollTestNode(t, st, adminID, "adoption-node-"+suffix)
	pkg := testDiscoveredSkillPackage(t)
	runtimePath := "/tmp/adoption-" + suffix + "/codex/skills/runtime-skill"
	sharedRoot := "/tmp/adoption-" + suffix + "/shared/skills"
	sharedPath := sharedRoot + "/shared-skill"
	inventory := domain.AgentInventory{
		Runtimes: []domain.InventoryRuntime{{
			Kind:     domain.RuntimeCodex,
			RootPath: "/tmp/adoption-" + suffix + "/codex/skills",
			Config:   map[string]any{},
			Inventory: map[string]any{"skills": []any{map[string]any{
				"name": "runtime-skill", "path": runtimePath, "sha256": pkg.SHA256,
				"managed": false, "protected": false, "disabled": false,
			}}},
		}},
		SharedSources: []domain.SharedSourceInventory{{
			Name: "adoption-shared-" + suffix, Mode: "observed", SkillsRoot: sharedRoot,
			MCPManifestPath: "/tmp/adoption-" + suffix + "/shared/mcp/servers.json", Status: "observed",
			Skills: []domain.SharedSkillInventory{{
				Name: "shared-skill", SourcePath: sharedPath, ResolvedSourcePath: sharedPath,
				SHA256: pkg.SHA256, EntryType: "directory", State: "observed",
			}},
		}},
	}
	if _, err := st.ProcessAgentInventory(ctx, nodeID, inventory, false); err != nil {
		t.Fatal(err)
	}

	discoveryIDs := map[string]string{}
	rows, err := st.pool.Query(ctx, `SELECT runtime_kind,id::text FROM skill_discoveries
		WHERE node_id=$1 AND canonical_path IN ($2,$3)`, nodeID, runtimePath, sharedPath)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var runtimeKind, discoveryID string
		if err := rows.Scan(&runtimeKind, &discoveryID); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		discoveryIDs[runtimeKind] = discoveryID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if discoveryIDs[domain.RuntimeCodex] == "" || discoveryIDs[domain.RuntimeShared] == "" {
		t.Fatalf("missing adoption discoveries: %+v", discoveryIDs)
	}

	completeAdoption := func(discoveryID string, markerWritten bool) {
		t.Helper()
		target, err := st.SkillDiscoveryForAdoption(ctx, discoveryID)
		if err != nil {
			t.Fatal(err)
		}
		task, err := st.CreateNodeTask(ctx, nodeID, "", "adopt_skill", map[string]any{
			"discoveryId": discoveryID, "runtime": target.Runtime, "path": target.Path, "sha256": target.SHA256,
		})
		if err != nil {
			t.Fatal(err)
		}
		reserved, err := st.ReserveNodeTask(ctx, nodeID, task.ID, "agent_wss", "integration-test", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		imported, err := st.ImportDiscoveredSkill(ctx, nodeID, discoveryID, task.ID, pkg)
		if err != nil {
			t.Fatal(err)
		}
		result, err := json.Marshal(map[string]any{
			"discoveryId": discoveryID, "skillId": imported.SkillID, "versionId": imported.VersionID,
			"sha256": imported.SHA256, "markerWritten": markerWritten,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CompleteTaskAttempt(ctx, nodeID, task.ID, reserved.Attempt, "succeeded", result); err != nil {
			t.Fatal(err)
		}
	}

	completeAdoption(discoveryIDs[domain.RuntimeCodex], true)
	completeAdoption(discoveryIDs[domain.RuntimeShared], false)

	for _, expectation := range []struct {
		runtime string
		managed bool
		status  string
	}{
		{runtime: domain.RuntimeCodex, managed: true, status: "adopted"},
		{runtime: domain.RuntimeShared, managed: false, status: "imported"},
	} {
		var managed bool
		var status string
		if err := st.pool.QueryRow(ctx, `SELECT managed,adoption_status FROM skill_discoveries WHERE id=$1`, discoveryIDs[expectation.runtime]).Scan(&managed, &status); err != nil {
			t.Fatal(err)
		}
		if managed != expectation.managed || status != expectation.status {
			t.Fatalf("%s adoption projected managed=%v status=%q, want managed=%v status=%q", expectation.runtime, managed, status, expectation.managed, expectation.status)
		}
	}
}

func TestEquivalentSharedMCPImportIsSuppressedIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "toolhub_discovery_test") {
		t.Fatal("integration test database URL must target toolhub_discovery_test")
	}
	ctx := context.Background()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(ctx, databaseURL, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = st.BootstrapAdmin(ctx, "admin", "admin@example.test", "Admin", "ToolHub-Test-Password-2026")
	var adminID string
	if err := st.pool.QueryRow(ctx, "SELECT id::text FROM users WHERE username='admin'").Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	makeDescriptors := func(name string) (domain.MCPDescriptor, domain.MCPDescriptor) {
		t.Helper()
		input := domain.MCPDescriptor{Name: name, Transport: "stdio", Command: "npx", Args: []string{"-y", "equivalent-server"}}
		live, err := protocol.NormalizeMCPDescriptor(domain.RuntimeCodex, input)
		if err != nil {
			t.Fatal(err)
		}
		shared, err := protocol.NormalizeMCPDescriptor(domain.RuntimeClaude, input)
		if err != nil {
			t.Fatal(err)
		}
		shared.ImportSource = "shared-manifest"
		shared.ImportSourceName = "root-shared"
		shared.ImportRuntime = domain.RuntimeClaude
		shared.ImportEnabled = false
		return live, shared
	}

	suffix := strings.Split(uuid.NewString(), "-")[0]
	nodeID, _ := enrollTestNode(t, st, adminID, "dedupe-node-"+suffix)
	live, shared := makeDescriptors("equivalent-" + suffix)
	requests, err := st.ProcessAgentInventory(ctx, nodeID, domain.AgentInventory{Runtimes: testInventory(live), MCPImports: []domain.MCPDescriptor{shared}}, true)
	if err != nil || len(requests) != 0 {
		t.Fatalf("equivalent inventory requests=%+v err=%v", requests, err)
	}
	var runtimeCount, sharedCount int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE source='runtime-auto'),count(*) FILTER (WHERE source='shared-import')
		FROM mcp_servers WHERE runtime_name=$1`, live.Name).Scan(&runtimeCount, &sharedCount); err != nil {
		t.Fatal(err)
	}
	if runtimeCount != 1 || sharedCount != 0 {
		t.Fatalf("simultaneous equivalent imports runtime=%d shared=%d", runtimeCount, sharedCount)
	}

	secondNodeID, _ := enrollTestNode(t, st, adminID, "dedupe-late-node-"+suffix)
	lateLive, lateShared := makeDescriptors("equivalent-late-" + suffix)
	if requests, err = st.ProcessAgentInventory(ctx, secondNodeID, domain.AgentInventory{MCPImports: []domain.MCPDescriptor{lateShared}}, true); err != nil || len(requests) != 0 {
		t.Fatalf("shared-first inventory requests=%+v err=%v", requests, err)
	}
	var candidateID string
	if err := st.pool.QueryRow(ctx, `SELECT id::text FROM mcp_servers WHERE runtime_name=$1 AND source='shared-import' AND archived_at IS NULL`, lateShared.Name).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	if requests, err = st.ProcessAgentInventory(ctx, secondNodeID, domain.AgentInventory{Runtimes: testInventory(lateLive), MCPImports: []domain.MCPDescriptor{lateShared}}, true); err != nil || len(requests) != 0 {
		t.Fatalf("runtime takeover inventory requests=%+v err=%v", requests, err)
	}
	var archived bool
	if err := st.pool.QueryRow(ctx, "SELECT archived_at IS NOT NULL AND NOT enabled FROM mcp_servers WHERE id=$1", candidateID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if !archived {
		t.Fatal("preexisting equivalent shared candidate was not archived")
	}
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE source='runtime-auto' AND archived_at IS NULL),
		count(*) FILTER (WHERE source='shared-import' AND archived_at IS NULL) FROM mcp_servers WHERE runtime_name=$1`, lateLive.Name).
		Scan(&runtimeCount, &sharedCount); err != nil {
		t.Fatal(err)
	}
	if runtimeCount != 1 || sharedCount != 0 {
		t.Fatalf("late equivalent imports runtime=%d active-shared=%d", runtimeCount, sharedCount)
	}
}

func TestMCPMImportSeedsManagedProfilesAndRequiresExplicitDeploymentIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "toolhub_discovery_test") {
		t.Fatal("integration test database URL must target toolhub_discovery_test")
	}
	ctx := context.Background()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(ctx, databaseURL, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = st.BootstrapAdmin(ctx, "admin", "admin@example.test", "Admin", "ToolHub-Test-Password-2026")
	var adminID string
	if err := st.pool.QueryRow(ctx, "SELECT id::text FROM users WHERE username='admin'").Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	suffix := strings.Split(uuid.NewString(), "-")[0]
	nodeID, taskKey := enrollTestNode(t, st, adminID, "mcpm-node-"+suffix)
	serverName := "conflict-" + suffix
	secretValue := "node-secret-" + suffix
	preexisting, err := protocol.NormalizeMCPDescriptor(domain.RuntimeCodex, domain.MCPDescriptor{
		Name: serverName, Transport: "stdio", Command: "legacy-runtime-command", Args: []string{"--legacy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	requests, err := st.ProcessAgentInventory(ctx, nodeID, domain.AgentInventory{Runtimes: testInventory(preexisting)}, true)
	if err != nil || len(requests) != 0 {
		t.Fatalf("preexisting runtime-auto baseline requests=%+v err=%v", requests, err)
	}
	var preexistingID string
	if err := st.pool.QueryRow(ctx, "SELECT id::text FROM mcp_servers WHERE source='runtime-auto' AND runtime_name=$1", serverName).Scan(&preexistingID); err != nil {
		t.Fatal(err)
	}
	live, err := protocol.NormalizeMCPDescriptor(domain.RuntimeCodex, domain.MCPDescriptor{
		Name: serverName, Transport: "stdio", Command: "live-command", Args: []string{"--stdio"}, EnvKeys: []string{"TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	live.SecretFingerprint = security.FingerprintSecretMap(taskKey, map[string]string{"env:TOKEN": secretValue})
	live.ImportSource = "mcpm"
	live.ImportSourceName = "mcpm"
	live.ImportRuntime = domain.RuntimeCodex
	live.ImportEnabled = true
	live.TargetRuntimes = []string{domain.RuntimeCodex, domain.RuntimeClaude}
	live.ProfileTags = []string{"all-mcp"}
	legacy, err := protocol.NormalizeMCPDescriptor(domain.RuntimeClaude, domain.MCPDescriptor{
		Name: serverName, Transport: "streamable-http", URL: "https://example.test/legacy-mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy.ImportSource = "shared-manifest"
	legacy.ImportSourceName = "legacy-" + suffix
	legacy.ImportRuntime = domain.RuntimeClaude
	legacy.ImportEnabled = false

	inventory := domain.AgentInventory{MCPImports: []domain.MCPDescriptor{live, legacy}}
	requests, err = st.ProcessAgentInventory(ctx, nodeID, inventory, true)
	if err != nil || len(requests) != 1 {
		t.Fatalf("initial mcpm import requests=%+v err=%v", requests, err)
	}
	if _, err := st.CaptureRuntimeMCP(ctx, nodeID, MCPSecretCapture{
		Token: requests[0].Token, Runtime: domain.RuntimeCodex, Name: serverName, Identity: live.Identity, Env: map[string]string{"TOKEN": secretValue}, Headers: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}

	var liveID, liveDisplayName string
	if err := st.pool.QueryRow(ctx, "SELECT id::text,name FROM mcp_servers WHERE source='mcpm-import' AND runtime_name=$1", serverName).Scan(&liveID, &liveDisplayName); err != nil {
		t.Fatal(err)
	}
	if liveDisplayName != serverName {
		t.Fatalf("live mcpm name=%q want=%q", liveDisplayName, serverName)
	}
	var displacedName string
	if err := st.pool.QueryRow(ctx, "SELECT name FROM mcp_servers WHERE id=$1", preexistingID).Scan(&displacedName); err != nil {
		t.Fatal(err)
	}
	if displacedName != serverName+"-codex" {
		t.Fatalf("preexisting runtime-auto name=%q want=%q", displacedName, serverName+"-codex")
	}
	var legacyID, legacyDisplayName string
	var legacyEnabled bool
	if err := st.pool.QueryRow(ctx, "SELECT id::text,name,enabled FROM mcp_servers WHERE source='shared-import' AND runtime_name=$1", serverName).Scan(&legacyID, &legacyDisplayName, &legacyEnabled); err != nil {
		t.Fatal(err)
	}
	if legacyDisplayName != serverName+"-shared" || legacyEnabled {
		t.Fatalf("legacy candidate name=%q enabled=%v", legacyDisplayName, legacyEnabled)
	}

	profileIDs := map[string]string{}
	for _, runtimeKind := range []string{domain.RuntimeCodex, domain.RuntimeClaude} {
		var profileID, source, managedRuntime, deploymentState string
		var member bool
		if err := st.pool.QueryRow(ctx, `SELECT p.id::text,p.source,coalesce(p.origin->>'managedRuntime',''),d.state,
			EXISTS(SELECT 1 FROM mcp_profile_servers ps WHERE ps.profile_id=p.id AND ps.server_id=$3)
			FROM mcp_profiles p JOIN mcp_deployments d ON d.profile_id=p.id AND d.node_id=$2 AND d.runtime_kind=$1
			WHERE p.name='toolhub-'||$1`, runtimeKind, nodeID, liveID).Scan(&profileID, &source, &managedRuntime, &deploymentState, &member); err != nil {
			t.Fatal(err)
		}
		if source != "toolhub" || managedRuntime != runtimeKind || deploymentState != "observed" || !member {
			t.Fatalf("managed %s profile source=%q runtime=%q state=%q member=%v", runtimeKind, source, managedRuntime, deploymentState, member)
		}
		profileIDs[runtimeKind] = profileID
	}

	requests, err = st.ProcessAgentInventory(ctx, nodeID, inventory, true)
	if err != nil || len(requests) != 0 {
		t.Fatalf("repeat mcpm import requested capture: requests=%+v err=%v", requests, err)
	}
	var importCount int
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM mcp_servers WHERE runtime_name=$1 AND source IN ('mcpm-import','shared-import')", serverName).Scan(&importCount); err != nil || importCount != 2 {
		t.Fatalf("repeat inventory duplicated imports: count=%d err=%v", importCount, err)
	}

	var observedHash string
	if err := st.pool.QueryRow(ctx, "SELECT desired_hash FROM mcp_deployments WHERE profile_id=$1 AND node_id=$2 AND runtime_kind='codex'", profileIDs[domain.RuntimeCodex], nodeID).Scan(&observedHash); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPProfileServers(ctx, profileIDs[domain.RuntimeCodex], nil); err != nil {
		t.Fatal(err)
	}
	var stateAfterEdit, hashAfterEdit string
	if err := st.pool.QueryRow(ctx, "SELECT state,desired_hash FROM mcp_deployments WHERE profile_id=$1 AND node_id=$2 AND runtime_kind='codex'", profileIDs[domain.RuntimeCodex], nodeID).Scan(&stateAfterEdit, &hashAfterEdit); err != nil {
		t.Fatal(err)
	}
	if stateAfterEdit != "observed" || hashAfterEdit == observedHash {
		t.Fatalf("observed membership edit state=%q hashChanged=%v", stateAfterEdit, hashAfterEdit != observedHash)
	}
	if err := st.SetMCPProfileServers(ctx, profileIDs[domain.RuntimeCodex], []string{liveID, liveID}); err != nil {
		t.Fatal(err)
	}
	var membershipCount int
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM mcp_profile_servers WHERE profile_id=$1", profileIDs[domain.RuntimeCodex]).Scan(&membershipCount); err != nil || membershipCount != 1 {
		t.Fatalf("fixed profile membership count=%d err=%v", membershipCount, err)
	}
	if err := st.SetMCPProfileServers(ctx, profileIDs[domain.RuntimeCodex], []string{legacyID}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPProfileServers(ctx, profileIDs[domain.RuntimeCodex], []string{liveID}); err != nil {
		t.Fatal(err)
	}
	var envRefs map[string]string
	var envJSON []byte
	if err := st.pool.QueryRow(ctx, "SELECT env_refs FROM mcp_servers WHERE id=$1", liveID).Scan(&envJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envJSON, &envRefs); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AgentSecretValue(ctx, nodeID, envRefs["TOKEN"]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("observed profile authorized a secret: %v", err)
	}
	customProfileID, err := st.CreateMCPProfile(ctx, "custom-"+suffix, "", adminID, []string{liveID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetMCPDeployments(ctx, customProfileID, adminID, []MCPDeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}, false); !errors.Is(err, ErrManagedMCPProfile) {
		t.Fatalf("custom profile deployment error=%v", err)
	}
	if _, err := st.SetMCPDeployments(ctx, profileIDs[domain.RuntimeCodex], adminID, []MCPDeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeClaude, Enabled: true}}, false); !errors.Is(err, ErrMCPProfileRuntime) {
		t.Fatalf("mismatched fixed profile deployment error=%v", err)
	}

	job, err := st.SetMCPDeployments(ctx, profileIDs[domain.RuntimeCodex], adminID, []MCPDeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != "mcp_sync" {
		t.Fatalf("deployment job kind=%q", job.Kind)
	}
	var state string
	var generation int64
	if err := st.pool.QueryRow(ctx, "SELECT state,desired_generation FROM mcp_deployments WHERE profile_id=$1 AND node_id=$2 AND runtime_kind='codex'", profileIDs[domain.RuntimeCodex], nodeID).Scan(&state, &generation); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || generation < 2 {
		t.Fatalf("explicit deployment state=%q generation=%d", state, generation)
	}
	var deploymentID string
	if err := st.pool.QueryRow(ctx, "SELECT id::text FROM mcp_deployments WHERE profile_id=$1 AND node_id=$2 AND runtime_kind='codex'", profileIDs[domain.RuntimeCodex], nodeID).Scan(&deploymentID); err != nil {
		t.Fatal(err)
	}
	payloadNodeID, payload, err := st.MCPDeploymentPayload(ctx, deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if payloadNodeID != nodeID || payload.MCPMProfile != "toolhub-codex" || len(payload.Servers) != 1 || payload.Servers[0].Name != serverName || payload.Servers[0].EnvRefs["TOKEN"] == "" {
		t.Fatalf("managed deployment payload node=%q payload=%+v", payloadNodeID, payload)
	}
}

func enrollTestNode(t *testing.T, st *Store, adminID, name string) (string, []byte) {
	t.Helper()
	ctx := context.Background()
	token, _, err := st.CreateEnrollmentToken(ctx, name, nil, adminID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := st.EnrollAgent(ctx, token, name, "linux", "amd64", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(result.TaskKey)
	if err != nil {
		t.Fatal(err)
	}
	return result.NodeID, key
}

func testDescriptor(t *testing.T, key []byte, name, command, secret string) domain.MCPDescriptor {
	t.Helper()
	descriptor, err := protocol.NormalizeMCPDescriptor("codex", domain.MCPDescriptor{Name: name, Transport: "stdio", Command: command, Args: []string{"-y", "example"}, EnvKeys: []string{"TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor.SecretFingerprint = security.FingerprintSecretMap(key, map[string]string{"env:TOKEN": secret})
	return descriptor
}

func testInventory(descriptor domain.MCPDescriptor) []domain.InventoryRuntime {
	return []domain.InventoryRuntime{{Kind: "codex", RootPath: "/tmp/codex/skills", Config: map[string]any{}, Inventory: map[string]any{"skills": []any{}}, MCPServers: []domain.MCPDescriptor{descriptor}}}
}

func testDiscoveredSkillPackage(t *testing.T) skills.Package {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("---\nname: Local Skill\nlicense: MIT\n---\nBody\n"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	pkg, err := skills.ScanZIP(buffer.Bytes(), skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}
