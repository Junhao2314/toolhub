package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/security"
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
	Name           string            `json:"name"`
	Enabled        *bool             `json:"enabled"`
	Transport      *string           `json:"transport"`
	Command        *string           `json:"command"`
	Args           *[]string         `json:"args"`
	URL            *string           `json:"url"`
	SecretChanges  *MCPSecretChanges `json:"secretChanges"`
	ConfirmTargets bool              `json:"confirmTargets"`
	Actor          string            `json:"-"`
}

type MCPSecretChanges struct {
	Env     MCPSecretDelta `json:"env"`
	Headers MCPSecretDelta `json:"headers"`
}

type MCPSecretDelta struct {
	Set    map[string]string `json:"set"`
	Remove []string          `json:"remove"`
}

func (s *Store) UpdateMCPServer(ctx context.Context, id string, patch MCPServerPatch) error {
	envSet, envRemove, headerSet, headerRemove, secretsChanged, err := normalizeMCPSecretChanges(patch.SecretChanges)
	if err != nil {
		return err
	}
	if patch.Enabled == nil && strings.TrimSpace(patch.Name) == "" && patch.Transport == nil && patch.Command == nil && patch.Args == nil && patch.URL == nil && !secretsChanged {
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
	managed, err := mcpServerInActiveToolHubProfileTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if managed {
		return ErrTargetManagedByProfile
	}
	var args []string
	var refs map[string]string
	var headerRefs map[string]string
	_ = json.Unmarshal(argsJSON, &args)
	_ = json.Unmarshal(refsJSON, &refs)
	_ = json.Unmarshal(headerRefsJSON, &headerRefs)
	if refs == nil {
		refs = map[string]string{}
	}
	if headerRefs == nil {
		headerRefs = map[string]string{}
	}
	if secretsChanged && !patch.ConfirmTargets {
		targets, err := mcpServerAffectedTargetsTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(targets) > 0 {
			return &SecretConfirmationRequiredError{
				EnvKeys:    sortedUnique(append(mapKeys(envSet), envRemove...)),
				HeaderKeys: sortedUnique(append(mapKeys(headerSet), headerRemove...)),
				Targets:    targets,
			}
		}
	}
	for _, key := range envRemove {
		delete(refs, key)
	}
	for _, key := range headerRemove {
		delete(headerRefs, key)
	}
	for key, value := range envSet {
		secretID, err := s.createSecret(ctx, tx, "mcp:"+id+":env:"+key+":"+uuid.NewString(), "mcp-env", []byte(value),
			map[string]any{"mcpServerId": id, "envName": key, "revision": "secret-delta"}, patch.Actor)
		if err != nil {
			return err
		}
		refs[key] = secretID
	}
	for key, value := range headerSet {
		secretID, err := s.createSecret(ctx, tx, "mcp:"+id+":header:"+key+":"+uuid.NewString(), "mcp-header", []byte(value),
			map[string]any{"mcpServerId": id, "headerName": key, "revision": "secret-delta"}, patch.Actor)
		if err != nil {
			return err
		}
		headerRefs[key] = secretID
	}
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
	refsJSON, _ = json.Marshal(refs)
	headerRefsJSON, _ = json.Marshal(headerRefs)
	if _, err := tx.Exec(ctx, `UPDATE mcp_servers SET name=$2,runtime_name=$3,transport=$4,command=$5,args=$6,url=$7,
		env_refs=$8,header_refs=$9,enabled=$10,config_fingerprint=$11,updated_at=now() WHERE id=$1`, id, name, runtimeName,
		descriptor.Transport, descriptor.Command, string(argsJSON), descriptor.URL, string(refsJSON), string(headerRefsJSON), enabled, descriptor.ConfigFingerprint); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET desired_config_fingerprint=$2,desired_enabled=$3,drift=true,updated_at=now() WHERE server_id=$1`, id, descriptor.ConfigFingerprint, enabled); err != nil {
		return err
	}
	if secretsChanged {
		rows, err := tx.Query(ctx, `SELECT id::text,node_id::text FROM mcp_runtime_bindings
			WHERE server_id=$1 AND control_mode='managed_target' FOR UPDATE`, id)
		if err != nil {
			return err
		}
		type bindingTarget struct{ id, nodeID string }
		var bindings []bindingTarget
		for rows.Next() {
			var binding bindingTarget
			if err := rows.Scan(&binding.id, &binding.nodeID); err != nil {
				rows.Close()
				return err
			}
			bindings = append(bindings, binding)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, binding := range bindings {
			key, err := agentTaskKeyTx(ctx, tx, binding.nodeID, s.cipher)
			if err != nil {
				return err
			}
			fingerprint, err := s.mcpSecretFingerprintTx(ctx, tx, key, refs, headerRefs)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET desired_secret_fingerprint=$2,drift=true,updated_at=now()
				WHERE id=$1`, binding.id, fingerprint); err != nil {
				return err
			}
		}
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

