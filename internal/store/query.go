package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *Store) JSONList(ctx context.Context, innerQuery string, args ...any) (json.RawMessage, error) {
	var raw []byte
	query := "SELECT COALESCE(jsonb_agg(to_jsonb(q)), '[]'::jsonb) FROM (" + innerQuery + ") q"
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&raw); err != nil {
		return nil, fmt.Errorf("query JSON list: %w", err)
	}
	return json.RawMessage(raw), nil
}

func (s *Store) JSONObject(ctx context.Context, innerQuery string, args ...any) (json.RawMessage, error) {
	var raw []byte
	query := "SELECT to_jsonb(q) FROM (" + innerQuery + ") q"
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func (s *Store) Overview(ctx context.Context) (map[string]any, error) {
	result := map[string]any{}
	row := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM nodes WHERE archived_at IS NULL),
		(SELECT count(*) FROM nodes WHERE status='online'),
		(SELECT count(*) FROM skills WHERE archived_at IS NULL),
		(SELECT count(*) FROM deployments WHERE state IN ('drift','conflict','failed')),
		(SELECT count(*) FROM jobs WHERE status IN ('pending','running')),
		(SELECT count(*) FROM updates WHERE status='available'),
		(SELECT count(*) FROM mcp_servers WHERE enabled),
		(SELECT count(*) FROM audit_events WHERE created_at>now()-interval '24 hours')`)
	var nodes, online, skills, attention, jobs, updates, mcp, audit int64
	if err := row.Scan(&nodes, &online, &skills, &attention, &jobs, &updates, &mcp, &audit); err != nil {
		return nil, err
	}
	result["nodes"] = nodes
	result["onlineNodes"] = online
	result["skills"] = skills
	result["needsAttention"] = attention
	result["activeJobs"] = jobs
	result["availableUpdates"] = updates
	result["enabledMCPServers"] = mcp
	result["auditEvents24h"] = audit
	return result, nil
}
