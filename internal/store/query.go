package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// jsonText keeps marshaled JSON from being encoded as bytea when pgx uses the
// simple query protocol. PostgreSQL can infer JSON/JSONB parameters from text.
func jsonText(encoded []byte) string {
	return string(encoded)
}

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
	var targets, unhealthy, skills, operations, mcp, alerts, audit int64
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM targets t JOIN nodes n ON n.id=t.node_id WHERE n.archived_at IS NULL),
		(SELECT count(*) FROM target_desired_snapshots WHERE health<>'healthy'),
		(SELECT count(*) FROM skills WHERE archived_at IS NULL),
		(SELECT count(*) FROM operations WHERE status IN ('queued','running')),
		(SELECT count(*) FROM mcp_servers),
		(SELECT count(*) FROM alerts WHERE acknowledged_at IS NULL),
		(SELECT count(*) FROM audit_events WHERE created_at>now()-interval '24 hours')`).Scan(&targets, &unhealthy, &skills, &operations, &mcp, &alerts, &audit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"targets": targets, "unhealthyTargets": unhealthy, "skills": skills, "activeOperations": operations, "mcpServers": mcp, "openAlerts": alerts, "auditEvents24h": audit}, nil
}
