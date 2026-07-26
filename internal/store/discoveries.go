package store

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/skills"
)

const mcpCaptureTTL = 5 * time.Minute

type MCPSecretCapture struct {
	Token    string            `json:"token"`
	Runtime  string            `json:"runtime"`
	Name     string            `json:"name"`
	Identity string            `json:"identity"`
	Secrets  map[string]string `json:"secrets"`
}

type MCPAdoptionResult struct {
	BindingID    string `json:"bindingId"`
	ServerID     string `json:"serverId"`
	ProfileID    string `json:"profileId"`
	DeploymentID string `json:"deploymentId"`
	Reused       bool   `json:"reused"`
}

type SkillAdoptionTarget struct {
	DiscoveryID string
	NodeID      string
	Runtime     string
	Path        string
	SHA256      string
}

type captureGrant struct {
	NodeID    string
	Runtime   string
	Name      string
	Identity  string
	ExpiresAt time.Time
	Used      bool
}

func (grant captureGrant) validate(now time.Time, nodeID, runtimeKind, name, identity string) error {
	switch {
	case grant.Used:
		return errors.New("capture token has already been used")
	case !now.Before(grant.ExpiresAt):
		return errors.New("capture token has expired")
	case grant.NodeID != nodeID:
		return errors.New("capture token belongs to another node")
	case grant.Runtime != runtimeKind || grant.Name != name || grant.Identity != identity:
		return errors.New("capture identity does not match the token")
	default:
		return nil
	}
}

