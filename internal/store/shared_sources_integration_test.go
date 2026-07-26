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
	cipher, err := security.NewCipher(bytes.Repeat([]byte{6}, 32))
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
	task, err := st.CreateNodeTask(ctx, nodeID, "", "sync_shared", protocol.SyncSharedPayload{SourceID: sourceID, SourceName: source.Name, Scopes: []string{"skills", "mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	source.SourceFingerprint = strings.Repeat("d", 64)
	resultJSON, _ := json.Marshal(domain.SharedSyncResult{Source: source, Changed: true})
	if err := st.CompleteTask(ctx, nodeID, task.ID, "succeeded", resultJSON); err != nil {
		t.Fatal(err)
	}
	var projectedFingerprint string
	var synced bool
	if err := st.pool.QueryRow(ctx, "SELECT source_fingerprint,last_sync_at IS NOT NULL FROM shared_sources WHERE id=$1", sourceID).Scan(&projectedFingerprint, &synced); err != nil || projectedFingerprint != source.SourceFingerprint || !synced {
		t.Fatalf("shared task result was not projected: fingerprint=%q synced=%v err=%v", projectedFingerprint, synced, err)
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
		Name: "central-" + suffix, Transport: "stdio", Command: "central-command", Env: map[string]string{"AUTH": "node-local-secret-env"}, Headers: map[string]string{"authorization": "Bearer node-local-secret-header"}, Enabled: true,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	profileID, err := st.CreateMCPProfile(ctx, "central-profile-"+suffix, "", adminID, []string{centralID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetMCPDeployments(ctx, profileID, []MCPDeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}); err != nil {
		t.Fatal(err)
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
