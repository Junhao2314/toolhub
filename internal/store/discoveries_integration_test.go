package store

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/toolhub-dev/toolhub/internal/domain"
	"github.com/toolhub-dev/toolhub/internal/protocol"
	"github.com/toolhub-dev/toolhub/internal/security"
	"github.com/toolhub-dev/toolhub/internal/skills"
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
	if !kinds["update_check"] || !kinds["sync"] || !kinds["mcp_sync"] {
		t.Fatalf("default dual reconciliation schedules missing: %+v", schedules)
	}
	var adminID string
	if err := st.pool.QueryRow(ctx, "SELECT id::text FROM users WHERE username='admin'").Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	firstNode, firstKey := enrollTestNode(t, st, adminID, "node-one")
	firstDescriptor := testDescriptor(t, firstKey, "example", "npx", "shared-secret")
	requests, err := st.ProcessAgentInventory(ctx, firstNode, testInventory(firstDescriptor), true)
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
	requests, err = st.ProcessAgentInventory(ctx, secondNode, testInventory(secondDescriptor), true)
	if err != nil || len(requests) != 1 {
		t.Fatalf("second discovery: requests=%+v err=%v", requests, err)
	}
	if _, err := st.CaptureRuntimeMCP(ctx, secondNode, MCPSecretCapture{Token: requests[0].Token, Runtime: "codex", Name: "example", Identity: secondDescriptor.Identity, Secrets: map[string]string{"TOKEN": "shared-secret"}}); err != nil {
		t.Fatal(err)
	}
	var serverCount int
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM mcp_servers WHERE source='runtime-auto'").Scan(&serverCount); err != nil || serverCount != 1 {
		t.Fatalf("same configuration was not reused: count=%d err=%v", serverCount, err)
	}

	thirdNode, thirdKey := enrollTestNode(t, st, adminID, "node-three")
	thirdDescriptor := testDescriptor(t, thirdKey, "example", "npx", "different-secret")
	requests, err = st.ProcessAgentInventory(ctx, thirdNode, testInventory(thirdDescriptor), true)
	if err != nil || len(requests) != 1 {
		t.Fatalf("third discovery: requests=%+v err=%v", requests, err)
	}
	if _, err := st.CaptureRuntimeMCP(ctx, thirdNode, MCPSecretCapture{Token: requests[0].Token, Runtime: "codex", Name: "example", Identity: thirdDescriptor.Identity, Secrets: map[string]string{"TOKEN": "different-secret"}}); err != nil {
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
	if requests, err = st.ProcessAgentInventory(ctx, firstNode, testInventory(drifted), true); err != nil || len(requests) != 0 {
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
	if _, err := st.ProcessAgentInventory(ctx, firstNode, skillInventory, false); err != nil {
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
	descriptor.SecretFingerprint = security.FingerprintSecretMap(key, map[string]string{"TOKEN": secret})
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
