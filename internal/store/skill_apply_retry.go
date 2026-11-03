package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// retrySkillApplyTargets carries successful target evidence without executing
// it twice, while retaining fleet-wide publication ownership. Reject evidence
// superseded by a later operation rather than publish a stale successful target.
func (s *Store) retrySkillApplyTargets(ctx context.Context, operationID, key string) (domain.Operation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Operation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var source, status string
	var metadataJSON []byte
	if err := tx.QueryRow(ctx, `SELECT source_id::text,status,metadata FROM operations WHERE id=$1 FOR UPDATE`, operationID).Scan(&source, &status, &metadataJSON); err != nil {
		return domain.Operation{}, err
	}
	if status != "failed" && status != "partial" {
		return domain.Operation{}, ErrConflict
	}
	var metadata map[string]any
	if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
		return domain.Operation{}, err
	}
	delete(metadata, "governanceFinalizedAction")
	metadata["retryOf"] = operationID
	rows, err := tx.Query(ctx, `SELECT target_id::text,status,request,result FROM operation_targets WHERE operation_id=$1 ORDER BY target_id`, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	targets := []profileApplyRetryTarget{}
	ids := []string{}
	requests := map[string]any{}
	failed := 0
	for rows.Next() {
		var target profileApplyRetryTarget
		if err := rows.Scan(&target.targetID, &target.status, &target.request, &target.result); err != nil {
			rows.Close()
			return domain.Operation{}, err
		}
		targets = append(targets, target)
		ids = append(ids, target.targetID)
		requests[target.targetID] = target.request
		if target.status == "failed" || target.status == "partial" {
			failed++
		} else if target.status != "succeeded" {
			rows.Close()
			return domain.Operation{}, ErrConflict
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return domain.Operation{}, err
	}
	if failed == 0 {
		return domain.Operation{}, ErrConflict
	}
	if key != "" {
		var existingID, existingHash string
		expectedHash, hashErr := operationRequestHash(map[string]any{"retryOf": operationID, "targetIds": ids}, requests)
		if hashErr != nil {
			return domain.Operation{}, hashErr
		}
		err := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM operations WHERE kind='apply' AND idempotency_key=$1`, key).Scan(&existingID, &existingHash)
		if err == nil {
			if existingHash != expectedHash {
				return domain.Operation{}, ErrIdempotencyConflict
			}
			_ = tx.Rollback(ctx)
			return s.Operation(ctx, existingID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return domain.Operation{}, err
		}
	}
	if err := validatePublishedProfilePredecessorTx(ctx, tx, source, stringMetadata(metadataJSON, "expectedPublishedProfileRevisionId")); err != nil {
		return domain.Operation{}, err
	}
	if err := lockActiveTargets(ctx, tx, ids); err != nil {
		return domain.Operation{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT t.id FROM targets t WHERE t.id=ANY($1::uuid[]) ORDER BY t.id FOR UPDATE`, ids); err != nil {
		return domain.Operation{}, err
	}
	var superseded bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_targets newer JOIN operations n ON n.id=newer.operation_id JOIN operations original ON original.id=$1 WHERE newer.target_id=ANY($2::uuid[]) AND newer.operation_id<>original.id AND n.created_at>original.created_at AND newer.status IN ('running','succeeded','partial'))`, operationID, ids).Scan(&superseded); err != nil {
		return domain.Operation{}, err
	}
	if superseded {
		return domain.Operation{}, ErrConflict
	}
	result, err := s.createOperationTx(ctx, tx, CreateOperationInput{Kind: "apply", SourceID: source, IdempotencyKey: key, Request: map[string]any{"retryOf": operationID, "targetIds": ids}, Metadata: metadata, TargetIDs: ids, TargetRequests: requests}, true)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Operation{}, ErrOperationActive
		}
		return domain.Operation{}, err
	}
	if result.Replay {
		_ = tx.Rollback(ctx)
		return s.Operation(ctx, result.ID)
	}
	for _, target := range targets {
		if target.status == bridgeprotocol.OperationSucceeded {
			if _, err := tx.Exec(ctx, `UPDATE operation_targets SET status='succeeded',result=$3,finished_at=now() WHERE operation_id=$1 AND target_id=$2`, result.ID, target.targetID, jsonText(target.result)); err != nil {
				return domain.Operation{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, err
	}
	return s.Operation(ctx, result.ID)
}