func (s *Store) ProcessAgentInventory(ctx context.Context, nodeID string, runtimes []domain.InventoryRuntime, issueCaptureTokens bool) ([]domain.MCPCaptureRequest, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	type priorBinding struct {
		ID             string
		Drift          bool
		Missing        bool
		DesiredEnabled bool
	}
	priorBindings := map[string]priorBinding{}
	rows, err := tx.Query(ctx, `SELECT id::text,runtime_kind,server_name,drift,missing,desired_enabled FROM mcp_runtime_bindings WHERE node_id=$1`, nodeID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var prior priorBinding
		var runtimeKind, name string
		if err := rows.Scan(&prior.ID, &runtimeKind, &name, &prior.Drift, &prior.Missing, &prior.DesiredEnabled); err != nil {
			rows.Close()
			return nil, err
		}
		priorBindings[runtimeKind+"\x00"+name] = prior
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `UPDATE skill_discoveries SET missing=true,
		drift=CASE WHEN adopted_skill_id IS NOT NULL THEN true ELSE drift END,updated_at=now() WHERE node_id=$1`, nodeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET missing=true,drift=desired_enabled,updated_at=now() WHERE node_id=$1`, nodeID); err != nil {
		return nil, err
	}
	var unknown []struct {
		Runtime    string
		Descriptor domain.MCPDescriptor
	}
	var drifted []string
	seenBindings := map[string]bool{}
	for _, runtime := range runtimes {
		if runtime.Kind != "codex" && runtime.Kind != "claude" && runtime.Kind != "hermes" {
			return nil, errors.New("inventory contains an invalid runtime")
		}
		config, _ := json.Marshal(security.RedactMap(runtime.Config))
		inventory, _ := json.Marshal(security.RedactMap(runtime.Inventory))
		if _, err := tx.Exec(ctx, `INSERT INTO runtimes(id,node_id,kind,root_path,version,config,inventory,scanned_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,now())
			ON CONFLICT(node_id,kind,root_path) DO UPDATE SET version=excluded.version,config=excluded.config,inventory=excluded.inventory,scanned_at=now()`, uuid.NewString(), nodeID, runtime.Kind, runtime.RootPath, runtime.Version, string(config), string(inventory)); err != nil {
			return nil, err
		}
		if err := upsertSkillDiscoveries(ctx, tx, nodeID, runtime); err != nil {
			return nil, err
		}
		for _, rawDescriptor := range runtime.MCPServers {
			descriptor, err := protocol.NormalizeMCPDescriptor(runtime.Kind, rawDescriptor)
			if err != nil {
				return nil, fmt.Errorf("normalize MCP descriptor: %w", err)
			}
			if len(descriptor.EnvKeys) > 0 {
				decoded, decodeErr := hex.DecodeString(descriptor.SecretFingerprint)
				if decodeErr != nil || len(decoded) != 32 {
					return nil, errors.New("MCP descriptor secret fingerprint is required")
				}
			}
			var bindingID, desiredConfig, desiredSecret string
			var desiredEnabled bool
			err = tx.QueryRow(ctx, `SELECT id::text,desired_config_fingerprint,desired_secret_fingerprint,desired_enabled
				FROM mcp_runtime_bindings WHERE node_id=$1 AND runtime_kind=$2 AND server_name=$3 FOR UPDATE`, nodeID, runtime.Kind, descriptor.Name).
				Scan(&bindingID, &desiredConfig, &desiredSecret, &desiredEnabled)
			if errors.Is(err, pgx.ErrNoRows) {
				unknown = append(unknown, struct {
					Runtime    string
					Descriptor domain.MCPDescriptor
				}{runtime.Kind, descriptor})
				continue
			}
			if err != nil {
				return nil, err
			}
			drift := !desiredEnabled || descriptor.ConfigFingerprint != desiredConfig || descriptor.SecretFingerprint != desiredSecret
			bindingKey := runtime.Kind + "\x00" + descriptor.Name
			seenBindings[bindingKey] = true
			envKeys, _ := json.Marshal(descriptor.EnvKeys)
			if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET identity=$2,env_keys=$3,
				observed_config_fingerprint=$4,observed_secret_fingerprint=$5,missing=false,drift=$6,last_seen_at=now(),updated_at=now() WHERE id=$1`, bindingID, descriptor.Identity, string(envKeys), descriptor.ConfigFingerprint, descriptor.SecretFingerprint, drift); err != nil {
				return nil, err
			}
			if drift && !priorBindings[bindingKey].Drift {
				drifted = append(drifted, bindingID)
			}
		}
	}
	for key, prior := range priorBindings {
		if !seenBindings[key] && prior.DesiredEnabled && (!prior.Drift || !prior.Missing) {
			drifted = append(drifted, prior.ID)
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE nodes SET last_seen_at=now(),status='online',updated_at=now() WHERE id=$1", nodeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	for _, bindingID := range drifted {
		_ = s.Audit(ctx, domain.AuditEvent{Action: "runtime_drift_detected", ResourceType: "mcp_binding", ResourceID: bindingID, Outcome: "success", Metadata: map[string]any{"nodeId": nodeID}})
	}
	if !issueCaptureTokens {
		return nil, nil
	}
	requests := make([]domain.MCPCaptureRequest, 0, len(unknown))
	for _, item := range unknown {
		if len(item.Descriptor.EnvKeys) == 0 {
			result, adoptErr := s.adoptRuntimeMCP(ctx, nodeID, item.Runtime, item.Descriptor, nil)
			if adoptErr != nil {
				return nil, adoptErr
			}
			_ = s.Audit(ctx, domain.AuditEvent{Action: "runtime_auto_adopt", ResourceType: "mcp_binding", ResourceID: result.BindingID, Outcome: "success", Metadata: map[string]any{"nodeId": nodeID, "runtime": item.Runtime, "serverName": item.Descriptor.Name, "reused": result.Reused}})
			continue
		}
		request, err := s.createMCPCaptureToken(ctx, nodeID, item.Runtime, item.Descriptor)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, nil
}

func upsertSkillDiscoveries(ctx context.Context, tx pgx.Tx, nodeID string, runtime domain.InventoryRuntime) error {
	rawSkills, _ := runtime.Inventory["skills"].([]any)
	for _, raw := range rawSkills {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		pathValue, _ := item["path"].(string)
		name, _ := item["name"].(string)
		hash, _ := item["sha256"].(string)
		managed, _ := item["managed"].(bool)
		protected, _ := item["protected"].(bool)
		disabled, _ := item["disabled"].(bool)
		if strings.TrimSpace(pathValue) == "" || strings.TrimSpace(name) == "" {
			continue
		}
		_, err := tx.Exec(ctx, `INSERT INTO skill_discoveries(id,node_id,runtime_kind,canonical_path,name,directory_hash,managed,protected,disabled,last_seen_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
			ON CONFLICT(node_id,runtime_kind,canonical_path) DO UPDATE SET name=excluded.name,directory_hash=excluded.directory_hash,
			managed=excluded.managed,protected=excluded.protected,disabled=excluded.disabled,missing=false,last_seen_at=now(),updated_at=now(),
			drift=CASE WHEN skill_discoveries.adopted_version_id IS NULL THEN false ELSE
				excluded.directory_hash<>coalesce((SELECT content_sha256 FROM skill_versions WHERE id=skill_discoveries.adopted_version_id),'') OR NOT excluded.managed END,
			adoption_status=CASE WHEN skill_discoveries.adopted_version_id IS NOT NULL AND excluded.managed THEN 'adopted' ELSE skill_discoveries.adoption_status END`, uuid.NewString(), nodeID, runtime.Kind, pathValue, name, hash, managed, protected, disabled)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) createMCPCaptureToken(ctx context.Context, nodeID, runtimeKind string, descriptor domain.MCPDescriptor) (domain.MCPCaptureRequest, error) {
	token, err := security.RandomToken(32)
	if err != nil {
		return domain.MCPCaptureRequest{}, err
	}
	body, _ := json.Marshal(descriptor)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.MCPCaptureRequest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "mcp-capture:"+nodeID+":"+runtimeKind+":"+descriptor.Name); err != nil {
		return domain.MCPCaptureRequest{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_capture_tokens SET used_at=now() WHERE node_id=$1 AND runtime_kind=$2 AND server_name=$3 AND used_at IS NULL`, nodeID, runtimeKind, descriptor.Name); err != nil {
		return domain.MCPCaptureRequest{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_capture_tokens(id,token_hash,node_id,runtime_kind,server_name,identity,descriptor,config_fingerprint,secret_fingerprint,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, uuid.NewString(), security.TokenHash(token), nodeID, runtimeKind, descriptor.Name, descriptor.Identity, string(body), descriptor.ConfigFingerprint, descriptor.SecretFingerprint, time.Now().UTC().Add(mcpCaptureTTL)); err != nil {
		return domain.MCPCaptureRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.MCPCaptureRequest{}, err
	}
	return domain.MCPCaptureRequest{Token: token, Runtime: runtimeKind, Name: descriptor.Name, Identity: descriptor.Identity}, nil
}

func (s *Store) CaptureRuntimeMCP(ctx context.Context, nodeID string, capture MCPSecretCapture) (MCPAdoptionResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var grant captureGrant
	var descriptorJSON []byte
	var configFingerprint, secretFingerprint string
	var usedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT node_id::text,runtime_kind,server_name,identity,descriptor,config_fingerprint,secret_fingerprint,expires_at,used_at
		FROM mcp_capture_tokens WHERE token_hash=$1 FOR UPDATE`, security.TokenHash(capture.Token)).
		Scan(&grant.NodeID, &grant.Runtime, &grant.Name, &grant.Identity, &descriptorJSON, &configFingerprint, &secretFingerprint, &grant.ExpiresAt, &usedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPAdoptionResult{}, ErrNotFound
	}
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	grant.Used = usedAt != nil
	if err := grant.validate(time.Now().UTC(), nodeID, capture.Runtime, capture.Name, capture.Identity); err != nil {
		return MCPAdoptionResult{}, err
	}
	var descriptor domain.MCPDescriptor
	if err := json.Unmarshal(descriptorJSON, &descriptor); err != nil {
		return MCPAdoptionResult{}, errors.New("capture descriptor is invalid")
	}
	if !sameStringSet(descriptor.EnvKeys, mapKeys(capture.Secrets)) {
		return MCPAdoptionResult{}, errors.New("captured secret names do not match the descriptor")
	}
	key, err := agentTaskKeyTx(ctx, tx, nodeID, s.cipher)
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	if subtle.ConstantTimeCompare([]byte(security.FingerprintSecretMap(key, capture.Secrets)), []byte(secretFingerprint)) != 1 {
		return MCPAdoptionResult{}, errors.New("captured secrets do not match the descriptor fingerprint")
	}
	result, err := s.adoptRuntimeMCPTx(ctx, tx, nodeID, capture.Runtime, descriptor, capture.Secrets)
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mcp_capture_tokens SET used_at=now() WHERE token_hash=$1", security.TokenHash(capture.Token)); err != nil {
		return MCPAdoptionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MCPAdoptionResult{}, err
	}
	_ = s.Audit(ctx, domain.AuditEvent{Action: "runtime_secret_capture", ResourceType: "mcp_binding", ResourceID: result.BindingID, Outcome: "success", Metadata: map[string]any{"nodeId": nodeID, "runtime": capture.Runtime, "serverName": capture.Name, "secretNames": mapKeys(capture.Secrets)}})
	_ = s.Audit(ctx, domain.AuditEvent{Action: "runtime_auto_adopt", ResourceType: "mcp_binding", ResourceID: result.BindingID, Outcome: "success", Metadata: map[string]any{"nodeId": nodeID, "runtime": capture.Runtime, "serverName": capture.Name, "reused": result.Reused}})
	return result, nil
}

func agentTaskKeyTx(ctx context.Context, tx pgx.Tx, nodeID string, cipher *security.Cipher) ([]byte, error) {
	var secretID string
	var ciphertext []byte
	if err := tx.QueryRow(ctx, `SELECT es.id::text,es.ciphertext FROM nodes n JOIN encrypted_secrets es ON es.id=n.task_key_secret_id WHERE n.id=$1`, nodeID).Scan(&secretID, &ciphertext); err != nil {
		return nil, err
	}
	return cipher.Decrypt(ciphertext, secretID)
}

func (s *Store) adoptRuntimeMCP(ctx context.Context, nodeID, runtimeKind string, descriptor domain.MCPDescriptor, secrets map[string]string) (MCPAdoptionResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := s.adoptRuntimeMCPTx(ctx, tx, nodeID, runtimeKind, descriptor, secrets)
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) adoptRuntimeMCPTx(ctx context.Context, tx pgx.Tx, nodeID, runtimeKind string, descriptor domain.MCPDescriptor, secrets map[string]string) (MCPAdoptionResult, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "runtime-binding:"+nodeID+":"+runtimeKind+":"+descriptor.Name); err != nil {
		return MCPAdoptionResult{}, err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "runtime-server:"+descriptor.Name+":"+descriptor.ConfigFingerprint); err != nil {
		return MCPAdoptionResult{}, err
	}
	var existing MCPAdoptionResult
	err := tx.QueryRow(ctx, `SELECT id::text,coalesce(server_id::text,''),coalesce(profile_id::text,''),coalesce(deployment_id::text,'')
		FROM mcp_runtime_bindings WHERE node_id=$1 AND runtime_kind=$2 AND server_name=$3`, nodeID, runtimeKind, descriptor.Name).
		Scan(&existing.BindingID, &existing.ServerID, &existing.ProfileID, &existing.DeploymentID)
	if err == nil {
		existing.Reused = true
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return MCPAdoptionResult{}, err
	}
	serverID, reused, err := s.findRuntimeAutoServer(ctx, tx, descriptor, secrets)
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	if serverID == "" {
		serverID, err = s.createRuntimeAutoServer(ctx, tx, nodeID, runtimeKind, descriptor, secrets)
		if err != nil {
			return MCPAdoptionResult{}, err
		}
	}
	profileID, err := ensureRuntimeAutoProfile(ctx, tx, nodeID, runtimeKind)
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	var existingAttention int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM mcp_runtime_bindings WHERE node_id=$1 AND runtime_kind=$2 AND (drift OR missing)`, nodeID, runtimeKind).Scan(&existingAttention); err != nil {
		return MCPAdoptionResult{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mcp_profile_servers(profile_id,server_id) VALUES($1,$2) ON CONFLICT DO NOTHING", profileID, serverID); err != nil {
		return MCPAdoptionResult{}, err
	}
	desiredHash, err := profileHashTx(ctx, tx, profileID)
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	var deploymentID string
	err = tx.QueryRow(ctx, "SELECT id::text FROM mcp_deployments WHERE profile_id=$1 AND node_id=$2 AND runtime_kind=$3 FOR UPDATE", profileID, nodeID, runtimeKind).Scan(&deploymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		deploymentID = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO mcp_deployments(id,profile_id,node_id,runtime_kind,desired_enabled,actual_hash,desired_hash,state)
			VALUES($1,$2,$3,$4,true,$5,$5,'in_sync')`, deploymentID, profileID, nodeID, runtimeKind, desiredHash)
	} else if err == nil && existingAttention == 0 {
		_, err = tx.Exec(ctx, "UPDATE mcp_deployments SET desired_enabled=true,actual_hash=$2,desired_hash=$2,state='in_sync',last_error='',updated_at=now() WHERE id=$1", deploymentID, desiredHash)
	} else if err == nil {
		_, err = tx.Exec(ctx, "UPDATE mcp_deployments SET desired_enabled=true,desired_hash=$2,state='drift',updated_at=now() WHERE id=$1", deploymentID, desiredHash)
	}
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	bindingID := uuid.NewString()
	envKeys, _ := json.Marshal(descriptor.EnvKeys)
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_runtime_bindings(id,node_id,runtime_kind,server_name,identity,server_id,profile_id,deployment_id,env_keys,
		observed_config_fingerprint,observed_secret_fingerprint,desired_config_fingerprint,desired_secret_fingerprint,desired_enabled,missing,drift,last_seen_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$10,$11,true,false,false,now())`, bindingID, nodeID, runtimeKind, descriptor.Name, descriptor.Identity, serverID, profileID, deploymentID, string(envKeys), descriptor.ConfigFingerprint, descriptor.SecretFingerprint); err != nil {
		return MCPAdoptionResult{}, err
	}
	return MCPAdoptionResult{BindingID: bindingID, ServerID: serverID, ProfileID: profileID, DeploymentID: deploymentID, Reused: reused}, nil
}

func (s *Store) findRuntimeAutoServer(ctx context.Context, tx pgx.Tx, descriptor domain.MCPDescriptor, secrets map[string]string) (string, bool, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,env_refs FROM mcp_servers WHERE source='runtime-auto' AND enabled AND runtime_name=$1 AND config_fingerprint=$2 ORDER BY created_at`, descriptor.Name, descriptor.ConfigFingerprint)
	if err != nil {
		return "", false, err
	}
	type candidate struct {
		id   string
		refs map[string]string
	}
	var candidates []candidate
	for rows.Next() {
		var id string
		var refsJSON []byte
		if err := rows.Scan(&id, &refsJSON); err != nil {
			rows.Close()
			return "", false, err
		}
		var refs map[string]string
		if json.Unmarshal(refsJSON, &refs) != nil || !sameStringSet(mapKeys(refs), mapKeys(secrets)) {
			continue
		}
		candidates = append(candidates, candidate{id: id, refs: refs})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", false, err
	}
	rows.Close()
	for _, candidate := range candidates {
		matches := true
		for name, secretID := range candidate.refs {
			var ciphertext []byte
			if err := tx.QueryRow(ctx, "SELECT ciphertext FROM encrypted_secrets WHERE id=$1", secretID).Scan(&ciphertext); err != nil {
				return "", false, err
			}
			plaintext, err := s.cipher.Decrypt(ciphertext, secretID)
			if err != nil {
				return "", false, err
			}
			if subtle.ConstantTimeCompare(plaintext, []byte(secrets[name])) != 1 {
				matches = false
			}
		}
		if matches {
			return candidate.id, true, nil
		}
	}
	return "", false, nil
}