func normalizeMCPSecretChanges(changes *MCPSecretChanges) (map[string]string, []string, map[string]string, []string, bool, error) {
	if changes == nil {
		return nil, nil, nil, nil, false, nil
	}
	envSet := map[string]string{}
	for raw, value := range changes.Env.Set {
		key := strings.TrimSpace(raw)
		if key == "" || len(key) > 128 || value == "" {
			return nil, nil, nil, nil, false, errors.New("MCP environment names and secret values cannot be empty")
		}
		if _, exists := envSet[key]; exists {
			return nil, nil, nil, nil, false, errors.New("duplicate MCP environment name")
		}
		envSet[key] = value
	}
	envRemove, err := normalizeSecretRemovals(changes.Env.Remove, func(value string) (string, error) {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 {
			return "", errors.New("invalid MCP environment name")
		}
		return value, nil
	})
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	headerSet := map[string]string{}
	for raw, value := range changes.Headers.Set {
		key, err := protocol.NormalizeHeaderName(raw)
		if err != nil || value == "" {
			return nil, nil, nil, nil, false, errors.New("MCP header names and secret values must be valid and non-empty")
		}
		if _, exists := headerSet[key]; exists {
			return nil, nil, nil, nil, false, errors.New("duplicate MCP HTTP header name")
		}
		headerSet[key] = value
	}
	headerRemove, err := normalizeSecretRemovals(changes.Headers.Remove, protocol.NormalizeHeaderName)
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	for _, key := range envRemove {
		if _, exists := envSet[key]; exists {
			return nil, nil, nil, nil, false, errors.New("an MCP environment key cannot be both set and removed")
		}
	}
	for _, key := range headerRemove {
		if _, exists := headerSet[key]; exists {
			return nil, nil, nil, nil, false, errors.New("an MCP header key cannot be both set and removed")
		}
	}
	changed := len(envSet)+len(envRemove)+len(headerSet)+len(headerRemove) > 0
	return envSet, envRemove, headerSet, headerRemove, changed, nil
}

