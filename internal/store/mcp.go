package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
)

type MCPServerInput struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	URL       string            `json:"url"`
	Env       map[string]string `json:"env"`
	Enabled   bool              `json:"enabled"`
}

func (s *Store) CreateMCPServer(ctx context.Context, input MCPServerInput, actor string) (string, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || (input.Transport != "stdio" && input.Transport != "sse" && input.Transport != "streamable-http") {
		return "", errors.New("valid name and transport are required")
	}
	if input.Transport == "stdio" && strings.TrimSpace(input.Command) == "" {
		return "", errors.New("stdio servers require a command")
	}
	if input.Transport != "stdio" && strings.TrimSpace(input.URL) == "" {
		return "", errors.New("network MCP servers require a URL")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	serverID := uuid.NewString()
	envRefs := map[string]string{}
	for key, value := range input.Env {
		key = strings.TrimSpace(key)
		if key == "" || value == "" || len(key) > 128 {
			return "", errors.New("MCP environment names and values cannot be empty")
		}
		secretID := uuid.NewString()
		ciphertext, err := s.cipher.Encrypt([]byte(value), secretID)
		if err != nil {
			return "", err
		}
		metadata, _ := json.Marshal(map[string]any{"mcpServerId": serverID, "envName": key})
		if _, err := tx.Exec(ctx, `INSERT INTO encrypted_secrets(id,name,kind,ciphertext,metadata,created_by)
			VALUES($1,$2,'mcp-env',$3,$4,$5)`, secretID, "mcp:"+serverID+":"+key, ciphertext, metadata, actor); err != nil {
			return "", err
		}
		envRefs[key] = secretID
	}
	args, _ := json.Marshal(input.Args)
	refs, _ := json.Marshal(envRefs)
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_servers(id,name,transport,command,args,url,env_refs,enabled,created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, serverID, input.Name, input.Transport, strings.TrimSpace(input.Command), args, strings.TrimSpace(input.URL), refs, input.Enabled, actor); err != nil {
		return "", err
	}
	return serverID, tx.Commit(ctx)
}

func (s *Store) UpdateMCPServer(ctx context.Context, id string, enabled *bool, name string) error {
	if enabled == nil && strings.TrimSpace(name) == "" {
		return errors.New("no MCP server changes supplied")
	}
	command, err := s.pool.Exec(ctx, `UPDATE mcp_servers SET
		name=CASE WHEN $2='' THEN name ELSE $2 END,
		enabled=coalesce($3,enabled),updated_at=now() WHERE id=$1`, id, strings.TrimSpace(name), enabled)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteMCPServer(ctx context.Context, id string) error {
	command, err := s.pool.Exec(ctx, "DELETE FROM mcp_servers WHERE id=$1", id)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateMCPProfile(ctx context.Context, name, description, actor string, serverIDs []string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("profile name is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id := uuid.NewString()
	if _, err := tx.Exec(ctx, "INSERT INTO mcp_profiles(id,name,description,created_by) VALUES($1,$2,$3,$4)", id, strings.TrimSpace(name), strings.TrimSpace(description), actor); err != nil {
		return "", err
	}
	for _, serverID := range serverIDs {
		if _, err := tx.Exec(ctx, "INSERT INTO mcp_profile_servers(profile_id,server_id) VALUES($1,$2)", id, serverID); err != nil {
			return "", err
		}
	}
	return id, tx.Commit(ctx)
}

type MCPDeploymentTarget struct {
	NodeID  string `json:"nodeId"`
	Runtime string `json:"runtime"`
	Enabled bool   `json:"enabled"`
}

func (s *Store) SetMCPDeployments(ctx context.Context, profileID string, targets []MCPDeploymentTarget) error {
	profile, err := s.JSONObject(ctx, `SELECT p.id::text AS id,p.name,p.description,
		coalesce((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT s.id::text AS id,s.name,s.transport,s.command,s.args,s.url,s.env_refs AS "envRefs",ps.overrides FROM mcp_profile_servers ps JOIN mcp_servers s ON s.id=ps.server_id WHERE ps.profile_id=p.id AND s.enabled ORDER BY s.name) x),'[]') AS servers
		FROM mcp_profiles p WHERE p.id=$1 AND p.enabled`, profileID)
	if err != nil {
		return ErrNotFound
	}
	sum := sha256.Sum256(profile)
	desiredHash := hex.EncodeToString(sum[:])
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, target := range targets {
		if target.Runtime != "codex" && target.Runtime != "claude" && target.Runtime != "hermes" {
			return errors.New("invalid runtime target")
		}
		_, err := tx.Exec(ctx, `INSERT INTO mcp_deployments(id,profile_id,node_id,runtime_kind,desired_enabled,desired_hash,state)
			VALUES($1,$2,$3,$4,$5,$6,'pending') ON CONFLICT(profile_id,node_id,runtime_kind)
			DO UPDATE SET desired_enabled=excluded.desired_enabled,desired_hash=excluded.desired_hash,state='pending',updated_at=now()`, uuid.NewString(), profileID, target.NodeID, target.Runtime, target.Enabled, desiredHash)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) MCPDeploymentPayload(ctx context.Context, deploymentID string) (string, string, map[string]any, error) {
	var nodeID, runtime string
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT d.node_id::text,d.runtime_kind,jsonb_build_object(
		'profileId',p.id::text,'profileName',p.name,'enabled',d.desired_enabled,
		'servers',coalesce((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT s.id::text AS id,s.name,s.transport,s.command,s.args,s.url,s.env_refs AS "envRefs",ps.overrides FROM mcp_profile_servers ps JOIN mcp_servers s ON s.id=ps.server_id WHERE ps.profile_id=p.id AND s.enabled ORDER BY s.name) x),'[]'::jsonb))
		FROM mcp_deployments d JOIN mcp_profiles p ON p.id=d.profile_id WHERE d.id=$1`, deploymentID).Scan(&nodeID, &runtime, &raw)
	if err != nil {
		return "", "", nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", "", nil, err
	}
	payload["runtime"] = runtime
	return nodeID, runtime, payload, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var _ = sortedKeys
