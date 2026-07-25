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
		VALUES($1,$2,$3,$4,$5,$6)`, id, name, kind, ciphertext, encoded, actor)
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
	err := s.pool.QueryRow(ctx, `SELECT es.ciphertext FROM encrypted_secrets es WHERE es.id=$2 AND es.kind='mcp-env' AND EXISTS (
		SELECT 1 FROM mcp_deployments d JOIN mcp_profile_servers ps ON ps.profile_id=d.profile_id
		JOIN mcp_servers ms ON ms.id=ps.server_id CROSS JOIN LATERAL jsonb_each_text(ms.env_refs) ref
		WHERE d.node_id=$1 AND ref.value=$2::text AND d.desired_enabled)`, nodeID, id).Scan(&ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.cipher.Decrypt(ciphertext, id)
}