func normalizeSecretRemovals(values []string, normalize func(string) (string, error)) ([]string, error) {
	set := map[string]struct{}{}
	for _, value := range values {
		normalized, err := normalize(value)
		if err != nil {
			return nil, err
		}
		set[normalized] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func mcpServerAffectedTargetsTx(ctx context.Context, tx pgx.Tx, serverID string) ([]MCPAffectedTarget, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT deployment.node_id::text,node.name,deployment.runtime_kind
		FROM mcp_profile_servers member JOIN mcp_deployments deployment ON deployment.profile_id=member.profile_id
		JOIN nodes node ON node.id=deployment.node_id
		WHERE member.server_id=$1 AND deployment.desired_enabled AND deployment.state<>'observed'
		AND deployment.runtime_kind IN ('codex','claude') AND node.archived_at IS NULL
		ORDER BY node.name,deployment.runtime_kind`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []MCPAffectedTarget
	for rows.Next() {
		var target MCPAffectedTarget
		if err := rows.Scan(&target.NodeID, &target.NodeName, &target.Runtime); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
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
	managed, err := mcpServerInActiveToolHubProfileTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if managed {
		return ErrTargetManagedByProfile
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

// SetMCPServerArchived hides or restores an MCP server without deleting its
// provenance. Active ToolHub Profiles must be deactivated first so a restore
// cannot add remote secrets without a fresh activation preflight.
func (s *Store) SetMCPServerArchived(ctx context.Context, id string, archived bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentlyArchived bool
	if err := tx.QueryRow(ctx, "SELECT archived_at IS NOT NULL FROM mcp_servers WHERE id=$1 FOR UPDATE", id).Scan(&currentlyArchived); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if currentlyArchived == archived {
		return tx.Commit(ctx)
	}
	managed, err := mcpServerInActiveToolHubProfileTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if managed {
		return ErrTargetManagedByProfile
	}
	profileIDs, err := profileIDsForServer(ctx, tx, id)
	if err != nil {
		return err
	}
	type deployment struct {
		id, nodeID, runtime, profileID string
		enabled                        bool
	}
	deployments := []deployment{}
	if len(profileIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT id::text,node_id::text,runtime_kind,profile_id::text,desired_enabled
			FROM mcp_deployments WHERE profile_id=ANY($1::uuid[]) FOR UPDATE`, profileIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item deployment
			if err := rows.Scan(&item.id, &item.nodeID, &item.runtime, &item.profileID, &item.enabled); err != nil {
				rows.Close()
				return err
			}
			deployments = append(deployments, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_servers SET archived_at=CASE WHEN $2 THEN now() ELSE NULL END,updated_at=now() WHERE id=$1`, id, archived); err != nil {
		return err
	}
	if err := refreshProfileDeployments(ctx, tx, profileIDs); err != nil {
		return err
	}
	for _, item := range deployments {
		if err := s.upsertMCPDeploymentBindingsTx(ctx, tx, item.nodeID, item.runtime, item.profileID, item.id, item.enabled); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func mcpServerInActiveToolHubProfileTx(ctx context.Context, tx pgx.Tx, serverID string) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM toolhub_profile_mcp_servers members
		JOIN toolhub_profile_activations activations ON activations.profile_id=members.profile_id
		WHERE members.server_id=$1 AND activations.state IN ('pending','active','partial','failed'))`, serverID).Scan(&active)
	return active, err
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

// SetMCPProfileServers replaces the membership of one fixed ToolHub-managed
// runtime profile. Membership is desired state: existing deployments receive a
// new hash/generation and refreshed bindings, while an unapproved observed
// deployment stays observed until SetMCPDeployments explicitly activates it.
func (s *Store) SetMCPProfileServers(ctx context.Context, profileID string, serverIDs []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	managedRuntime, err := managedMCPProfileRuntimeTx(ctx, tx, profileID, true)
	if err != nil {
		return err
	}
	var managedTarget bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_deployments d
		JOIN toolhub_profile_activations a ON a.node_id=d.node_id AND a.runtime_kind=d.runtime_kind
		WHERE d.profile_id=$1 AND a.state IN ('pending','active','partial','failed'))`, profileID).Scan(&managedTarget); err != nil {
		return err
	}
	if managedTarget {
		return ErrTargetManagedByProfile
	}
	unique := make([]string, 0, len(serverIDs))
	seen := make(map[string]struct{}, len(serverIDs))
	for _, serverID := range serverIDs {
		serverID = strings.TrimSpace(serverID)
		if serverID == "" {
			return errors.New("MCP server IDs cannot be empty")
		}
		if _, exists := seen[serverID]; exists {
			continue
		}
		seen[serverID] = struct{}{}
		var authority string
		if err := tx.QueryRow(ctx, "SELECT authority FROM mcp_servers WHERE id=$1 FOR KEY SHARE", serverID).Scan(&authority); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		} else if authority == "shared-file" {
			return ErrSourceFileAuthoritative
		}
		unique = append(unique, serverID)
	}
	sort.Strings(unique)
	if _, err := tx.Exec(ctx, "DELETE FROM mcp_profile_servers WHERE profile_id=$1", profileID); err != nil {
		return err
	}
	for _, serverID := range unique {
		if _, err := tx.Exec(ctx, "INSERT INTO mcp_profile_servers(profile_id,server_id) VALUES($1,$2)", profileID, serverID); err != nil {
			return err
		}
	}
	type deployment struct {
		id, nodeID, runtime string
		enabled             bool
	}
	rows, err := tx.Query(ctx, `SELECT id::text,node_id::text,runtime_kind,desired_enabled
		FROM mcp_deployments WHERE profile_id=$1 FOR UPDATE`, profileID)
	if err != nil {
		return err
	}
	var deployments []deployment
	for rows.Next() {
		var item deployment
		if err := rows.Scan(&item.id, &item.nodeID, &item.runtime, &item.enabled); err != nil {
			rows.Close()
			return err
		}
		if item.runtime != managedRuntime {
			rows.Close()
			return ErrMCPProfileRuntime
		}
		deployments = append(deployments, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if err := refreshProfileDeployments(ctx, tx, []string{profileID}); err != nil {
		return err
	}
	for _, item := range deployments {
		if err := s.upsertMCPDeploymentBindingsTx(ctx, tx, item.nodeID, item.runtime, profileID, item.id, item.enabled); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type MCPDeploymentTarget struct {
	NodeID  string `json:"nodeId"`
	Runtime string `json:"runtime"`
	Enabled bool   `json:"enabled"`
}

func (s *Store) SetMCPDeployments(ctx context.Context, profileID, actor string, targets []MCPDeploymentTarget, dryRun bool) (domain.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	managedRuntime, err := managedMCPProfileRuntimeTx(ctx, tx, profileID, true)
	if err != nil {
		return domain.Job{}, err
	}
	deploymentIDs := make([]string, 0, len(targets))
	nodeIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.Runtime != domain.RuntimeCodex && target.Runtime != domain.RuntimeClaude {
			return domain.Job{}, errors.New("managed MCP delivery supports only Codex and Claude")
		}
		if target.Runtime != managedRuntime {
			return domain.Job{}, ErrMCPProfileRuntime
		}
		if managed, err := targetManagedByProfileTx(ctx, tx, target.NodeID, target.Runtime); err != nil {
			return domain.Job{}, err
		} else if managed {
			return domain.Job{}, ErrTargetManagedByProfile
		}
		deploymentID, err := s.upsertManagedMCPDeploymentTx(ctx, tx, profileID, target.NodeID, target.Runtime, target.Enabled)
		if err != nil {
			return domain.Job{}, err
		}
		deploymentIDs = append(deploymentIDs, deploymentID)
		nodeIDs = append(nodeIDs, target.NodeID)
	}
	job, err := s.enqueueJobTx(ctx, tx, "mcp_sync", map[string]any{"nodeIds": nodeIDs, "profileIds": []string{profileID}, "deploymentIds": deploymentIDs, "manual": true}, dryRun, actor)
	if err != nil {
		return domain.Job{}, err
	}
	return job, tx.Commit(ctx)
}

func (s *Store) upsertManagedMCPDeploymentTx(ctx context.Context, tx pgx.Tx, profileID, nodeID, runtimeKind string, enabled bool) (string, error) {
	if !domain.IsManagedMCPRuntime(runtimeKind) {
		return "", ErrHermesReadOnly
	}
	desiredHash, err := deploymentProfileHashTx(ctx, tx, nodeID, runtimeKind, profileID)
	if err != nil {
		return "", err
	}
	var deploymentID string
	err = tx.QueryRow(ctx, `INSERT INTO mcp_deployments(id,profile_id,node_id,runtime_kind,desired_enabled,desired_hash,desired_generation,state)
		VALUES($1,$2,$3,$4,$5,$6,1,'pending') ON CONFLICT(profile_id,node_id,runtime_kind)
		DO UPDATE SET
			desired_enabled=excluded.desired_enabled,
			desired_hash=excluded.desired_hash,
			desired_generation=CASE WHEN mcp_deployments.desired_enabled IS DISTINCT FROM excluded.desired_enabled OR mcp_deployments.desired_hash IS DISTINCT FROM excluded.desired_hash OR mcp_deployments.state='observed' THEN mcp_deployments.desired_generation + 1 ELSE mcp_deployments.desired_generation END,
			state=CASE WHEN mcp_deployments.desired_enabled IS DISTINCT FROM excluded.desired_enabled OR mcp_deployments.desired_hash IS DISTINCT FROM excluded.desired_hash OR mcp_deployments.state='observed' THEN 'pending' ELSE mcp_deployments.state END,
			updated_at=now()
		RETURNING id::text`, uuid.NewString(), profileID, nodeID, runtimeKind, enabled, desiredHash).Scan(&deploymentID)
	if err != nil {
		return "", err
	}
	if err := s.upsertMCPDeploymentBindingsTx(ctx, tx, nodeID, runtimeKind, profileID, deploymentID, enabled); err != nil {
		return "", err
	}
	return deploymentID, nil
}

func (s *Store) upsertMCPDeploymentBindingsTx(ctx context.Context, tx pgx.Tx, nodeID, runtimeKind, profileID, deploymentID string, deploymentEnabled bool) error {
	if !domain.IsManagedMCPRuntime(runtimeKind) {
		return ErrHermesReadOnly
	}
	serverIDs, err := effectiveMCPServerIDsTx(ctx, tx, nodeID, runtimeKind, profileID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET desired_enabled=false,
		drift=CASE WHEN missing THEN false ELSE true END,updated_at=now()
		WHERE node_id=$1 AND runtime_kind=$2 AND profile_id=$3
		AND NOT (server_id=ANY($4::uuid[]))`, nodeID, runtimeKind, profileID, serverIDs); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text,s.runtime_name,s.config_fingerprint,s.env_refs,s.header_refs,s.enabled
		FROM mcp_servers s WHERE s.id=ANY($1::uuid[]) AND s.enabled AND s.authority='toolhub' AND s.archived_at IS NULL
		ORDER BY s.runtime_name,s.id`, serverIDs)
	if err != nil {
		return err
	}
	type member struct {
		id, name, configFingerprint string
		envRefs, headerRefs         map[string]string
		enabled                     bool
	}
	var members []member
	for rows.Next() {
		var item member
		var envJSON, headerJSON []byte
		if err := rows.Scan(&item.id, &item.name, &item.configFingerprint, &envJSON, &headerJSON, &item.enabled); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal(envJSON, &item.envRefs) != nil || json.Unmarshal(headerJSON, &item.headerRefs) != nil {
			rows.Close()
			return errors.New("MCP server secret references are invalid")
		}
		members = append(members, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(members) == 0 {
		return nil
	}
	key, err := agentTaskKeyTx(ctx, tx, nodeID, s.cipher)
	if err != nil {
		return err
	}
	for _, item := range members {
		secretFingerprint, err := s.mcpSecretFingerprintTx(ctx, tx, key, item.envRefs, item.headerRefs)
		if err != nil {
			return err
		}
		desiredEnabled := deploymentEnabled && item.enabled
		bindingID := uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO mcp_runtime_bindings(id,node_id,runtime_kind,server_name,identity,server_id,profile_id,deployment_id,env_keys,header_keys,
			desired_config_fingerprint,desired_secret_fingerprint,desired_fingerprint,desired_enabled,missing,drift,last_seen_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$11,$13,$13,$13,now())
			ON CONFLICT(node_id,runtime_kind,server_name) DO UPDATE SET server_id=excluded.server_id,profile_id=excluded.profile_id,
			deployment_id=excluded.deployment_id,identity=excluded.identity,env_keys=excluded.env_keys,header_keys=excluded.header_keys,
			desired_config_fingerprint=excluded.desired_config_fingerprint,desired_secret_fingerprint=excluded.desired_secret_fingerprint,
			desired_fingerprint=excluded.desired_fingerprint,desired_enabled=excluded.desired_enabled,shared_source_id=NULL,
			drift=CASE WHEN excluded.desired_enabled THEN mcp_runtime_bindings.missing OR
				mcp_runtime_bindings.observed_config_fingerprint<>excluded.desired_config_fingerprint OR
				mcp_runtime_bindings.observed_secret_fingerprint<>excluded.desired_secret_fingerprint
				ELSE NOT mcp_runtime_bindings.missing END,updated_at=now()`, bindingID, nodeID, runtimeKind, item.name,
			protocol.MCPIdentity(runtimeKind, item.name), item.id, profileID, deploymentID, jsonStringArray(mapKeys(item.envRefs)),
			jsonStringArray(mapKeys(item.headerRefs)), item.configFingerprint, secretFingerprint, desiredEnabled); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) mcpSecretFingerprintTx(ctx context.Context, tx pgx.Tx, key []byte, envRefs, headerRefs map[string]string) (string, error) {
	if len(envRefs) == 0 && len(headerRefs) == 0 {
		return "", nil
	}
	values := make(map[string]string, len(envRefs)+len(headerRefs))
	for name, secretID := range envRefs {
		var ciphertext []byte
		if err := tx.QueryRow(ctx, "SELECT ciphertext FROM encrypted_secrets WHERE id=$1", secretID).Scan(&ciphertext); err != nil {
			return "", err
		}
		plaintext, err := s.cipher.Decrypt(ciphertext, secretID)
		if err != nil {
			return "", err
		}
		values["env:"+name] = string(plaintext)
	}
	for name, secretID := range headerRefs {
		var ciphertext []byte
		if err := tx.QueryRow(ctx, "SELECT ciphertext FROM encrypted_secrets WHERE id=$1", secretID).Scan(&ciphertext); err != nil {
			return "", err
		}
		plaintext, err := s.cipher.Decrypt(ciphertext, secretID)
		if err != nil {
			return "", err
		}
		values["header:"+name] = string(plaintext)
	}
	return security.FingerprintSecretMap(key, values), nil
}

