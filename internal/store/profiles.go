package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Junhao2314/toolhub/internal/domain"
	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
)

type ActivationIssue struct {
	Code   string `json:"code,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type ActivationPreflight struct {
	OK               bool              `json:"ok"`
	Errors           []ActivationIssue `json:"errors"`
	Skipped          []ActivationIssue `json:"skipped"`
	RemoteSecretKeys []string          `json:"remoteSecretKeys"`
	NodeIsLocal      bool              `json:"nodeIsLocal"`
	NodeName         string            `json:"nodeName"`
	fixedProfileID   string
}

type ActivationPreflightError struct {
	Preflight ActivationPreflight
}

func (e *ActivationPreflightError) Error() string {
	if len(e.Preflight.Errors) == 0 {
		return "profile activation preflight failed"
	}
	issue := e.Preflight.Errors[0]
	if issue.Detail != "" {
		return issue.Code + ": " + issue.Detail
	}
	return issue.Code
}

func (s *Store) ListToolHubProfiles(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT p.id::text AS id,p.name,p.description,p.created_at AS "createdAt",p.updated_at AS "updatedAt",
		(SELECT count(*) FROM toolhub_profile_mcp_servers m WHERE m.profile_id=p.id) AS "mcpServerCount",
		(SELECT count(*) FROM toolhub_profile_skills sk WHERE sk.profile_id=p.id) AS "skillCount",
		(SELECT count(*) FROM toolhub_profile_activations a WHERE a.profile_id=p.id) AS "activationCount"
		FROM toolhub_profiles p ORDER BY lower(p.name),p.id`)
}

func (s *Store) GetToolHubProfile(ctx context.Context, id string) (json.RawMessage, error) {
	return s.JSONObject(ctx, `SELECT p.id::text AS id,p.name,p.description,p.created_at AS "createdAt",p.updated_at AS "updatedAt",
		coalesce((SELECT array_agg(m.server_id::text ORDER BY m.server_id::text) FROM toolhub_profile_mcp_servers m WHERE m.profile_id=p.id),ARRAY[]::text[]) AS "mcpServerIds",
		coalesce((SELECT array_agg(sk.skill_id::text ORDER BY sk.skill_id::text) FROM toolhub_profile_skills sk WHERE sk.profile_id=p.id),ARRAY[]::text[]) AS "skillIds",
		coalesce((SELECT jsonb_agg(jsonb_build_object('id',a.id::text,'nodeId',a.node_id::text,'nodeName',n.name,
			'runtime',a.runtime_kind,'state',a.state,'lastError',a.last_error,'skipped',a.skipped,'activatedAt',a.activated_at)
			ORDER BY n.name,a.runtime_kind) FROM toolhub_profile_activations a JOIN nodes n ON n.id=a.node_id WHERE a.profile_id=p.id),'[]'::jsonb) AS activations
		FROM toolhub_profiles p WHERE p.id=$1`, id)
}

func (s *Store) CreateToolHubProfile(ctx context.Context, name, description, actor string) (string, error) {
	name, description, err := validateToolHubProfileText(name, description)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	if _, err := s.pool.Exec(ctx, `INSERT INTO toolhub_profiles(id,name,description,created_by)
		VALUES($1,$2,$3,$4)`, id, name, description, nullString(actor)); err != nil {
		return "", mapToolHubProfileWriteError(err)
	}
	return id, nil
}

func (s *Store) UpdateToolHubProfile(ctx context.Context, id, name, description string) error {
	name, description, err := validateToolHubProfileText(name, description)
	if err != nil {
		return err
	}
	command, err := s.pool.Exec(ctx, `UPDATE toolhub_profiles SET name=$2,description=$3,updated_at=now() WHERE id=$1`, id, name, description)
	if err != nil {
		return mapToolHubProfileWriteError(err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func validateToolHubProfileText(name, description string) (string, string, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" || len(name) > 100 {
		return "", "", fmt.Errorf("%w: profile name must contain 1-100 characters", ErrInvalidToolHubProfile)
	}
	if len(description) > 1000 {
		return "", "", fmt.Errorf("%w: profile description is too long", ErrInvalidToolHubProfile)
	}
	return name, description, nil
}

func mapToolHubProfileWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "toolhub_profiles_name") {
		return ErrToolHubProfileNameTaken
	}
	return err
}

