package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/security"
)

func TestSharedSourceProjectionAndHeaderAuthorizationIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "toolhub_discovery_test") {
		t.Fatal("integration test database URL must target toolhub_discovery_test")
	}
	ctx := context.Background()
	// Fixed managed MCP profiles are global across integration cases in the
	// shared test database, so use the same deterministic key as the mcpm import
	// integration test that may already own toolhub-codex/toolhub-claude.
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
	if err := st.pool.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN user_roles ur ON ur.user_id=u.id JOIN roles r ON r.id=ur.role_id WHERE r.name='admin' ORDER BY u.created_at LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	nodeID, _ := enrollTestNode(t, st, adminID, "shared-node-"+suffix)
	descriptor, err := protocol.NormalizeMCPDescriptor(domain.RuntimeClaude, domain.MCPDescriptor{
		Name: "shared-server-" + suffix, Transport: "streamable-http", URL: "https://example.test/mcp", EnvKeys: []string{"TOKEN"}, HeaderKeys: []string{"authorization"},
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor.SecretFingerprint = strings.Repeat("a", 64)
	source := domain.SharedSourceInventory{
		Name: "shared-" + suffix, Mode: "managed", SkillsRoot: "/tmp/shared-" + suffix + "/skills", MCPManifestPath: "/tmp/shared-" + suffix + "/servers.json",
		ConfigFingerprint: strings.Repeat("b", 64), SourceFingerprint: strings.Repeat("c", 64), Status: "in_sync",
		Skills:     []domain.SharedSkillInventory{},
		MCPServers: []domain.SharedMCPServerInventory{{Descriptor: descriptor, Enabled: true}},
		Consumers: []domain.SharedConsumerInventory{{
			Kind: domain.RuntimeClaude, MCPPath: "/tmp/shared-" + suffix + "/claude.json", MCPFormat: "claude-settings-json", MCPEnabled: true, State: "in_sync",
			SkillLinks: []domain.SharedSkillLinkInventory{}, MCPBindings: []domain.SharedMCPBindingInventory{{ServerName: descriptor.Name, DesiredFingerprint: "desired", ActualFingerprint: "desired", EnvKeys: descriptor.EnvKeys, HeaderKeys: descriptor.HeaderKeys, Enabled: true, State: "in_sync"}},
		}},
	}
	if _, err := st.ProcessAgentInventory(ctx, nodeID, domain.AgentInventory{SharedSources: []domain.SharedSourceInventory{source}}, false); err != nil {
		t.Fatal(err)
	}
	projection, err := st.ListSharedSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projection), source.Name) || !strings.Contains(string(projection), "Authorization") {
		t.Fatalf("shared projection is incomplete: %s", projection)
	}
	if strings.Contains(string(projection), "node-local-secret") || strings.Contains(string(projection), descriptor.SecretFingerprint) {
		t.Fatalf("shared projection leaked secret material: %s", projection)
	}
	var sourceID, sharedServerID string
	if err := st.pool.QueryRow(ctx, `SELECT ss.id::text,ms.id::text FROM shared_sources ss JOIN mcp_servers ms ON ms.shared_source_id=ss.id WHERE ss.node_id=$1 AND ss.name=$2`, nodeID, source.Name).Scan(&sourceID, &sharedServerID); err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	if err := st.UpdateMCPServer(ctx, sharedServerID, MCPServerPatch{Name: name}); !errors.Is(err, ErrSourceFileAuthoritative) {
		t.Fatalf("shared-file update error = %v", err)
	}
	if err := st.DeleteMCPServer(ctx, sharedServerID); !errors.Is(err, ErrSourceFileAuthoritative) {
		t.Fatalf("shared-file delete error = %v", err)
	}
	if _, err := st.CreateMCPProfile(ctx, "invalid-profile-"+suffix, "", adminID, []string{sharedServerID}); !errors.Is(err, ErrSourceFileAuthoritative) {
		t.Fatalf("shared-file profile error = %v", err)
	}

	centralID, err := st.CreateMCPServer(ctx, MCPServerInput{
		Name: descriptor.Name, Transport: "stdio", Command: "central-command", Env: map[string]string{"AUTH": "node-local-secret-env"}, Headers: map[string]string{"authorization": "Bearer node-local-secret-header"}, Enabled: true,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	profileID, err := st.CreateMCPProfile(ctx, "central-profile-"+suffix, "", adminID, []string{centralID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetMCPDeployments(ctx, profileID, adminID, []MCPDeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}, false); !errors.Is(err, ErrManagedMCPProfile) {
		t.Fatalf("custom MCP profile deployment error = %v", err)
	}
	var envRefs, headerRefs map[string]string
	var envJSON, headerJSON []byte
	if err := st.pool.QueryRow(ctx, "SELECT env_refs,header_refs FROM mcp_servers WHERE id=$1", centralID).Scan(&envJSON, &headerJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envJSON, &envRefs); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(headerJSON, &headerRefs); err != nil {
		t.Fatal(err)
	}
	legacyDeploymentID := uuid.NewString()
	if _, err := st.pool.Exec(ctx, `INSERT INTO mcp_deployments(id,profile_id,node_id,runtime_kind,desired_enabled,desired_hash,state)
		VALUES($1,$2,$3,'codex',true,'legacy-arbitrary-profile','pending')`, legacyDeploymentID, profileID, nodeID); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingMCPDeployments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range pending {
		if item.DeploymentID == legacyDeploymentID {
			t.Fatal("legacy arbitrary MCP deployment remained dispatchable")
		}
	}
	if _, err := st.AgentSecretValue(ctx, nodeID, envRefs["AUTH"]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy arbitrary MCP deployment authorized a secret: %v", err)
	}
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	managedProfileID, err := ensureManagedMCPProfileTx(ctx, tx, domain.RuntimeCodex, nodeID)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPProfileServers(ctx, managedProfileID, []string{sharedServerID}); !errors.Is(err, ErrSourceFileAuthoritative) {
		t.Fatalf("shared-file managed membership error = %v", err)
	}
	if err := st.SetMCPProfileServers(ctx, managedProfileID, []string{centralID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetMCPDeployments(ctx, managedProfileID, adminID, []MCPDeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeClaude, Enabled: true}}, false); !errors.Is(err, ErrMCPProfileRuntime) {
		t.Fatalf("mismatched managed MCP profile error = %v", err)
	}
	managedJob, err := st.SetMCPDeployments(ctx, managedProfileID, adminID, []MCPDeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}, false)
	if err != nil {
		t.Fatal(err)
	}
	var managedDeploymentID, bindingServerID, bindingProfileID, bindingDeploymentID string
	var bindingSharedSourceID *string
	if err := st.pool.QueryRow(ctx, `SELECT d.id::text,b.server_id::text,b.profile_id::text,b.deployment_id::text,b.shared_source_id::text
		FROM mcp_deployments d JOIN mcp_runtime_bindings b ON b.deployment_id=d.id
		WHERE d.profile_id=$1 AND d.node_id=$2 AND d.runtime_kind='codex' AND b.server_name=$3`, managedProfileID, nodeID, descriptor.Name).
		Scan(&managedDeploymentID, &bindingServerID, &bindingProfileID, &bindingDeploymentID, &bindingSharedSourceID); err != nil {
		t.Fatal(err)
	}
	if managedJob.Kind != "mcp_sync" || bindingServerID != centralID || bindingProfileID != managedProfileID || bindingDeploymentID != managedDeploymentID || bindingSharedSourceID != nil {
		t.Fatalf("central takeover did not clear shared ownership: job=%q server=%q profile=%q deployment=%q shared=%v", managedJob.Kind, bindingServerID, bindingProfileID, bindingDeploymentID, bindingSharedSourceID)
	}
	if _, err := st.ProcessAgentInventory(ctx, nodeID, domain.AgentInventory{SharedSources: []domain.SharedSourceInventory{source}}, false); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT server_id::text,profile_id::text,deployment_id::text,shared_source_id::text
		FROM mcp_runtime_bindings WHERE node_id=$1 AND runtime_kind='codex' AND server_name=$2`, nodeID, descriptor.Name).
		Scan(&bindingServerID, &bindingProfileID, &bindingDeploymentID, &bindingSharedSourceID); err != nil {
		t.Fatal(err)
	}
	if bindingServerID != centralID || bindingProfileID != managedProfileID || bindingDeploymentID != managedDeploymentID || bindingSharedSourceID != nil {
		t.Fatalf("shared rescan reclaimed central binding: server=%q profile=%q deployment=%q shared=%v", bindingServerID, bindingProfileID, bindingDeploymentID, bindingSharedSourceID)
	}
	otherNodeID, _ := enrollTestNode(t, st, adminID, "other-node-"+uuid.NewString())
	for _, secretID := range []string{envRefs["AUTH"], headerRefs["Authorization"]} {
		if value, err := st.AgentSecretValue(ctx, nodeID, secretID); err != nil || len(value) == 0 {
			t.Fatalf("authorized Agent secret read failed: value=%q err=%v", value, err)
		}
		if _, err := st.AgentSecretValue(ctx, otherNodeID, secretID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-node Agent secret read error = %v", err)
		}
	}
	enabled := false
	if err := st.UpdateMCPServer(ctx, centralID, MCPServerPatch{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AgentSecretValue(ctx, nodeID, headerRefs["Authorization"]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled MCP header remained readable: %v", err)
	}
}

func TestMCPMImportTakesOwnershipFromSharedBindingIntegration(t *testing.T) {
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
	if err := st.pool.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN user_roles ur ON ur.user_id=u.id JOIN roles r ON r.id=ur.role_id WHERE r.name='admin' ORDER BY u.created_at LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	nodeID, _ := enrollTestNode(t, st, adminID, "shared-takeover-node-"+suffix)
	name := "shared-takeover-" + suffix
	sharedDescriptor, err := protocol.NormalizeMCPDescriptor(domain.RuntimeClaude, domain.MCPDescriptor{
		Name: name, Transport: "streamable-http", URL: "https://example.test/legacy-mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := domain.SharedSourceInventory{
		Name: "takeover-" + suffix, Mode: "managed", SkillsRoot: "/tmp/takeover-" + suffix + "/skills", MCPManifestPath: "/tmp/takeover-" + suffix + "/servers.json",
		ConfigFingerprint: strings.Repeat("a", 64), SourceFingerprint: strings.Repeat("b", 64), Status: "in_sync",
		MCPServers: []domain.SharedMCPServerInventory{{Descriptor: sharedDescriptor, Enabled: true}},
		Consumers: []domain.SharedConsumerInventory{
			{Kind: domain.RuntimeCodex, MCPEnabled: true, State: "in_sync", MCPBindings: []domain.SharedMCPBindingInventory{{ServerName: name, Enabled: true, State: "in_sync"}}},
			{Kind: domain.RuntimeClaude, MCPEnabled: true, State: "in_sync", MCPBindings: []domain.SharedMCPBindingInventory{{ServerName: name, Enabled: true, State: "in_sync"}}},
		},
	}
	if _, err := st.ProcessAgentInventory(ctx, nodeID, domain.AgentInventory{SharedSources: []domain.SharedSourceInventory{source}}, false); err != nil {
		t.Fatal(err)
	}

	live, err := protocol.NormalizeMCPDescriptor(domain.RuntimeCodex, domain.MCPDescriptor{
		Name: name, Transport: "stdio", Command: "live-command", Args: []string{"--stdio"},
	})
	if err != nil {
		t.Fatal(err)
	}
	live.ImportSource = "mcpm"
	live.ImportSourceName = "mcpm"
	live.ImportRuntime = domain.RuntimeCodex
	live.ImportEnabled = true
	live.TargetRuntimes = []string{domain.RuntimeCodex, domain.RuntimeClaude}
	live.ProfileTags = []string{"toolhub-codex", "toolhub-claude"}
	inventory := domain.AgentInventory{SharedSources: []domain.SharedSourceInventory{source}, MCPImports: []domain.MCPDescriptor{live}}
	if requests, err := st.ProcessAgentInventory(ctx, nodeID, inventory, true); err != nil || len(requests) != 0 {
		t.Fatalf("mcpm takeover inventory requests=%+v err=%v", requests, err)
	}
	var liveID string
	if err := st.pool.QueryRow(ctx, "SELECT id::text FROM mcp_servers WHERE source='mcpm-import' AND runtime_name=$1", name).Scan(&liveID); err != nil {
		t.Fatal(err)
	}
	assertOwnedByMCPM := func() {
		t.Helper()
		rows, err := st.pool.Query(ctx, `SELECT runtime_kind,server_id::text,shared_source_id::text FROM mcp_runtime_bindings
			WHERE node_id=$1 AND server_name=$2 ORDER BY runtime_kind`, nodeID, name)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var runtimeKind, serverID string
			var sharedSourceID *string
			if err := rows.Scan(&runtimeKind, &serverID, &sharedSourceID); err != nil {
				t.Fatal(err)
			}
			if serverID != liveID || sharedSourceID != nil {
				t.Fatalf("%s binding ownership server=%q shared=%v", runtimeKind, serverID, sharedSourceID)
			}
			count++
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("mcpm-owned binding count=%d, want 2", count)
		}
	}
	assertOwnedByMCPM()
	if requests, err := st.ProcessAgentInventory(ctx, nodeID, inventory, true); err != nil || len(requests) != 0 {
		t.Fatalf("repeat takeover inventory requests=%+v err=%v", requests, err)
	}
	assertOwnedByMCPM()
}

// A binding reported without environment or header keys must be stored as an empty jsonb
// array. A nil Go slice previously marshalled to `null`, and the projection's
// jsonb_array_elements_text expansion then failed with "cannot extract elements from a
// scalar", turning every shared-source read into a 500.
func TestSharedSourceProjectionToleratesBindingsWithoutKeysIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "toolhub_discovery_test") {
		t.Fatal("integration test database URL must target toolhub_discovery_test")
	}
	ctx := context.Background()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{7}, 32))
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
	if err := st.pool.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN user_roles ur ON ur.user_id=u.id JOIN roles r ON r.id=ur.role_id WHERE r.name='admin' ORDER BY u.created_at LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	nodeID, _ := enrollTestNode(t, st, adminID, "bare-shared-node-"+suffix)
	descriptor, err := protocol.NormalizeMCPDescriptor(domain.RuntimeClaude, domain.MCPDescriptor{
		Name: "bare-server-" + suffix, Transport: "stdio", Command: "bare-command",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := domain.SharedSourceInventory{
		Name: "bare-" + suffix, Mode: "managed", SkillsRoot: "/tmp/bare-" + suffix + "/skills", MCPManifestPath: "/tmp/bare-" + suffix + "/servers.json",
		ConfigFingerprint: strings.Repeat("e", 64), SourceFingerprint: strings.Repeat("f", 64), Status: "in_sync",
		Skills:     []domain.SharedSkillInventory{},
		MCPServers: []domain.SharedMCPServerInventory{{Descriptor: descriptor, Enabled: true}},
		Consumers: []domain.SharedConsumerInventory{{
			Kind: domain.RuntimeClaude, MCPPath: "/tmp/bare-" + suffix + "/claude.json", MCPFormat: "claude-settings-json", MCPEnabled: true, State: "in_sync",
			SkillLinks: []domain.SharedSkillLinkInventory{},
			// EnvKeys and HeaderKeys are deliberately left nil.
			MCPBindings: []domain.SharedMCPBindingInventory{{ServerName: descriptor.Name, DesiredFingerprint: "desired", ActualFingerprint: "desired", Enabled: true, State: "in_sync"}},
		}},
	}
	if _, err := st.ProcessAgentInventory(ctx, nodeID, domain.AgentInventory{SharedSources: []domain.SharedSourceInventory{source}}, false); err != nil {
		t.Fatal(err)
	}
	var envType, headerType string
	if err := st.pool.QueryRow(ctx, `SELECT jsonb_typeof(env_keys),jsonb_typeof(header_keys) FROM mcp_runtime_bindings WHERE node_id=$1 AND server_name=$2`, nodeID, descriptor.Name).Scan(&envType, &headerType); err != nil {
		t.Fatal(err)
	}
	if envType != "array" || headerType != "array" {
		t.Fatalf("binding key types = env:%q header:%q, want array", envType, headerType)
	}
	projection, err := st.ListSharedSources(ctx)
	if err != nil {
		t.Fatalf("shared projection failed for a binding without keys: %v", err)
	}
	if !strings.Contains(string(projection), source.Name) {
		t.Fatalf("shared projection omitted the source: %s", projection)
	}
}