func (s *Store) MCPDeploymentPayload(ctx context.Context, deploymentID string) (string, protocol.ApplyMCPPayload, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", protocol.ApplyMCPPayload{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var nodeID string
	var payload protocol.ApplyMCPPayload
	err = tx.QueryRow(ctx, `SELECT d.node_id::text,d.runtime_kind,d.desired_generation,d.desired_hash,d.desired_enabled,p.id::text,p.name
		FROM mcp_deployments d JOIN mcp_profiles p ON p.id=d.profile_id
		WHERE d.id=$1 AND d.runtime_kind IN ('codex','claude') AND p.source='toolhub'
		AND p.name='toolhub-'||d.runtime_kind AND p.origin->>'managedRuntime'=d.runtime_kind`,
		deploymentID).Scan(&nodeID, &payload.Runtime, &payload.DesiredGeneration, &payload.DesiredHash, &payload.Enabled, &payload.ProfileID, &payload.ProfileName)
	if err != nil {
		return "", protocol.ApplyMCPPayload{}, err
	}
	payload.Servers = []protocol.MCPServerRef{}
	if payload.Enabled {
		serverIDs, err := effectiveMCPServerIDsTx(ctx, tx, nodeID, payload.Runtime, payload.ProfileID)
		if err != nil {
			return "", protocol.ApplyMCPPayload{}, err
		}
		if len(serverIDs) > 0 {
			var raw []byte
			if err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM (
				SELECT s.id::text AS id,s.runtime_name AS name,s.transport,s.command,s.args,s.url,
				s.env_refs AS "envRefs",s.header_refs AS "headerRefs",coalesce(ps.overrides,'{}'::jsonb) AS overrides
				FROM mcp_servers s LEFT JOIN mcp_profile_servers ps ON ps.profile_id=$1 AND ps.server_id=s.id
				WHERE s.id=ANY($2::uuid[]) AND s.enabled AND s.authority='toolhub' AND s.archived_at IS NULL
				ORDER BY s.runtime_name,s.id) x`, payload.ProfileID, serverIDs).Scan(&raw); err != nil {
				return "", protocol.ApplyMCPPayload{}, err
			}
			if err := json.Unmarshal(raw, &payload.Servers); err != nil {
				return "", protocol.ApplyMCPPayload{}, err
			}
		}
	}
	payload.DeploymentID = deploymentID
	payload.MCPMProfile = "toolhub-" + payload.Runtime
	return nodeID, payload, tx.Commit(ctx)
}

func managedMCPProfileRuntimeTx(ctx context.Context, tx pgx.Tx, profileID string, lock bool) (string, error) {
	query := `SELECT name,source,coalesce(origin->>'managedRuntime',''),enabled FROM mcp_profiles WHERE id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	var name, source, runtimeKind string
	var enabled bool
	if err := tx.QueryRow(ctx, query, profileID).Scan(&name, &source, &runtimeKind, &enabled); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	if !enabled || source != "toolhub" || (runtimeKind != domain.RuntimeCodex && runtimeKind != domain.RuntimeClaude) || name != "toolhub-"+runtimeKind {
		return "", ErrManagedMCPProfile
	}
	return runtimeKind, nil
}

func profileIDsForServer(ctx context.Context, tx pgx.Tx, serverID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT profile_id::text FROM mcp_profile_servers WHERE server_id=$1
		UNION SELECT mp.id::text FROM toolhub_profile_mcp_servers tpm
		JOIN toolhub_profile_activations a ON a.profile_id=tpm.profile_id AND a.state IN ('pending','active','partial')
		JOIN mcp_profiles mp ON mp.source='toolhub' AND mp.name='toolhub-'||a.runtime_kind
			AND mp.origin->>'managedRuntime'=a.runtime_kind
		WHERE tpm.server_id=$1`, serverID)
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
		rows, err := tx.Query(ctx, `SELECT id::text,node_id::text,runtime_kind FROM mcp_deployments
			WHERE profile_id=$1 FOR UPDATE`, profileID)
		if err != nil {
			return err
		}
		type deployment struct{ id, nodeID, runtime string }
		var deployments []deployment
		for rows.Next() {
			var item deployment
			if err := rows.Scan(&item.id, &item.nodeID, &item.runtime); err != nil {
				rows.Close()
				return err
			}
			deployments = append(deployments, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, item := range deployments {
			desiredHash, err := deploymentProfileHashTx(ctx, tx, item.nodeID, item.runtime, profileID)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE mcp_deployments SET desired_hash=$2,
				desired_generation=CASE WHEN desired_hash IS DISTINCT FROM $2 THEN desired_generation + 1 ELSE desired_generation END,
				state=CASE WHEN desired_hash IS DISTINCT FROM $2 AND state<>'observed' THEN 'pending' ELSE state END,
				updated_at=now() WHERE id=$1`, item.id, desiredHash); err != nil {
				return err
			}
		}
	}
	return nil
}

