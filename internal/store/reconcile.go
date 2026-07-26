package store

import (
	"context"
	"encoding/json"
)

type SkillDeploymentTask struct {
	DeploymentID     string
	NodeID           string
	Runtime          string
	SharedSourceName string
	SkillSlug        string
	SkillID          string
	SourceID         string
	NodeGroup        string
	VersionID        string
	SHA256           string
	Enabled          bool
	DesiredGeneration int64
}

func (s *Store) PendingSkillDeployments(ctx context.Context) ([]SkillDeploymentTask, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.id::text,d.node_id::text,d.runtime_kind,coalesce(shared_source.name,''),s.slug,s.id::text,coalesce(s.source_id::text,''),coalesce(n.labels->>'group',''),d.desired_version_id::text,v.content_sha256,d.desired_enabled,d.desired_generation
		FROM deployments d JOIN skills s ON s.id=d.skill_id JOIN skill_versions v ON v.id=d.desired_version_id
		JOIN nodes n ON n.id=d.node_id
		LEFT JOIN LATERAL (SELECT ss.name FROM shared_sources ss WHERE ss.node_id=d.node_id AND ss.mode='managed' AND ss.status<>'missing' ORDER BY ss.name LIMIT 1) shared_source ON d.runtime_kind='shared'
		WHERE d.state IN ('pending','drift','failed','rolling_back') AND v.approved_at IS NOT NULL AND n.archived_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SkillDeploymentTask
	for rows.Next() {
		var item SkillDeploymentTask
		if err := rows.Scan(&item.DeploymentID, &item.NodeID, &item.Runtime, &item.SharedSourceName, &item.SkillSlug, &item.SkillID, &item.SourceID, &item.NodeGroup, &item.VersionID, &item.SHA256, &item.Enabled, &item.DesiredGeneration); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) PendingMCPDeploymentIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, "SELECT id::text FROM mcp_deployments WHERE state IN ('pending','drift','failed') ORDER BY updated_at")
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

type MCPDeploymentRef struct {
	DeploymentID string
	NodeID       string
	ProfileID    string
	NodeGroup    string
}

func (s *Store) PendingMCPDeployments(ctx context.Context) ([]MCPDeploymentRef, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.id::text,d.node_id::text,d.profile_id::text,coalesce(n.labels->>'group','')
		FROM mcp_deployments d JOIN nodes n ON n.id=d.node_id
		WHERE d.state IN ('pending','drift','failed') AND n.archived_at IS NULL ORDER BY d.updated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []MCPDeploymentRef
	for rows.Next() {
		var item MCPDeploymentRef
		if err := rows.Scan(&item.DeploymentID, &item.NodeID, &item.ProfileID, &item.NodeGroup); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type Schedule struct {
	Kind      string
	ScopeType string
	ScopeID   string
	Spec      string
	Timezone  string
}

func (s *Store) Schedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.pool.Query(ctx, `SELECT 'update_check',scope_type,scope_id,schedule,timezone FROM update_policies WHERE enabled
		UNION ALL SELECT 'sync',scope_type,scope_id,schedule,timezone FROM sync_policies WHERE enabled
		UNION ALL SELECT 'mcp_sync',scope_type,scope_id,schedule,timezone FROM sync_policies WHERE enabled AND scope_type IN ('global','node_group')
		UNION ALL SELECT 'shared_sync',scope_type,scope_id,schedule,timezone FROM sync_policies WHERE enabled AND scope_type IN ('global','node_group')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Schedule
	for rows.Next() {
		var schedule Schedule
		if err := rows.Scan(&schedule.Kind, &schedule.ScopeType, &schedule.ScopeID, &schedule.Spec, &schedule.Timezone); err != nil {
			return nil, err
		}
		result = append(result, schedule)
	}
	return result, rows.Err()
}

func taskResultHash(result json.RawMessage) string {
	var value struct {
		ActualHash string `json:"actualHash"`
	}
	_ = json.Unmarshal(result, &value)
	return value.ActualHash
}
