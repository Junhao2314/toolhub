package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
)

type MCPServerInput struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	URL       string            `json:"url"`
	Env       map[string]string `json:"env"`
	Headers   map[string]string `json:"headers"`
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
	canonicalHeaders := make(map[string]string, len(input.Headers))
	for key, value := range input.Headers {
		canonical, err := protocol.NormalizeHeaderName(key)
		if err != nil {
			return "", err
		}
		if _, exists := canonicalHeaders[canonical]; exists {
			return "", errors.New("duplicate MCP HTTP header name")
		}
		canonicalHeaders[canonical] = value
	}
	input.Headers = canonicalHeaders
	descriptor, err := protocol.NormalizeMCPDescriptor("codex", domain.MCPDescriptor{Name: input.Name, Transport: input.Transport, Command: input.Command, Args: input.Args, URL: input.URL, EnvKeys: sortedKeys(input.Env), HeaderKeys: sortedKeys(input.Headers)})
	if err != nil {
		return "", err
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
			VALUES($1,$2,'mcp-env',$3,$4,$5)`, secretID, "mcp:"+serverID+":"+key, ciphertext, string(metadata), actor); err != nil {
			return "", err
		}
		envRefs[key] = secretID
	}
	headerRefs := map[string]string{}
	for key, value := range input.Headers {
		key = strings.TrimSpace(key)
		if key == "" || value == "" || len(key) > 128 {
			return "", errors.New("MCP header names and values cannot be empty")
		}
		secretID := uuid.NewString()
		ciphertext, err := s.cipher.Encrypt([]byte(value), secretID)
		if err != nil {
			return "", err
		}
		metadata, _ := json.Marshal(map[string]any{"mcpServerId": serverID, "headerName": key})
		if _, err := tx.Exec(ctx, `INSERT INTO encrypted_secrets(id,name,kind,ciphertext,metadata,created_by)
			VALUES($1,$2,'mcp-header',$3,$4,$5)`, secretID, "mcp:"+serverID+":header:"+key, ciphertext, string(metadata), actor); err != nil {
			return "", err
		}
		headerRefs[key] = secretID
	}
	args, _ := json.Marshal(input.Args)
	refs, _ := json.Marshal(envRefs)
	headerRefsJSON, _ := json.Marshal(headerRefs)
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_servers(id,name,runtime_name,transport,command,args,url,env_refs,header_refs,enabled,config_fingerprint,created_by)
		VALUES($1,$2,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, serverID, input.Name, descriptor.Transport, descriptor.Command, string(args), descriptor.URL, string(refs), string(headerRefsJSON), input.Enabled, descriptor.ConfigFingerprint, actor); err != nil {
		return "", err
	}
	return serverID, tx.Commit(ctx)
}

type MCPServerPatch struct {
	Name      string    `json:"name"`
	Enabled   *bool     `json:"enabled"`
	Transport *string   `json:"transport"`
	Command   *string   `json:"command"`
	Args      *[]string `json:"args"`
	URL       *string   `json:"url"`
}

func (s *Store) UpdateMCPServer(ctx context.Context, id string, patch MCPServerPatch) error {
	if patch.Enabled == nil && strings.TrimSpace(patch.Name) == "" && patch.Transport == nil && patch.Command == nil && patch.Args == nil && patch.URL == nil {
		return errors.New("no MCP server changes supplied")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentName, runtimeName, source, authority, transport, commandValue, urlValue string
	var argsJSON, refsJSON, headerRefsJSON []byte
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT name,runtime_name,source,authority,transport,command,args,url,env_refs,header_refs,enabled FROM mcp_servers WHERE id=$1 FOR UPDATE`, id).
		Scan(&currentName, &runtimeName, &source, &authority, &transport, &commandValue, &argsJSON, &urlValue, &refsJSON, &headerRefsJSON, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if authority == "shared-file" {
		return ErrSourceFileAuthoritative
	}
	var args []string
	var refs map[string]string
	var headerRefs map[string]string
	_ = json.Unmarshal(argsJSON, &args)
	_ = json.Unmarshal(refsJSON, &refs)
	_ = json.Unmarshal(headerRefsJSON, &headerRefs)
	if patch.Transport != nil {
		transport = *patch.Transport
	}
	if patch.Command != nil {
		commandValue = *patch.Command
	}
	if patch.Args != nil {
		args = append([]string(nil), (*patch.Args)...)
	}
	if patch.URL != nil {
		urlValue = *patch.URL
	}
	name := strings.TrimSpace(patch.Name)
	if name == "" {
		name = currentName
	} else if source != "runtime-auto" {
		runtimeName = name
	}
	if patch.Enabled != nil {
		enabled = *patch.Enabled
	}
	descriptor, err := protocol.NormalizeMCPDescriptor("codex", domain.MCPDescriptor{Name: runtimeName, Transport: transport, Command: commandValue, Args: args, URL: urlValue, EnvKeys: sortedKeys(refs), HeaderKeys: sortedKeys(headerRefs)})
	if err != nil {
		return err
	}
	argsJSON, _ = json.Marshal(descriptor.Args)
	if _, err := tx.Exec(ctx, `UPDATE mcp_servers SET name=$2,runtime_name=$3,transport=$4,command=$5,args=$6,url=$7,enabled=$8,config_fingerprint=$9,updated_at=now() WHERE id=$1`, id, name, runtimeName, descriptor.Transport, descriptor.Command, string(argsJSON), descriptor.URL, enabled, descriptor.ConfigFingerprint); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET desired_config_fingerprint=$2,desired_enabled=$3,drift=true,updated_at=now() WHERE server_id=$1`, id, descriptor.ConfigFingerprint, enabled); err != nil {
		return err
	}
	profileIDs, err := profileIDsForServer(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := refreshProfileDeployments(ctx, tx, profileIDs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteMCPServer(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var authority string
	if err := tx.QueryRow(ctx, "SELECT authority FROM mcp_servers WHERE id=$1 FOR UPDATE", id).Scan(&authority); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	} else if authority == "shared-file" {
		return ErrSourceFileAuthoritative
	}
	profileIDs, err := profileIDsForServer(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET desired_enabled=false,drift=NOT missing,updated_at=now() WHERE server_id=$1`, id); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, "DELETE FROM mcp_servers WHERE id=$1", id)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := refreshProfileDeployments(ctx, tx, profileIDs); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
		var authority string
		if err := tx.QueryRow(ctx, "SELECT authority FROM mcp_servers WHERE id=$1", serverID).Scan(&authority); errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		} else if err != nil {
			return "", err
		} else if authority == "shared-file" {
			return "", ErrSourceFileAuthoritative
		}
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