// effectiveMCPServerIDsTx resolves the server set delivered to one target. A
// target activation wins; without one, the fixed channel's membership is used.
func effectiveMCPServerIDsTx(ctx context.Context, tx pgx.Tx, nodeID, runtimeKind, fixedProfileID string) ([]string, error) {
	serverIDs, _, err := resolveEffectiveMCPServerIDsTx(ctx, tx, nodeID, runtimeKind, fixedProfileID)
	return serverIDs, err
}

func resolveEffectiveMCPServerIDsTx(ctx context.Context, tx pgx.Tx, nodeID, runtimeKind, fixedProfileID string) ([]string, bool, error) {
	var activeProfileID string
	err := tx.QueryRow(ctx, `SELECT a.profile_id::text FROM toolhub_profile_activations a
		JOIN mcp_profiles p ON p.id=$3 AND p.enabled AND p.source='toolhub'
			AND p.name='toolhub-'||$2 AND p.origin->>'managedRuntime'=$2
		WHERE a.node_id=$1 AND a.runtime_kind=$2 AND a.state IN ('pending','active','partial')`,
		nodeID, runtimeKind, fixedProfileID).Scan(&activeProfileID)
	activated := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	query := `SELECT s.id::text FROM mcp_profile_servers members JOIN mcp_servers s ON s.id=members.server_id
		WHERE members.profile_id=$1 AND s.enabled AND s.authority='toolhub' AND s.archived_at IS NULL
		ORDER BY s.runtime_name,s.id`
	profileID := fixedProfileID
	if activated {
		query = `SELECT s.id::text FROM toolhub_profile_mcp_servers members JOIN mcp_servers s ON s.id=members.server_id
			WHERE members.profile_id=$1 AND s.enabled AND s.authority='toolhub' AND s.archived_at IS NULL
			ORDER BY s.runtime_name,s.id`
		profileID = activeProfileID
	}
	rows, err := tx.Query(ctx, query, profileID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	serverIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		serverIDs = append(serverIDs, id)
	}
	return serverIDs, activated, rows.Err()
}