func (s *Store) SetToolHubProfileMembers(ctx context.Context, id string, mcpServerIDs, skillIDs []string) error {
	mcpServerIDs, err := normalizedUUIDs(mcpServerIDs, 1000)
	if err != nil {
		return fmt.Errorf("%w: MCP server IDs: %v", ErrInvalidToolHubProfile, err)
	}
	skillIDs, err = normalizedUUIDs(skillIDs, 1000)
	if err != nil {
		return fmt.Errorf("%w: Skill IDs: %v", ErrInvalidToolHubProfile, err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, "SELECT id::text FROM toolhub_profiles WHERE id=$1 FOR UPDATE", id).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM toolhub_profile_activations WHERE profile_id=$1)", id).Scan(&active); err != nil {
		return err
	}
	if active {
		return ErrStateConflict
	}
	for _, serverID := range mcpServerIDs {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT enabled AND authority='toolhub' AND archived_at IS NULL FROM mcp_servers WHERE id=$1 FOR KEY SHARE`, serverID).Scan(&valid); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		} else if !valid {
			return fmt.Errorf("%w: profiles may include only enabled, unarchived ToolHub-authoritative MCP servers", ErrInvalidToolHubProfile)
		}
	}
	for _, skillID := range skillIDs {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT archived_at IS NULL FROM skills WHERE id=$1 FOR KEY SHARE`, skillID).Scan(&valid); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		} else if !valid {
			return fmt.Errorf("%w: archived Skills cannot be profile members", ErrInvalidToolHubProfile)
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM toolhub_profile_mcp_servers WHERE profile_id=$1", id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM toolhub_profile_skills WHERE profile_id=$1", id); err != nil {
		return err
	}
	for _, serverID := range mcpServerIDs {
		if _, err := tx.Exec(ctx, "INSERT INTO toolhub_profile_mcp_servers(profile_id,server_id) VALUES($1,$2)", id, serverID); err != nil {
			return err
		}
	}
	for _, skillID := range skillIDs {
		if _, err := tx.Exec(ctx, "INSERT INTO toolhub_profile_skills(profile_id,skill_id) VALUES($1,$2)", id, skillID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE toolhub_profiles SET updated_at=now() WHERE id=$1", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func normalizedUUIDs(values []string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, errors.New("too many IDs")
	}
	set := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, err := uuid.Parse(value); err != nil {
			return nil, errors.New("contains an invalid ID")
		}
		if _, exists := set[value]; exists {
			continue
		}
		set[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func (s *Store) DeleteToolHubProfile(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var activationCount int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM toolhub_profile_activations WHERE profile_id=p.id)
		FROM toolhub_profiles p WHERE p.id=$1 FOR UPDATE`, id).Scan(&activationCount); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if activationCount > 0 {
		return ErrStateConflict
	}
	if _, err := tx.Exec(ctx, "DELETE FROM toolhub_profiles WHERE id=$1", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) PreflightProfileActivation(ctx context.Context, profileID, nodeID, runtimeKind string) (ActivationPreflight, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ActivationPreflight{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := s.preflightProfileActivationTx(ctx, tx, profileID, nodeID, runtimeKind, false, false)
	if err != nil {
		return ActivationPreflight{}, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) preflightProfileActivationTx(ctx context.Context, tx pgx.Tx, profileID, nodeID, runtimeKind string, confirmSecrets, lock bool) (ActivationPreflight, error) {
	result := ActivationPreflight{Errors: []ActivationIssue{}, Skipped: []ActivationIssue{}, RemoteSecretKeys: []string{}}
	if !domain.IsConsumerRuntime(runtimeKind) {
		result.Errors = append(result.Errors, ActivationIssue{Code: "runtime_unavailable", Scope: "runtime", Detail: "Unsupported runtime"})
		return result, nil
	}
	profileQuery := "SELECT id::text FROM toolhub_profiles WHERE id=$1"
	if lock {
		profileQuery += " FOR UPDATE"
	}
	var lockedProfileID string
	if err := tx.QueryRow(ctx, profileQuery, profileID).Scan(&lockedProfileID); errors.Is(err, pgx.ErrNoRows) {
		return ActivationPreflight{}, ErrNotFound
	} else if err != nil {
		return ActivationPreflight{}, err
	}

	var status string
	var hasSSH, hasRuntime bool
	nodeQuery := `SELECT name,status,coalesce(labels->>'scope'='local',false),
		EXISTS(SELECT 1 FROM node_connections c WHERE c.node_id=nodes.id AND c.kind='ssh' AND c.enabled),
		EXISTS(SELECT 1 FROM runtimes r WHERE r.node_id=nodes.id AND r.kind=$2)
		FROM nodes WHERE id=$1 AND archived_at IS NULL`
	if lock {
		nodeQuery += " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, nodeQuery, nodeID, runtimeKind).Scan(&result.NodeName, &status, &result.NodeIsLocal, &hasSSH, &hasRuntime)
	if errors.Is(err, pgx.ErrNoRows) {
		result.Errors = append(result.Errors, ActivationIssue{Code: "node_not_found", Scope: "node", Detail: "Node does not exist or is archived"})
		result.OK = false
		return result, nil
	}
	if err != nil {
		return ActivationPreflight{}, err
	}
	if status != "online" && !hasSSH {
		result.Errors = append(result.Errors, ActivationIssue{Code: "node_offline", Scope: "node", Detail: "Node is offline and has no SSH fallback"})
	}
	if !hasRuntime {
		result.Errors = append(result.Errors, ActivationIssue{Code: "runtime_unavailable", Scope: "runtime", Detail: runtimeKind + " is not present in the latest inventory"})
	}

	var invalidSkills []string
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(s.slug ORDER BY s.slug) FILTER (
		WHERE s.review_status<>'approved' OR s.archived_at IS NOT NULL OR s.current_version_id IS NULL),ARRAY[]::text[])
		FROM toolhub_profile_skills members JOIN skills s ON s.id=members.skill_id WHERE members.profile_id=$1`, profileID).Scan(&invalidSkills); err != nil {
		return ActivationPreflight{}, err
	}
	if len(invalidSkills) > 0 {
		result.Errors = append(result.Errors, ActivationIssue{Code: "skill_not_approved", Scope: "skills", Detail: strings.Join(invalidSkills, ", ")})
	}

	var invalidServers []string
	var mcpCount int
	if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(array_agg(s.name ORDER BY s.name) FILTER (
		WHERE s.archived_at IS NOT NULL OR s.authority<>'toolhub' OR NOT s.enabled),ARRAY[]::text[])
		FROM toolhub_profile_mcp_servers members JOIN mcp_servers s ON s.id=members.server_id WHERE members.profile_id=$1`, profileID).Scan(&mcpCount, &invalidServers); err != nil {
		return ActivationPreflight{}, err
	}
	if len(invalidServers) > 0 {
		result.Errors = append(result.Errors, ActivationIssue{Code: "mcp_server_unavailable", Scope: "mcp", Detail: strings.Join(invalidServers, ", ")})
	}

	mcpProfile := runtimeadapter.MCPMProfileForRuntime(runtimeKind)
	if mcpProfile == "" {
		if mcpCount > 0 {
			reason := "mcp_unsupported_runtime"
			detail := "This runtime has no ToolHub MCP writer"
			if runtimeKind == domain.RuntimeGrok {
				reason = "mcp_follows_claude"
				detail = "MCP follows claude on this node"
			}
			result.Skipped = append(result.Skipped, ActivationIssue{Scope: "mcp", Reason: reason, Detail: detail})
		}
	} else {
		if mcpCount == 0 {
			result.Skipped = append(result.Skipped, ActivationIssue{Scope: "mcp", Reason: "empty_mcp_set", Detail: "The profile contains no MCP servers"})
		}
		err := tx.QueryRow(ctx, `SELECT id::text FROM mcp_profiles WHERE name=$1 AND source='toolhub' AND enabled
			AND origin->>'managedRuntime'=$2`, mcpProfile, runtimeKind).Scan(&result.fixedProfileID)
		if errors.Is(err, pgx.ErrNoRows) {
			result.Errors = append(result.Errors, ActivationIssue{Code: "managed_mcp_profile_missing", Scope: "mcp", Detail: "The fixed delivery profile is missing"})
		} else if err != nil {
			return ActivationPreflight{}, err
		} else if _, err := managedMCPProfileRuntimeTx(ctx, tx, result.fixedProfileID, lock); err != nil {
			result.Errors = append(result.Errors, ActivationIssue{Code: "managed_mcp_profile_missing", Scope: "mcp", Detail: "The fixed delivery profile is invalid"})
		}
	}

	if !result.NodeIsLocal && mcpCount > 0 && mcpProfile != "" {
		rows, err := tx.Query(ctx, `SELECT key FROM (
			SELECT jsonb_object_keys(s.env_refs) AS key FROM toolhub_profile_mcp_servers members JOIN mcp_servers s ON s.id=members.server_id WHERE members.profile_id=$1
			UNION SELECT jsonb_object_keys(s.header_refs) AS key FROM toolhub_profile_mcp_servers members JOIN mcp_servers s ON s.id=members.server_id WHERE members.profile_id=$1
		) keys ORDER BY key`, profileID)
		if err != nil {
			return ActivationPreflight{}, err
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return ActivationPreflight{}, err
			}
			result.RemoteSecretKeys = append(result.RemoteSecretKeys, key)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return ActivationPreflight{}, err
		}
		rows.Close()
		if len(result.RemoteSecretKeys) > 0 && !confirmSecrets {
			result.Errors = append(result.Errors, ActivationIssue{Code: "remote_secret_confirmation_required", Scope: "mcp", Detail: "Confirm the named secret keys before remote delivery"})
		}
	}
	result.OK = len(result.Errors) == 0
	return result, nil
}

func (s *Store) ActivateProfile(ctx context.Context, profileID, nodeID, runtimeKind, actor string, confirmSecrets bool) (domain.Job, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.Job{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "toolhub-profile-target:"+nodeID+":"+runtimeKind); err != nil {
		return domain.Job{}, err
	}
	preflight, err := s.preflightProfileActivationTx(ctx, tx, profileID, nodeID, runtimeKind, confirmSecrets, true)
	if err != nil {
		return domain.Job{}, err
	}
	if !preflight.OK {
		return domain.Job{}, &ActivationPreflightError{Preflight: preflight}
	}

	var currentProfileID string
	err = tx.QueryRow(ctx, `SELECT profile_id::text FROM toolhub_profile_activations
		WHERE node_id=$1 AND runtime_kind=$2 FOR UPDATE`, nodeID, runtimeKind).Scan(&currentProfileID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.Job{}, err
	}
	skippedJSON, _ := json.Marshal(preflight.Skipped)
	activationID := uuid.NewString()
	var previousProfileID string
	err = tx.QueryRow(ctx, `INSERT INTO toolhub_profile_activations(id,node_id,runtime_kind,profile_id,previous_profile_id,state,last_error,skipped,activated_by)
		VALUES($1,$2,$3,$4,$5,'pending','',$6,$7)
		ON CONFLICT(node_id,runtime_kind) DO UPDATE SET
			id=excluded.id,
			previous_profile_id=CASE WHEN toolhub_profile_activations.profile_id IS DISTINCT FROM excluded.profile_id
				THEN toolhub_profile_activations.profile_id ELSE toolhub_profile_activations.previous_profile_id END,
			profile_id=excluded.profile_id,state='pending',last_error='',skipped=excluded.skipped,
			activated_by=excluded.activated_by,activated_at=now(),updated_at=now()
		RETURNING id::text,coalesce(previous_profile_id::text,'')`, activationID, nodeID, runtimeKind, profileID, nullString(currentProfileID), string(skippedJSON), nullString(actor)).
		Scan(&activationID, &previousProfileID)
	if err != nil {
		return domain.Job{}, err
	}

	type skillMember struct{ id, versionID string }
	rows, err := tx.Query(ctx, `SELECT s.id::text,s.current_version_id::text FROM toolhub_profile_skills members
		JOIN skills s ON s.id=members.skill_id WHERE members.profile_id=$1 ORDER BY s.id`, profileID)
	if err != nil {
		return domain.Job{}, err
	}
	members := []skillMember{}
	skillIDs := []string{}
	deploymentIDs := []string{}
	for rows.Next() {
		var item skillMember
		if err := rows.Scan(&item.id, &item.versionID); err != nil {
			rows.Close()
			return domain.Job{}, err
		}
		members = append(members, item)
		skillIDs = append(skillIDs, item.id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.Job{}, err
	}
	rows.Close()
	for _, member := range members {
		deploymentID, err := upsertSkillDeploymentTx(ctx, tx, nodeID, runtimeKind, member.id, member.versionID, true)
		if err != nil {
			return domain.Job{}, err
		}
		deploymentIDs = append(deploymentIDs, deploymentID)
	}
	disabledRows, err := tx.Query(ctx, `UPDATE deployments SET desired_enabled=false,
		desired_generation=CASE WHEN desired_enabled THEN desired_generation+1 ELSE desired_generation END,
		state=CASE WHEN desired_enabled THEN 'pending' ELSE state END,updated_at=now()
		WHERE node_id=$1 AND runtime_kind=$2 AND NOT (skill_id=ANY($3::uuid[]))
		RETURNING id::text,skill_id::text`, nodeID, runtimeKind, skillIDs)
	if err != nil {
		return domain.Job{}, err
	}
	for disabledRows.Next() {
		var deploymentID, skillID string
		if err := disabledRows.Scan(&deploymentID, &skillID); err != nil {
			disabledRows.Close()
			return domain.Job{}, err
		}
		deploymentIDs = append(deploymentIDs, deploymentID)
		skillIDs = append(skillIDs, skillID)
	}
	if err := disabledRows.Err(); err != nil {
		disabledRows.Close()
		return domain.Job{}, err
	}
	disabledRows.Close()
	skillIDs = sortedUnique(skillIDs)
	deploymentIDs = sortedUnique(deploymentIDs)

	profileIDs := []string{}
	mcpDeploymentIDs := []string{}
	if preflight.fixedProfileID != "" {
		deploymentID, err := s.upsertManagedMCPDeploymentTx(ctx, tx, preflight.fixedProfileID, nodeID, runtimeKind, true)
		if err != nil {
			return domain.Job{}, err
		}
		profileIDs = append(profileIDs, preflight.fixedProfileID)
		mcpDeploymentIDs = append(mcpDeploymentIDs, deploymentID)
	}
	job, err := s.enqueueJobTxWithOptions(ctx, tx, "profile_activate", map[string]any{
		"activationId": activationID, "profileId": profileID, "nodeIds": []string{nodeID}, "runtime": runtimeKind,
		"skillIds": skillIDs, "skillDeploymentIds": deploymentIDs, "profileIds": profileIDs, "mcpDeploymentIds": mcpDeploymentIDs,
		"previousProfileId": previousProfileID, "skipped": preflight.Skipped, "remoteSecretKeys": preflight.RemoteSecretKeys,
	}, false, actor, JobOptions{MaxAttempts: 1})
	if err != nil {
		return domain.Job{}, err
	}
	return job, tx.Commit(ctx)
}

func sortedUnique(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (s *Store) DeactivateProfile(ctx context.Context, nodeID, runtimeKind, actor string) error {
	if !domain.IsConsumerRuntime(runtimeKind) {
		return errors.New("invalid runtime")
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM toolhub_profile_activations WHERE node_id=$1 AND runtime_kind=$2`, nodeID, runtimeKind)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetProfileActivationState(ctx context.Context, activationID, state, lastError string) error {
	if state != "active" && state != "partial" && state != "failed" {
		return errors.New("invalid profile activation state")
	}
	command, err := s.pool.Exec(ctx, `UPDATE toolhub_profile_activations SET state=$2,last_error=$3,updated_at=now() WHERE id=$1`, activationID, state, truncate(lastError, 4000))
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TargetView(ctx context.Context, nodeID, runtimeKind string) (json.RawMessage, error) {
	if !domain.IsConsumerRuntime(runtimeKind) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var node struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		IsLocal bool   `json:"isLocal"`
	}
	err = tx.QueryRow(ctx, `SELECT id::text,name,status,coalesce(labels->>'scope'='local',false) FROM nodes WHERE id=$1 AND archived_at IS NULL`, nodeID).
		Scan(&node.ID, &node.Name, &node.Status, &node.IsLocal)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	activation := any(nil)
	var activationRaw []byte
	err = tx.QueryRow(ctx, `SELECT to_jsonb(q) FROM (SELECT a.profile_id::text AS "profileId",p.name AS "profileName",
		coalesce(a.previous_profile_id::text,'') AS "previousProfileId",coalesce(previous.name,'') AS "previousProfileName",
		a.state,a.last_error AS "lastError",a.skipped,a.activated_at AS "activatedAt",coalesce(u.username,'') AS "activatedBy"
		FROM toolhub_profile_activations a JOIN toolhub_profiles p ON p.id=a.profile_id
		LEFT JOIN toolhub_profiles previous ON previous.id=a.previous_profile_id LEFT JOIN users u ON u.id=a.activated_by
		WHERE a.node_id=$1 AND a.runtime_kind=$2) q`, nodeID, runtimeKind).Scan(&activationRaw)
	if err == nil {
		activation = json.RawMessage(activationRaw)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	mcpRuntime := runtimeKind
	mcpNote := ""
	if runtimeKind == domain.RuntimeGrok {
		mcpRuntime = domain.RuntimeClaude
		mcpNote = "MCP follows claude on this node"
	} else if runtimeKind == domain.RuntimeHermes {
		mcpNote = "MCP is read-only from ~/.hermes/config.yaml"
	} else if runtimeKind == domain.RuntimeOpenClaw {
		mcpNote = "MCP is read-only from the OpenClaw mcporter configuration"
	}
	mcpmProfile := runtimeadapter.MCPMProfileForRuntime(mcpRuntime)
	mcpCapable := runtimeadapter.MCPMProfileForRuntime(runtimeKind) != ""
	serverIDs := []string{}
	fixedProfileID := ""
	if mcpmProfile != "" {
		_ = tx.QueryRow(ctx, `SELECT id::text FROM mcp_profiles WHERE name=$1 AND source='toolhub' AND enabled
			AND origin->>'managedRuntime'=$2`, mcpmProfile, mcpRuntime).Scan(&fixedProfileID)
		if fixedProfileID != "" {
			serverIDs, err = effectiveMCPServerIDsTx(ctx, tx, nodeID, mcpRuntime, fixedProfileID)
			if err != nil {
				return nil, err
			}
		}
	} else {
		rows, err := tx.Query(ctx, `SELECT DISTINCT server_id::text FROM mcp_runtime_bindings
			WHERE node_id=$1 AND runtime_kind=$2 AND server_id IS NOT NULL AND desired_enabled ORDER BY server_id::text`, nodeID, runtimeKind)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			serverIDs = append(serverIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	mcpServers := json.RawMessage(`[]`)
	if len(serverIDs) > 0 {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(q)),'[]'::jsonb) FROM (
			SELECT s.id::text AS id,s.name,s.runtime_name AS "runtimeName",s.transport,
			CASE WHEN s.transport='stdio' THEN s.command ELSE s.url END AS endpoint,s.enabled,s.source,
			coalesce(nullif(s.origin->>'importSourceName',''),nullif(s.origin->>'importSource',''),'') AS "originName",
			EXISTS(SELECT 1 FROM mcp_runtime_bindings b WHERE b.node_id=$1 AND b.runtime_kind=$2 AND b.server_id=s.id AND b.missing) AS missing,
			EXISTS(SELECT 1 FROM mcp_runtime_bindings b WHERE b.node_id=$1 AND b.runtime_kind=$2 AND b.server_id=s.id AND b.drift) AS drift
			FROM mcp_servers s WHERE s.id=ANY($3::uuid[]) ORDER BY s.name,s.id) q`, nodeID, mcpRuntime, serverIDs).Scan(&raw); err != nil {
			return nil, err
		}
		mcpServers = raw
	}
	mcpDeployment := map[string]any{"mcpmProfile": mcpmProfile, "deploymentId": "", "state": "unmanaged", "servers": mcpServers}
	if fixedProfileID != "" {
		var deploymentID, state string
		err := tx.QueryRow(ctx, `SELECT id::text,state FROM mcp_deployments WHERE profile_id=$1 AND node_id=$2 AND runtime_kind=$3`, fixedProfileID, nodeID, mcpRuntime).
			Scan(&deploymentID, &state)
		if err == nil {
			mcpDeployment["deploymentId"] = deploymentID
			mcpDeployment["state"] = state
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}

	var skillsRaw []byte
	if err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(q)),'[]'::jsonb) FROM (
		SELECT d.id::text AS "deploymentId",s.id::text AS "skillId",s.name,s.slug,d.desired_enabled AS "desiredEnabled",
		d.actual_enabled AS "actualEnabled",d.state,coalesce(d.desired_version_id::text,'') AS "desiredVersionId",
		coalesce(d.actual_version_id::text,'') AS "actualVersionId",coalesce(v.content_sha256,'') AS sha256,d.last_error AS "lastError"
		FROM deployments d JOIN skills s ON s.id=d.skill_id LEFT JOIN skill_versions v ON v.id=d.desired_version_id
		WHERE d.node_id=$1 AND d.runtime_kind=$2 ORDER BY s.name,s.id) q`, nodeID, runtimeKind).Scan(&skillsRaw); err != nil {
		return nil, err
	}
	var skillDrift int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM deployments WHERE node_id=$1 AND runtime_kind=$2
		AND (state<>'in_sync' OR desired_enabled IS DISTINCT FROM actual_enabled OR desired_version_id IS DISTINCT FROM actual_version_id)`, nodeID, runtimeKind).Scan(&skillDrift); err != nil {
		return nil, err
	}
	var mcpDrift int
	if state, _ := mcpDeployment["state"].(string); state != "in_sync" && state != "unmanaged" {
		mcpDrift = 1
	}
	var serverViews []struct {
		Missing bool `json:"missing"`
		Drift   bool `json:"drift"`
	}
	_ = json.Unmarshal(mcpServers, &serverViews)
	for _, server := range serverViews {
		if server.Missing || server.Drift {
			mcpDrift++
		}
	}
	result := map[string]any{
		"node": node, "runtime": runtimeKind,
		"capabilities": map[string]any{"skills": domain.IsSkillRuntime(runtimeKind), "mcp": mcpCapable, "mcpNote": mcpNote},
		"activation":   activation, "mcp": mcpDeployment, "skills": json.RawMessage(skillsRaw),
		"drift": map[string]int{"mcp": mcpDrift, "skills": skillDrift},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return encoded, tx.Commit(ctx)
}
