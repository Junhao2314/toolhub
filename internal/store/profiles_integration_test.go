package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/security"
)

func TestToolHubProfilesIntegration(t *testing.T) {
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
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = st.BootstrapAdmin(ctx, "admin", "admin@example.test", "Admin", "ToolHub-Test-Password-2026")
	var adminID string
	if err := st.pool.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN user_roles ur ON ur.user_id=u.id
		JOIN roles r ON r.id=ur.role_id WHERE r.name='admin' ORDER BY u.created_at LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	suffix := strings.SplitN(uuid.NewString(), "-", 2)[0]
	nodeA, _ := enrollTestNode(t, st, adminID, "profile-node-a-"+suffix)
	nodeB, _ := enrollTestNode(t, st, adminID, "profile-node-b-"+suffix)
	if err := st.ReplaceInventory(ctx, nodeA, profileTestInventory(suffix, true)); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceInventory(ctx, nodeB, profileTestInventory(suffix, false)); err != nil {
		t.Fatal(err)
	}

	alphaID, err := st.CreateMCPServer(ctx, MCPServerInput{Name: "profile-alpha-" + suffix, Transport: "stdio", Command: "alpha", Enabled: true}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	betaID, err := st.CreateMCPServer(ctx, MCPServerInput{Name: "profile-beta-" + suffix, Transport: "stdio", Command: "beta", Enabled: true}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	secretValue := "profile-secret-" + suffix
	secretID, err := st.CreateMCPServer(ctx, MCPServerInput{
		Name: "profile-secret-" + suffix, Transport: "streamable-http", URL: "https://example.test/mcp", Enabled: true,
		Env: map[string]string{"TAVILY_API_KEY": secretValue}, Headers: map[string]string{"Authorization": "Bearer " + secretValue},
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}

	fixedProfileID := ensureProfileTestManagedMCPProfile(t, st, nodeA, domain.RuntimeCodex)
	legacyMembers := profileTestMembership(t, st, fixedProfileID)
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), "DELETE FROM toolhub_profile_activations WHERE node_id=ANY($1::uuid[])", []string{nodeA, nodeB})
		_, _ = st.pool.Exec(context.Background(), "DELETE FROM mcp_profile_servers WHERE profile_id=$1", fixedProfileID)
		for _, serverID := range legacyMembers {
			_, _ = st.pool.Exec(context.Background(), `INSERT INTO mcp_profile_servers(profile_id,server_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, fixedProfileID, serverID)
		}
	})
	if err := st.SetMCPProfileServers(ctx, fixedProfileID, []string{alphaID, betaID}); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{nodeA, nodeB} {
		if _, err := st.SetMCPDeployments(ctx, fixedProfileID, adminID, []MCPDeploymentTarget{{NodeID: nodeID, Runtime: domain.RuntimeCodex, Enabled: true}}, false); err != nil {
			t.Fatal(err)
		}
	}

	deploymentA := profileTestMCPDeploymentID(t, st, fixedProfileID, nodeA)
	deploymentB := profileTestMCPDeploymentID(t, st, fixedProfileID, nodeB)
	_, currentLegacyPayload, err := st.MCPDeploymentPayload(ctx, deploymentA)
	if err != nil {
		t.Fatal(err)
	}
	legacyPayload := profileTestLegacyMCPPayload(t, st, deploymentA)
	if !reflect.DeepEqual(currentLegacyPayload, legacyPayload) {
		t.Fatalf("no-activation payload changed\ncurrent: %#v\nlegacy:  %#v", currentLegacyPayload, legacyPayload)
	}
	var desiredHash string
	if err := st.pool.QueryRow(ctx, "SELECT desired_hash FROM mcp_deployments WHERE id=$1", deploymentA).Scan(&desiredHash); err != nil {
		t.Fatal(err)
	}
	if legacyHash := profileTestLegacyProfileHash(t, st, fixedProfileID); desiredHash != legacyHash {
		t.Fatalf("no-activation desired hash=%q want legacy hash=%q", desiredHash, legacyHash)
	}

	skillA := profileTestApprovedSkill(t, st, adminID, "profile-skill-a-"+suffix)
	skillB := profileTestApprovedSkill(t, st, adminID, "profile-skill-b-"+suffix)
	profileA, err := st.CreateToolHubProfile(ctx, "Research "+suffix, "Remote research selection", adminID)
	if err != nil {
		t.Fatal(err)
	}
	profileB, err := st.CreateToolHubProfile(ctx, "Development "+suffix, "Development selection", adminID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetToolHubProfileMembers(ctx, profileA, []string{alphaID, secretID}, []string{skillA}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetToolHubProfileMembers(ctx, profileB, []string{betaID}, []string{skillB}); err != nil {
		t.Fatal(err)
	}
	profiles, err := st.ListToolHubProfiles(ctx)
	if err != nil || !bytes.Contains(profiles, []byte(profileA)) || !bytes.Contains(profiles, []byte(profileB)) {
		t.Fatalf("profile list missing created profiles: %s err=%v", profiles, err)
	}
	profileRaw, err := st.GetToolHubProfile(ctx, profileA)
	if err != nil {
		t.Fatal(err)
	}
	var profileView struct {
		MCPServerIDs []string `json:"mcpServerIds"`
		SkillIDs     []string `json:"skillIds"`
	}
	if err := json.Unmarshal(profileRaw, &profileView); err != nil {
		t.Fatal(err)
	}
	if !sameProfileTestStringSet(profileView.MCPServerIDs, []string{alphaID, secretID}) || !sameProfileTestStringSet(profileView.SkillIDs, []string{skillA}) {
		t.Fatalf("profile members=%+v", profileView)
	}

	preflight, err := st.PreflightProfileActivation(ctx, profileA, nodeA, domain.RuntimeCodex)
	if err != nil {
		t.Fatal(err)
	}
	wantSecretKeys := []string{"Authorization", "TAVILY_API_KEY"}
	if preflight.OK || !sameProfileTestStringSet(preflight.RemoteSecretKeys, wantSecretKeys) || !hasActivationIssue(preflight.Errors, "remote_secret_confirmation_required") {
		t.Fatalf("remote preflight=%+v", preflight)
	}
	if _, err := st.ActivateProfile(ctx, profileA, nodeA, domain.RuntimeCodex, adminID, false); !isActivationIssue(err, "remote_secret_confirmation_required") {
		t.Fatalf("activation without secret confirmation error=%v", err)
	}
	jobA, err := st.ActivateProfile(ctx, profileA, nodeA, domain.RuntimeCodex, adminID, true)
	if err != nil {
		t.Fatal(err)
	}
	if jobA.Kind != "profile_activate" || jobA.MaxAttempts != 1 || !bytes.Contains(jobA.Payload, []byte("TAVILY_API_KEY")) || bytes.Contains(jobA.Payload, []byte(secretValue)) {
		t.Fatalf("activation job=%+v", jobA)
	}

	refs := profileTestServerSecretRefs(t, st, secretID)
	for key, secretRef := range refs {
		value, err := st.AgentSecretValue(ctx, nodeA, secretRef)
		if err != nil || !strings.Contains(string(value), secretValue) {
			t.Fatalf("authorized %s secret value=%q err=%v", key, value, err)
		}
		if _, err := st.AgentSecretValue(ctx, nodeB, secretRef); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unactivated node received %s secret: %v", key, err)
		}
	}
	if _, err := st.SetSkillTargets(ctx, skillA, adminID, []DeploymentTarget{{NodeID: nodeA, Runtime: domain.RuntimeCodex, Enabled: true}}, false); !errors.Is(err, ErrTargetManagedByProfile) {
		t.Fatalf("managed Skill target update error=%v", err)
	}
	if _, err := st.SetMCPDeployments(ctx, fixedProfileID, adminID, []MCPDeploymentTarget{{NodeID: nodeA, Runtime: domain.RuntimeCodex, Enabled: true}}, false); !errors.Is(err, ErrTargetManagedByProfile) {
		t.Fatalf("managed MCP target update error=%v", err)
	}
	if err := st.SetMCPProfileServers(ctx, fixedProfileID, []string{alphaID}); !errors.Is(err, ErrTargetManagedByProfile) {
		t.Fatalf("managed fixed profile membership error=%v", err)
	}
	if err := st.SetToolHubProfileMembers(ctx, profileA, []string{alphaID}, []string{skillA}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("active ToolHub Profile membership error=%v", err)
	}
	if err := st.SetMCPServerArchived(ctx, secretID, true); !errors.Is(err, ErrTargetManagedByProfile) {
		t.Fatalf("active profile archive error=%v", err)
	}
	disabled := false
	if err := st.UpdateMCPServer(ctx, secretID, MCPServerPatch{Enabled: &disabled}); !errors.Is(err, ErrTargetManagedByProfile) {
		t.Fatalf("active profile disable error=%v", err)
	}
	if err := st.DeleteMCPServer(ctx, secretID); !errors.Is(err, ErrTargetManagedByProfile) {
		t.Fatalf("active profile delete error=%v", err)
	}

	jobB, err := st.ActivateProfile(ctx, profileB, nodeB, domain.RuntimeCodex, adminID, false)
	if err != nil {
		t.Fatal(err)
	}
	if jobB.Kind != "profile_activate" {
		t.Fatalf("second activation job kind=%q", jobB.Kind)
	}
	_, payloadA, err := st.MCPDeploymentPayload(ctx, deploymentA)
	if err != nil {
		t.Fatal(err)
	}
	_, payloadB, err := st.MCPDeploymentPayload(ctx, deploymentB)
	if err != nil {
		t.Fatal(err)
	}
	if !sameProfileTestStringSet(mcpPayloadServerIDs(payloadA), []string{alphaID, secretID}) || !sameProfileTestStringSet(mcpPayloadServerIDs(payloadB), []string{betaID}) || payloadA.DesiredHash == payloadB.DesiredHash {
		t.Fatalf("per-target payloads A=%+v B=%+v", payloadA, payloadB)
	}

	targetRaw, err := st.TargetView(ctx, nodeA, domain.RuntimeCodex)
	if err != nil {
		t.Fatal(err)
	}
	var targetView struct {
		Activation *struct {
			ProfileID string `json:"profileId"`
			State     string `json:"state"`
		} `json:"activation"`
		MCP struct {
			Servers []struct {
				ID string `json:"id"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(targetRaw, &targetView); err != nil {
		t.Fatal(err)
	}
	if targetView.Activation == nil || targetView.Activation.ProfileID != profileA || !sameProfileTestStringSet(targetServerIDs(targetView.MCP.Servers), []string{alphaID, secretID}) {
		t.Fatalf("target view=%s", targetRaw)
	}
	grokRaw, err := st.TargetView(ctx, nodeA, domain.RuntimeGrok)
	if err != nil || !bytes.Contains(grokRaw, []byte("MCP follows claude on this node")) || !bytes.Contains(grokRaw, []byte(`"mcp":false`)) {
		t.Fatalf("grok target view=%s err=%v", grokRaw, err)
	}

	var skillDesiredEnabled bool
	var mcpDesiredHash string
	if err := st.pool.QueryRow(ctx, `SELECT desired_enabled FROM deployments WHERE node_id=$1 AND runtime_kind='codex' AND skill_id=$2`, nodeA, skillA).Scan(&skillDesiredEnabled); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, "SELECT desired_hash FROM mcp_deployments WHERE id=$1", deploymentA).Scan(&mcpDesiredHash); err != nil {
		t.Fatal(err)
	}
	if err := st.DeactivateProfile(ctx, nodeA, domain.RuntimeCodex, adminID); err != nil {
		t.Fatal(err)
	}
	var skillAfter bool
	var hashAfter string
	if err := st.pool.QueryRow(ctx, `SELECT desired_enabled FROM deployments WHERE node_id=$1 AND runtime_kind='codex' AND skill_id=$2`, nodeA, skillA).Scan(&skillAfter); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, "SELECT desired_hash FROM mcp_deployments WHERE id=$1", deploymentA).Scan(&hashAfter); err != nil {
		t.Fatal(err)
	}
	if skillAfter != skillDesiredEnabled || hashAfter != mcpDesiredHash {
		t.Fatalf("deactivation changed desired state: skill %v->%v hash %q->%q", skillDesiredEnabled, skillAfter, mcpDesiredHash, hashAfter)
	}
	for key, secretRef := range refs {
		if _, err := st.AgentSecretValue(ctx, nodeA, secretRef); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deactivated target retained %s secret authorization: %v", key, err)
		}
	}
	if _, err := st.SetSkillTargets(ctx, skillA, adminID, []DeploymentTarget{{NodeID: nodeA, Runtime: domain.RuntimeCodex, Enabled: true}}, false); err != nil {
		t.Fatalf("manual Skill target remained blocked after deactivation: %v", err)
	}
	if err := st.SetMCPServerArchived(ctx, secretID, true); err != nil {
		t.Fatal(err)
	}
	preflight, err = st.PreflightProfileActivation(ctx, profileA, nodeA, domain.RuntimeCodex)
	if err != nil || !hasActivationIssue(preflight.Errors, "mcp_server_unavailable") {
		t.Fatalf("archived member preflight=%+v err=%v", preflight, err)
	}
	if err := st.SetMCPServerArchived(ctx, secretID, false); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateToolHubProfile(ctx, profileA, "Research updated "+suffix, "Updated description"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteToolHubProfile(ctx, profileB); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("active profile delete error=%v", err)
	}
	if err := st.DeactivateProfile(ctx, nodeB, domain.RuntimeCodex, adminID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteToolHubProfile(ctx, profileB); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteToolHubProfile(ctx, profileA); err != nil {
		t.Fatal(err)
	}
}

func profileTestInventory(suffix string, includeGrok bool) domain.AgentInventory {
	runtimes := []domain.InventoryRuntime{{Kind: domain.RuntimeCodex, RootPath: "/tmp/profile-" + suffix + "/codex", Config: map[string]any{}, Inventory: map[string]any{"skills": []any{}}}}
	if includeGrok {
		runtimes = append(runtimes, domain.InventoryRuntime{Kind: domain.RuntimeGrok, RootPath: "/tmp/profile-" + suffix + "/grok", Config: map[string]any{}, Inventory: map[string]any{"skills": []any{}}})
	}
	return domain.AgentInventory{Runtimes: runtimes}
}

func ensureProfileTestManagedMCPProfile(t *testing.T, st *Store, nodeID, runtimeKind string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	profileID, err := ensureManagedMCPProfileTx(ctx, tx, runtimeKind, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return profileID
}

func profileTestMembership(t *testing.T, st *Store, profileID string) []string {
	t.Helper()
	rows, err := st.pool.Query(context.Background(), "SELECT server_id::text FROM mcp_profile_servers WHERE profile_id=$1 ORDER BY server_id::text", profileID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		result = append(result, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func profileTestMCPDeploymentID(t *testing.T, st *Store, profileID, nodeID string) string {
	t.Helper()
	var id string
	if err := st.pool.QueryRow(context.Background(), `SELECT id::text FROM mcp_deployments
		WHERE profile_id=$1 AND node_id=$2 AND runtime_kind='codex'`, profileID, nodeID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func profileTestLegacyMCPPayload(t *testing.T, st *Store, deploymentID string) protocol.ApplyMCPPayload {
	t.Helper()
	ctx := context.Background()
	var payload protocol.ApplyMCPPayload
	if err := st.pool.QueryRow(ctx, `SELECT d.runtime_kind,d.desired_generation,d.desired_hash,d.desired_enabled,p.id::text,p.name
		FROM mcp_deployments d JOIN mcp_profiles p ON p.id=d.profile_id WHERE d.id=$1`, deploymentID).
		Scan(&payload.Runtime, &payload.DesiredGeneration, &payload.DesiredHash, &payload.Enabled, &payload.ProfileID, &payload.ProfileName); err != nil {
		t.Fatal(err)
	}
	payload.Servers = []protocol.MCPServerRef{}
	if payload.Enabled {
		var raw []byte
		if err := st.pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM (
			SELECT s.id::text AS id,s.runtime_name AS name,s.transport,s.command,s.args,s.url,
			s.env_refs AS "envRefs",s.header_refs AS "headerRefs",ps.overrides
			FROM mcp_profile_servers ps JOIN mcp_servers s ON s.id=ps.server_id
			WHERE ps.profile_id=$1 AND s.enabled AND s.authority='toolhub' ORDER BY s.runtime_name,s.id) x`, payload.ProfileID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &payload.Servers); err != nil {
			t.Fatal(err)
		}
	}
	payload.DeploymentID = deploymentID
	payload.MCPMProfile = "toolhub-" + payload.Runtime
	return payload
}

func profileTestLegacyProfileHash(t *testing.T, st *Store, profileID string) string {
	t.Helper()
	var profile []byte
	if err := st.pool.QueryRow(context.Background(), `SELECT to_jsonb(q) FROM (SELECT p.id::text AS id,p.name,p.description,
		coalesce((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT s.id::text AS id,s.runtime_name AS name,s.transport,s.command,s.args,s.url,
		s.env_refs AS "envRefs",s.header_refs AS "headerRefs",ps.overrides FROM mcp_profile_servers ps JOIN mcp_servers s ON s.id=ps.server_id
		WHERE ps.profile_id=p.id AND s.enabled AND s.authority='toolhub' ORDER BY s.runtime_name,s.id) x),'[]'::jsonb) AS servers
		FROM mcp_profiles p WHERE p.id=$1 AND p.enabled) q`, profileID).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(security.TokenHash(string(profile)))
}

func profileTestApprovedSkill(t *testing.T, st *Store, adminID, slug string) string {
	t.Helper()
	pkg := testDiscoveredSkillPackage(t)
	pkg.Slug = slug
	pkg.Name = slug
	result, err := st.ImportSkill(context.Background(), SourceInput{Kind: "upload", Name: slug + ".zip"}, pkg, map[string]any{}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReviewSkill(context.Background(), result.SkillID, "approved", adminID); err != nil {
		t.Fatal(err)
	}
	return result.SkillID
}

func profileTestServerSecretRefs(t *testing.T, st *Store, serverID string) map[string]string {
	t.Helper()
	var envRaw, headerRaw []byte
	if err := st.pool.QueryRow(context.Background(), "SELECT env_refs,header_refs FROM mcp_servers WHERE id=$1", serverID).Scan(&envRaw, &headerRaw); err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, raw := range [][]byte{envRaw, headerRaw} {
		var refs map[string]string
		if err := json.Unmarshal(raw, &refs); err != nil {
			t.Fatal(err)
		}
		for key, value := range refs {
			result[key] = value
		}
	}
	return result
}

func hasActivationIssue(issues []ActivationIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func isActivationIssue(err error, code string) bool {
	var preflightErr *ActivationPreflightError
	return errors.As(err, &preflightErr) && hasActivationIssue(preflightErr.Preflight.Errors, code)
}

func sameProfileTestStringSet(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return reflect.DeepEqual(left, right)
}

func mcpPayloadServerIDs(payload protocol.ApplyMCPPayload) []string {
	result := make([]string, 0, len(payload.Servers))
	for _, server := range payload.Servers {
		result = append(result, server.ID)
	}
	return result
}

func targetServerIDs(servers []struct {
	ID string `json:"id"`
}) []string {
	result := make([]string, 0, len(servers))
	for _, server := range servers {
		result = append(result, server.ID)
	}
	return result
}
