package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type secretExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (s *Store) CreateSecret(ctx context.Context, name, kind string, value []byte, metadata map[string]any, createdBy string) (string, error) {
	return s.createSecret(ctx, s.pool, name, kind, value, metadata, createdBy)
}

func (s *Store) createSecret(ctx context.Context, executor secretExecutor, name, kind string, value []byte, metadata map[string]any, createdBy string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || kind == "" || len(value) == 0 {
		return "", errors.New("secret name, kind, and value are required")
	}
	id := uuid.NewString()
	ciphertext, err := s.cipher.Encrypt(value, id)
	if err != nil {
		return "", err
	}
	encoded, _ := json.Marshal(metadata)
	var actor any
	if createdBy != "" {
		actor = createdBy
	}
	_, err = executor.Exec(ctx, `INSERT INTO encrypted_secrets(id,name,kind,ciphertext,metadata,created_by)
		VALUES($1,$2,$3,$4,$5,$6)`, id, name, kind, ciphertext, string(encoded), actor)
	return id, err
}

func (s *Store) SecretValue(ctx context.Context, id string) ([]byte, error) {
	var ciphertext []byte
	err := s.pool.QueryRow(ctx, "SELECT ciphertext FROM encrypted_secrets WHERE id=$1", id).Scan(&ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.cipher.Decrypt(ciphertext, id)
}

func (s *Store) AgentSecretValue(ctx context.Context, nodeID, id string) ([]byte, error) {
	var ciphertext []byte
	err := s.pool.QueryRow(ctx, `SELECT es.ciphertext FROM encrypted_secrets es WHERE es.id=$2 AND es.kind IN ('mcp-env','mcp-header') AND (
		EXISTS (
			SELECT 1 FROM mcp_deployments d JOIN mcp_profiles p ON p.id=d.profile_id
		JOIN mcp_servers ms ON ms.enabled AND ms.authority='toolhub' AND ms.archived_at IS NULL CROSS JOIN LATERAL (
			SELECT value FROM jsonb_each_text(ms.env_refs)
			UNION ALL
			SELECT value FROM jsonb_each_text(ms.header_refs)
		) ref
		WHERE d.node_id=$1 AND d.runtime_kind IN ('codex','claude') AND d.state<>'observed' AND d.desired_enabled
		AND p.source='toolhub' AND p.name='toolhub-'||d.runtime_kind AND p.origin->>'managedRuntime'=d.runtime_kind
		AND ref.value=$2::text AND (
			EXISTS (SELECT 1 FROM toolhub_profile_activations a
				JOIN toolhub_profile_mcp_servers members ON members.profile_id=a.profile_id AND members.server_id=ms.id
				WHERE a.node_id=d.node_id AND a.runtime_kind=d.runtime_kind AND a.state IN ('pending','active','partial'))
				OR (NOT EXISTS (SELECT 1 FROM toolhub_profile_activations a WHERE a.node_id=d.node_id
					AND a.runtime_kind=d.runtime_kind AND a.state IN ('pending','active','partial'))
					AND EXISTS (SELECT 1 FROM mcp_profile_servers members WHERE members.profile_id=d.profile_id AND members.server_id=ms.id))
			))
		OR EXISTS (
			SELECT 1 FROM node_tasks task
			CROSS JOIN LATERAL jsonb_array_elements(coalesce(task.payload->'servers','[]'::jsonb)) server
			CROSS JOIN LATERAL (
				SELECT value FROM jsonb_each_text(coalesce(server->'envRefs','{}'::jsonb))
				UNION ALL
				SELECT value FROM jsonb_each_text(coalesce(server->'headerRefs','{}'::jsonb))
			) task_ref
			WHERE task.node_id=$1 AND task.kind='apply_mcp' AND task.status IN ('pending','delivered','running')
				AND task.payload->>'runtime' IN ('codex','claude') AND task_ref.value=$2::text
		))`, nodeID, id).Scan(&ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.cipher.Decrypt(ciphertext, id)
}