func deploymentProfileHashTx(ctx context.Context, tx pgx.Tx, nodeID, runtimeKind, fixedProfileID string) (string, error) {
	serverIDs, activated, err := resolveEffectiveMCPServerIDsTx(ctx, tx, nodeID, runtimeKind, fixedProfileID)
	if err != nil {
		return "", err
	}
	if !activated {
		return profileHashTx(ctx, tx, fixedProfileID)
	}
	var profile []byte
	err = tx.QueryRow(ctx, `SELECT to_jsonb(q) FROM (SELECT p.id::text AS id,p.name,p.description,
		coalesce((SELECT jsonb_agg(to_jsonb(x)) FROM (
			SELECT s.id::text AS id,s.runtime_name AS name,s.transport,s.command,s.args,s.url,
			s.env_refs AS "envRefs",s.header_refs AS "headerRefs",coalesce(ps.overrides,'{}'::jsonb) AS overrides
			FROM mcp_servers s LEFT JOIN mcp_profile_servers ps ON ps.profile_id=p.id AND ps.server_id=s.id
			WHERE s.id=ANY($2::uuid[]) AND s.enabled AND s.authority='toolhub' AND s.archived_at IS NULL
			ORDER BY s.runtime_name,s.id) x),'[]'::jsonb) AS servers
		FROM mcp_profiles p WHERE p.id=$1 AND p.enabled) q`, fixedProfileID, serverIDs).Scan(&profile)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(security.TokenHash(string(profile))), nil
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