func (s *Store) createRuntimeAutoServer(ctx context.Context, tx pgx.Tx, nodeID, runtimeKind string, descriptor domain.MCPDescriptor, secrets map[string]string) (string, error) {
	serverID := uuid.NewString()
	name, err := uniqueRuntimeAutoServerName(ctx, tx, descriptor.Name, nodeID, runtimeKind)
	if err != nil {
		return "", err
	}
	refs := map[string]string{}
	for _, key := range descriptor.EnvKeys {
		secretID, err := s.createSecret(ctx, tx, "mcp:"+serverID+":"+key, "mcp-env", []byte(secrets[key]), map[string]any{"mcpServerId": serverID, "envName": key, "source": "runtime-auto"}, "")
		if err != nil {
			return "", err
		}
		refs[key] = secretID
	}
	args, _ := json.Marshal(descriptor.Args)
	refsJSON, _ := json.Marshal(refs)
	origin, _ := json.Marshal(map[string]any{"nodeId": nodeID, "runtime": runtimeKind, "serverName": descriptor.Name})
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_servers(id,name,runtime_name,transport,command,args,url,env_refs,enabled,source,origin,config_fingerprint)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,true,'runtime-auto',$9,$10)`, serverID, name, descriptor.Name, descriptor.Transport, descriptor.Command, string(args), descriptor.URL, string(refsJSON), string(origin), descriptor.ConfigFingerprint); err != nil {
		return "", err
	}
	return serverID, nil
}

func uniqueRuntimeAutoServerName(ctx context.Context, tx pgx.Tx, original, nodeID, runtimeKind string) (string, error) {
	base := strings.TrimSpace(original)
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mcp_servers WHERE name=$1)", base).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return base, nil
	}
	suffix := runtimeKind + "@" + strings.SplitN(nodeID, "-", 2)[0]
	for index := 1; index < 1000; index++ {
		candidate := base + " · " + suffix
		if index > 1 {
			candidate += fmt.Sprintf("-%d", index)
		}
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mcp_servers WHERE name=$1)", candidate).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", errors.New("could not allocate a unique MCP server name")
}

func ensureRuntimeAutoProfile(ctx context.Context, tx pgx.Tx, nodeID, runtimeKind string) (string, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "runtime-auto-profile:"+nodeID+":"+runtimeKind); err != nil {
		return "", err
	}
	var profileID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM mcp_profiles WHERE source='runtime-auto' AND origin->>'nodeId'=$1 AND origin->>'runtime'=$2`, nodeID, runtimeKind).Scan(&profileID)
	if err == nil {
		return profileID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var nodeName string
	if err := tx.QueryRow(ctx, "SELECT name FROM nodes WHERE id=$1", nodeID).Scan(&nodeName); err != nil {
		return "", err
	}
	profileID = uuid.NewString()
	origin, _ := json.Marshal(map[string]string{"nodeId": nodeID, "runtime": runtimeKind})
	_, err = tx.Exec(ctx, `INSERT INTO mcp_profiles(id,name,description,enabled,source,origin)
		VALUES($1,$2,$3,true,'runtime-auto',$4)`, profileID, "Auto · "+nodeName+" · "+runtimeKind, "Automatically managed runtime discovery baseline", string(origin))
	return profileID, err
}

