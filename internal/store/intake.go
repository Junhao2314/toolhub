package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/security"
)

func (s *Store) CreateLocalMCPImportConfirmation(ctx context.Context, targetID, targetRevision string, preview bridgeprotocol.LocalMCPServerPreview, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 || ttl > 10*time.Minute || uuid.Validate(targetID) != nil || !bridgeprotocol.IsSHA256(targetRevision) || !bridgeprotocol.IsSHA256(preview.ContentHash) {
		return "", time.Time{}, errors.New("local MCP import confirmation is invalid")
	}
	target, err := s.Target(ctx, targetID)
	if err != nil {
		return "", time.Time{}, err
	}
	if target.NodeKind != bridgeprotocol.NodeKindLocal || (target.Runtime != bridgeprotocol.RuntimeClaude && target.Runtime != bridgeprotocol.RuntimeCodex) {
		return "", time.Time{}, errors.New("local MCP import source must be local Claude or Codex")
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		return "", time.Time{}, err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().UTC().Add(ttl)
	_, err = s.pool.Exec(ctx, `INSERT INTO local_mcp_import_confirmations(token_hash,target_id,target_revision,server_name,content_hash,preview,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, security.TokenHash(token), targetID, targetRevision, preview.Name, preview.ContentHash, jsonText(encoded), expires)
	return token, expires, err
}

func (s *Store) CreateLocalMCPImportOperation(ctx context.Context, token, idempotencyKey string) (domain.Operation, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.Operation{}, ErrConflict
	}
	requestIdentity := map[string]any{"confirmationTokenHash": fmt.Sprintf("%x", security.TokenHash(token))}
	requestHash, err := operationRequestHash(requestIdentity, nil)
	if err != nil {
		return domain.Operation{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Operation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if idempotencyKey != "" {
		var existingID, existingHash string
		err := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM operations WHERE kind='mcp_import' AND idempotency_key=$1`, idempotencyKey).Scan(&existingID, &existingHash)
		if err == nil {
			if existingHash != requestHash {
				return domain.Operation{}, ErrIdempotencyConflict
			}
			_ = tx.Rollback(ctx)
			return s.Operation(ctx, existingID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return domain.Operation{}, err
		}
	}
	var targetID, targetRevision, serverName, contentHash string
	var previewJSON []byte
	err = tx.QueryRow(ctx, `SELECT target_id::text,target_revision,server_name,content_hash,preview FROM local_mcp_import_confirmations WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, security.TokenHash(token)).Scan(&targetID, &targetRevision, &serverName, &contentHash, &previewJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, ErrConflict
	}
	if err != nil {
		return domain.Operation{}, err
	}
	var preview bridgeprotocol.LocalMCPServerPreview
	if err := json.Unmarshal(previewJSON, &preview); err != nil || preview.Name != serverName || preview.ContentHash != contentHash {
		return domain.Operation{}, ErrConflict
	}
	operationID, operationTargetID := uuid.NewString(), uuid.NewString()
	metadata, _ := json.Marshal(map[string]any{"source": "local", "targetId": targetID, "serverName": serverName})
	request, _ := json.Marshal(map[string]any{"targetRevision": targetRevision, "serverName": serverName, "contentHash": contentHash, "preview": preview})
	if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,source_id,idempotency_key,request_hash,metadata) VALUES($1,'mcp_import','queued',$2,$3,$4,$5)`, operationID, targetID, nullableText(idempotencyKey), requestHash, jsonText(metadata)); err != nil {
		return domain.Operation{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,request) VALUES($1,$2,$3,$4)`, operationTargetID, operationID, targetID, jsonText(request)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Operation{}, ErrOperationActive
		}
		return domain.Operation{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE local_mcp_import_confirmations SET consumed_at=now() WHERE token_hash=$1`, security.TokenHash(token)); err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, err
	}
	return s.Operation(ctx, operationID)
}