func (s *Store) SetMCPDeployments(ctx context.Context, profileID string, targets []MCPDeploymentTarget) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	desiredHash, err := profileHashTx(ctx, tx, profileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	deploymentIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		if !domain.IsMCPRuntime(target.Runtime) {
			return nil, errors.New("invalid runtime target")
		}
		var deploymentID string
		err := tx.QueryRow(ctx, `INSERT INTO mcp_deployments(id,profile_id,node_id,runtime_kind,desired_enabled,desired_hash,state)
			VALUES($1,$2,$3,$4,$5,$6,'pending') ON CONFLICT(profile_id,node_id,runtime_kind)
			DO UPDATE SET desired_enabled=excluded.desired_enabled,desired_hash=excluded.desired_hash,state='pending',updated_at=now()
			RETURNING id::text`, uuid.NewString(), profileID, target.NodeID, target.Runtime, target.Enabled, desiredHash).Scan(&deploymentID)
		if err != nil {
			return nil, err
		}
		deploymentIDs = append(deploymentIDs, deploymentID)
	}
	return deploymentIDs, tx.Commit(ctx)
}

func (s *Store) MCPDeploymentPayload(ctx context.Context, deploymentID string) (string, string, map[string]any, error) {
	var nodeID, runtime string
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT d.node_id::text,d.runtime_kind,jsonb_build_object(
		'profileId',p.id::text,'profileName',p.name,'enabled',d.desired_enabled,
		'servers',CASE WHEN d.desired_enabled THEN coalesce((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT s.id::text AS id,s.runtime_name AS name,s.transport,s.command,s.args,s.url,s.env_refs AS "envRefs",s.header_refs AS "headerRefs",ps.overrides FROM mcp_profile_servers ps JOIN mcp_servers s ON s.id=ps.server_id WHERE ps.profile_id=p.id AND s.enabled AND s.authority='toolhub' ORDER BY s.runtime_name,s.id) x),'[]'::jsonb) ELSE '[]'::jsonb END)
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

func profileIDsForServer(ctx context.Context, tx pgx.Tx, serverID string) ([]string, error) {
	rows, err := tx.Query(ctx, "SELECT profile_id::text FROM mcp_profile_servers WHERE server_id=$1", serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func refreshProfileDeployments(ctx context.Context, tx pgx.Tx, profileIDs []string) error {
	for _, profileID := range profileIDs {
		desiredHash, err := profileHashTx(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE mcp_deployments SET desired_hash=$2,state='pending',updated_at=now() WHERE profile_id=$1", profileID, desiredHash); err != nil {
			return err
		}
	}
	return nil
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