func profileHashTx(ctx context.Context, tx pgx.Tx, profileID string) (string, error) {
	var profile []byte
	err := tx.QueryRow(ctx, `SELECT to_jsonb(q) FROM (SELECT p.id::text AS id,p.name,p.description,
		coalesce((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT s.id::text AS id,s.runtime_name AS name,s.transport,s.command,s.args,s.url,s.env_refs AS "envRefs",ps.overrides FROM mcp_profile_servers ps JOIN mcp_servers s ON s.id=ps.server_id WHERE ps.profile_id=p.id AND s.enabled ORDER BY s.runtime_name,s.id) x),'[]'::jsonb) AS servers
		FROM mcp_profiles p WHERE p.id=$1 AND p.enabled) q`, profileID).Scan(&profile)
	if err != nil {
		return "", err
	}
	sum := security.TokenHash(string(profile))
	return hex.EncodeToString(sum), nil
}

func (s *Store) ListDiscoveries(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT * FROM (
		SELECT sd.id::text AS id,'skill'::text AS kind,sd.node_id::text AS "nodeId",n.name AS "nodeName",sd.runtime_kind AS runtime,
			sd.name,sd.canonical_path AS path,sd.directory_hash AS sha256,sd.managed,sd.protected,sd.disabled,sd.missing,sd.drift,
			sd.adoption_status AS status,sd.adoption_error AS "lastError",coalesce(sd.adopted_skill_id::text,'') AS "adoptedSkillId",sd.last_seen_at AS "lastSeenAt",
			''::text AS source,''::text AS "serverId",''::text AS "profileId",''::text AS "deploymentId"
		FROM skill_discoveries sd JOIN nodes n ON n.id=sd.node_id
		UNION ALL
		SELECT mb.id::text AS id,'mcp'::text AS kind,mb.node_id::text AS "nodeId",n.name AS "nodeName",mb.runtime_kind AS runtime,
			mb.server_name AS name,''::text AS path,''::text AS sha256,false AS managed,false AS protected,false AS disabled,mb.missing,mb.drift,
			CASE WHEN mb.drift THEN 'drift' WHEN mb.missing THEN 'missing' ELSE 'managed' END AS status,''::text AS "lastError",''::text AS "adoptedSkillId",mb.last_seen_at AS "lastSeenAt",
			'runtime-auto'::text AS source,coalesce(mb.server_id::text,'') AS "serverId",coalesce(mb.profile_id::text,'') AS "profileId",coalesce(mb.deployment_id::text,'') AS "deploymentId"
		FROM mcp_runtime_bindings mb JOIN nodes n ON n.id=mb.node_id
	) discoveries ORDER BY kind,"nodeName",runtime,name`)
}

func (s *Store) SkillDiscoveryForAdoption(ctx context.Context, discoveryID string) (SkillAdoptionTarget, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SkillAdoptionTarget{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var target SkillAdoptionTarget
	err = tx.QueryRow(ctx, `SELECT id::text,node_id::text,runtime_kind,canonical_path,directory_hash FROM skill_discoveries
		WHERE id=$1 AND NOT missing AND NOT managed AND NOT protected FOR UPDATE`, discoveryID).
		Scan(&target.DiscoveryID, &target.NodeID, &target.Runtime, &target.Path, &target.SHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillAdoptionTarget{}, ErrNotFound
	}
	if err != nil {
		return SkillAdoptionTarget{}, err
	}
	if target.SHA256 == "" {
		return SkillAdoptionTarget{}, errors.New("discovered skill could not be safely hashed")
	}
	if _, err := tx.Exec(ctx, "UPDATE skill_discoveries SET adoption_status='adopting',adoption_error='',updated_at=now() WHERE id=$1", discoveryID); err != nil {
		return SkillAdoptionTarget{}, err
	}
	return target, tx.Commit(ctx)
}

func (s *Store) FailSkillAdoption(ctx context.Context, discoveryID string, cause error) {
	message := "adoption failed"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	_, _ = s.pool.Exec(ctx, "UPDATE skill_discoveries SET adoption_status='failed',adoption_error=$2,updated_at=now() WHERE id=$1", discoveryID, message)
}

func (s *Store) ImportDiscoveredSkill(ctx context.Context, nodeID, discoveryID, taskID string, pkg skills.Package) (ImportedSkill, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ImportedSkill{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var runtimeKind, pathValue, expectedHash, adoptedSkillID, adoptedVersionID string
	err = tx.QueryRow(ctx, `SELECT runtime_kind,canonical_path,directory_hash,coalesce(adopted_skill_id::text,''),coalesce(adopted_version_id::text,'')
		FROM skill_discoveries WHERE id=$1 AND node_id=$2 FOR UPDATE`, discoveryID, nodeID).
		Scan(&runtimeKind, &pathValue, &expectedHash, &adoptedSkillID, &adoptedVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ImportedSkill{}, ErrNotFound
	}
	if err != nil {
		return ImportedSkill{}, err
	}
	if adoptedVersionID != "" {
		return ImportedSkill{SkillID: adoptedSkillID, VersionID: adoptedVersionID, SHA256: expectedHash, Status: "pending"}, tx.Commit(ctx)
	}
	var taskPayload []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM node_tasks WHERE id=$1 AND node_id=$2 AND kind='adopt_skill' AND status IN ('pending','delivered','running')", taskID, nodeID).Scan(&taskPayload); err != nil {
		return ImportedSkill{}, errors.New("skill adoption upload is not authorized")
	}
	var task struct {
		DiscoveryID string `json:"discoveryId"`
	}
	if json.Unmarshal(taskPayload, &task) != nil || task.DiscoveryID != discoveryID {
		return ImportedSkill{}, errors.New("skill adoption task identity mismatch")
	}
	if pkg.SHA256 != expectedHash {
		return ImportedSkill{}, errors.New("uploaded skill hash does not match discovery")
	}
	result, err := importDiscoveredSkillTx(ctx, tx, nodeID, runtimeKind, pathValue, pkg)
	if err != nil {
		return ImportedSkill{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE skill_discoveries SET adopted_skill_id=$2,adopted_version_id=$3,adoption_status='imported',adoption_error='',updated_at=now() WHERE id=$1`, discoveryID, result.SkillID, result.VersionID); err != nil {
		return ImportedSkill{}, err
	}
	return result, tx.Commit(ctx)
}

func importDiscoveredSkillTx(ctx context.Context, tx pgx.Tx, nodeID, runtimeKind, pathValue string, pkg skills.Package) (ImportedSkill, error) {
	result := ImportedSkill{SkillID: uuid.NewString(), VersionID: uuid.NewString(), SourceID: uuid.NewString(), SHA256: pkg.SHA256, RiskLevel: pkg.Report.RiskLevel, Status: "pending"}
	sourceName := "Node snapshot · " + runtimeKind + " · " + filepathBase(pathValue)
	if _, err := tx.Exec(ctx, `INSERT INTO skill_sources(id,kind,name,subdirectory) VALUES($1,'node',$2,$3)`, result.SourceID, sourceName, pathValue); err != nil {
		return ImportedSkill{}, err
	}
	slug, err := uniqueDiscoveredSkillSlug(ctx, tx, pkg.Slug, nodeID, runtimeKind)
	if err != nil {
		return ImportedSkill{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO skills(id,slug,name,description,source_id) VALUES($1,$2,$3,$4,$5)`, result.SkillID, slug, pkg.Name, pkg.Description, result.SourceID); err != nil {
		return ImportedSkill{}, err
	}
	report, _ := json.Marshal(pkg.Report)
	var artifactID string
	err = tx.QueryRow(ctx, "SELECT id::text FROM skill_artifacts WHERE sha256=$1", pkg.SHA256).Scan(&artifactID)
	if errors.Is(err, pgx.ErrNoRows) {
		artifactID = uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO skill_artifacts(id,sha256,size_bytes,content,scan_report) VALUES($1,$2,$3,$4,$5)`, artifactID, pkg.SHA256, len(pkg.CanonicalZIP), pkg.CanonicalZIP, string(report)); err != nil {
			return ImportedSkill{}, err
		}
	} else if err != nil {
		return ImportedSkill{}, err
	}
	manifest, _ := json.Marshal(pkg.Manifest)
	provenance, _ := json.Marshal(map[string]any{"sourceKind": "node", "nodeId": nodeID, "runtime": runtimeKind, "path": pathValue, "contentSHA256": pkg.SHA256})
	if _, err := tx.Exec(ctx, `INSERT INTO skill_versions(id,skill_id,source_commit,content_sha256,artifact_id,provenance,manifest,risk_level)
		VALUES($1,$2,'',$3,$4,$5,$6,$7)`, result.VersionID, result.SkillID, pkg.SHA256, artifactID, string(provenance), string(manifest), pkg.Report.RiskLevel); err != nil {
		return ImportedSkill{}, err
	}
	return result, nil
}

func uniqueDiscoveredSkillSlug(ctx context.Context, tx pgx.Tx, base, nodeID, runtimeKind string) (string, error) {
	candidates := []string{base, base + "-" + runtimeKind + "-" + strings.SplitN(nodeID, "-", 2)[0]}
	for index := 0; index < 1000; index++ {
		candidate := candidates[len(candidates)-1]
		if index == 0 {
			candidate = candidates[0]
		} else if index > 1 {
			candidate += fmt.Sprintf("-%d", index)
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM skills WHERE slug=$1)", candidate).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", errors.New("could not allocate a unique discovered Skill slug")
}

func filepathBase(value string) string {
	value = strings.TrimRight(strings.ReplaceAll(value, "\\", "/"), "/")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}

func mapKeys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func sameStringSet(first, second []string) bool {
	first = append([]string(nil), first...)
	second = append([]string(nil), second...)
	sort.Strings(first)
	sort.Strings(second)
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
