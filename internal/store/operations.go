package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
)

type CreateOperationInput struct {
	Kind           string
	SourceID       string
	IdempotencyKey string
	Request        any
	Metadata       map[string]any
	TargetIDs      []string
	TargetRequests map[string]any
}

var controlOperationKinds = []string{"skill_import", "update_check", "refresh", "backup_gc"}

func (s *Store) CreateOperation(ctx context.Context, input CreateOperationInput) (domain.Operation, error) {
	requestHash, err := operationRequestHash(input.Request, input.TargetRequests)
	if err != nil {
		return domain.Operation{}, err
	}
	metadataJSON, err := marshalJSONObject(input.Metadata)
	if err != nil {
		return domain.Operation{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Operation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if input.IdempotencyKey != "" {
		var existingID, existingHash string
		err := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM operations WHERE kind=$1 AND idempotency_key=$2`, input.Kind, input.IdempotencyKey).Scan(&existingID, &existingHash)
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
	id := uuid.NewString()
	var source any
	if uuid.Validate(input.SourceID) == nil {
		source = input.SourceID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,source_id,idempotency_key,request_hash,metadata) VALUES($1,$2,'queued',$3,$4,$5,$6)`, id, input.Kind, source, nullableText(input.IdempotencyKey), requestHash, jsonText(metadataJSON)); err != nil {
		return domain.Operation{}, err
	}
	for _, targetID := range uniqueIDs(input.TargetIDs) {
		requestJSON, err := marshalJSONObject(input.TargetRequests[targetID])
		if err != nil {
			return domain.Operation{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,request) VALUES($1,$2,$3,$4)`, uuid.NewString(), id, targetID, jsonText(requestJSON)); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return domain.Operation{}, ErrOperationActive
			}
			return domain.Operation{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, err
	}
	return s.Operation(ctx, id)
}

func operationRequestHash(request any, targetRequests map[string]any) (string, error) {
	hashInput := struct {
		Request        any            `json:"request"`
		TargetRequests map[string]any `json:"targetRequests,omitempty"`
	}{Request: request, TargetRequests: targetRequests}
	requestJSON, err := json.Marshal(hashInput)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(requestJSON)
	return hex.EncodeToString(sum[:]), nil
}

func marshalJSONObject(value any) ([]byte, error) {
	if value == nil {
		return []byte(`{}`), nil
	}
	if object, ok := value.(map[string]any); ok && object == nil {
		return []byte(`{}`), nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		if !json.Valid(raw) {
			return nil, errors.New("operation target request is invalid JSON")
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, errors.New("operation target request must be a JSON object")
		}
		if object == nil {
			return nil, errors.New("operation target request must be a JSON object")
		}
		return append([]byte(nil), raw...), nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil || object == nil {
		return nil, errors.New("operation target request must be a JSON object")
	}
	return encoded, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) Operation(ctx context.Context, id string) (domain.Operation, error) {
	var operation domain.Operation
	err := s.pool.QueryRow(ctx, `SELECT id::text,kind,status,coalesce(source_id::text,''),coalesce(idempotency_key,''),metadata,error_code,error_reason,cancel_requested,created_at,started_at,finished_at,updated_at FROM operations WHERE id=$1`, id).Scan(&operation.ID, &operation.Kind, &operation.Status, &operation.SourceID, &operation.IdempotencyKey, &operation.Metadata, &operation.ErrorCode, &operation.ErrorReason, &operation.CancelRequested, &operation.CreatedAt, &operation.StartedAt, &operation.FinishedAt, &operation.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, ErrNotFound
	}
	return operation, err
}

func (s *Store) ListOperations(ctx context.Context, limit int) (json.RawMessage, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	return s.JSONList(ctx, `SELECT o.id::text,o.kind,o.status,coalesce(o.source_id::text,'') AS "sourceId",o.metadata,o.error_code AS "errorCode",o.error_reason AS "errorReason",o.cancel_requested AS "cancelRequested",o.created_at AS "createdAt",o.started_at AS "startedAt",o.finished_at AS "finishedAt",o.updated_at AS "updatedAt",coalesce((SELECT jsonb_agg(jsonb_build_object('id',ot.id::text,'targetId',ot.target_id::text,'targetKey',t.target_key,'status',ot.status,'attempt',ot.attempt,'pendingRerun',ot.pending_rerun,'errorCode',ot.error_code,'errorReason',ot.error_reason) ORDER BY t.target_key) FROM operation_targets ot JOIN targets t ON t.id=ot.target_id WHERE ot.operation_id=o.id),'[]'::jsonb) AS targets FROM operations o ORDER BY o.created_at DESC LIMIT $1`, limit)
}

func (s *Store) OperationDetail(ctx context.Context, id string) (json.RawMessage, error) {
	return s.JSONObject(ctx, `SELECT o.id::text,o.kind,o.status,coalesce(o.source_id::text,'') AS "sourceId",o.metadata,o.error_code AS "errorCode",o.error_reason AS "errorReason",o.cancel_requested AS "cancelRequested",o.created_at AS "createdAt",o.started_at AS "startedAt",o.finished_at AS "finishedAt",o.updated_at AS "updatedAt",coalesce((SELECT jsonb_agg(jsonb_build_object('id',ot.id::text,'targetId',ot.target_id::text,'targetKey',t.target_key,'status',ot.status,'attempt',ot.attempt,'pendingRerun',ot.pending_rerun,'bridgeOperationId',ot.bridge_operation_id,'saltJid',ot.salt_jid,'result',ot.result,'errorCode',ot.error_code,'errorReason',ot.error_reason,'createdAt',ot.created_at,'startedAt',ot.started_at,'finishedAt',ot.finished_at,'updatedAt',ot.updated_at) ORDER BY t.target_key) FROM operation_targets ot JOIN targets t ON t.id=ot.target_id WHERE ot.operation_id=o.id),'[]'::jsonb) AS targets FROM operations o WHERE o.id=$1`, id)
}

type WorkItem struct {
	Operation       domain.Operation
	OperationTarget domain.OperationTarget
	Target          domain.Target
}

func (s *Store) ClaimControlOperation(ctx context.Context) (domain.Operation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Operation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `SELECT o.id::text FROM operations o WHERE o.status='queued' AND NOT o.cancel_requested AND o.kind=ANY($1::text[]) AND NOT EXISTS(SELECT 1 FROM operation_targets ot WHERE ot.operation_id=o.id) ORDER BY o.created_at FOR UPDATE OF o SKIP LOCKED LIMIT 1`, controlOperationKinds).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, ErrNotFound
	}
	if err != nil {
		return domain.Operation{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE operations SET status='running',started_at=coalesce(started_at,now()),updated_at=now() WHERE id=$1`, id); err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, err
	}
	return s.Operation(ctx, id)
}

func (s *Store) FinishControlOperation(ctx context.Context, operationID, status string, result any, apiErr *bridgeprotocol.APIError) error {
	if status != bridgeprotocol.OperationSucceeded && status != bridgeprotocol.OperationFailed && status != bridgeprotocol.OperationCancelled {
		return errors.New("control operation terminal status is invalid")
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	errorCode, errorReason := "", ""
	if apiErr != nil {
		errorCode, errorReason = apiErr.Code, truncate(apiErr.Message, 500)
	}
	command, err := s.pool.Exec(ctx, `UPDATE operations SET status=$2,metadata=metadata||jsonb_build_object('result',$3::jsonb),error_code=$4,error_reason=$5,finished_at=now(),updated_at=now() WHERE id=$1 AND status='running' AND NOT EXISTS(SELECT 1 FROM operation_targets WHERE operation_id=$1)`, operationID, status, jsonText(resultJSON), errorCode, errorReason)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) ClaimOperationTarget(ctx context.Context) (WorkItem, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WorkItem{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var item WorkItem
	err = tx.QueryRow(ctx, `SELECT ot.id::text,ot.operation_id::text,ot.target_id::text,ot.status,ot.attempt,ot.pending_rerun,ot.bridge_operation_id,ot.salt_jid,ot.request,ot.created_at,ot.updated_at FROM operation_targets ot JOIN operations o ON o.id=ot.operation_id WHERE ot.status='queued' AND NOT o.cancel_requested ORDER BY ot.created_at FOR UPDATE OF ot SKIP LOCKED LIMIT 1`).Scan(&item.OperationTarget.ID, &item.OperationTarget.OperationID, &item.OperationTarget.TargetID, &item.OperationTarget.Status, &item.OperationTarget.Attempt, &item.OperationTarget.PendingRerun, &item.OperationTarget.BridgeOperationID, &item.OperationTarget.SaltJID, &item.OperationTarget.Request, &item.OperationTarget.CreatedAt, &item.OperationTarget.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkItem{}, ErrNotFound
	}
	if err != nil {
		return WorkItem{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE operation_targets SET status='running',started_at=coalesce(started_at,now()),updated_at=now() WHERE id=$1`, item.OperationTarget.ID); err != nil {
		return WorkItem{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE operations SET status='running',started_at=coalesce(started_at,now()),updated_at=now() WHERE id=$1`, item.OperationTarget.OperationID); err != nil {
		return WorkItem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkItem{}, err
	}
	item.Operation, err = s.Operation(ctx, item.OperationTarget.OperationID)
	if err != nil {
		return WorkItem{}, err
	}
	item.Target, err = s.Target(ctx, item.OperationTarget.TargetID)
	if err != nil {
		return WorkItem{}, err
	}
	item.OperationTarget.Status = bridgeprotocol.OperationRunning
	return item, nil
}

func (s *Store) FinishOperationTarget(ctx context.Context, operationTargetID, status string, result any, apiErr *bridgeprotocol.APIError) error {
	if status != bridgeprotocol.OperationSucceeded && status != bridgeprotocol.OperationFailed && status != bridgeprotocol.OperationCancelled {
		return errors.New("operation target terminal status is invalid")
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	errorCode, errorReason := "", ""
	if apiErr != nil {
		errorCode, errorReason = apiErr.Code, truncate(apiErr.Message, 500)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var operationID string
	if err := tx.QueryRow(ctx, `UPDATE operation_targets SET status=$2,result=$3,error_code=$4,error_reason=$5,finished_at=now(),updated_at=now() WHERE id=$1 AND status='running' RETURNING operation_id::text`, operationTargetID, status, jsonText(resultJSON), errorCode, errorReason).Scan(&operationID); err != nil {
		return err
	}
	if err := recalculateOperation(ctx, tx, operationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func recalculateOperation(ctx context.Context, tx pgx.Tx, operationID string) error {
	var queued, running, succeeded, failed, cancelled int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='queued'),count(*) FILTER(WHERE status='running'),count(*) FILTER(WHERE status='succeeded'),count(*) FILTER(WHERE status='failed'),count(*) FILTER(WHERE status='cancelled') FROM operation_targets WHERE operation_id=$1`, operationID).Scan(&queued, &running, &succeeded, &failed, &cancelled); err != nil {
		return err
	}
	if queued > 0 || running > 0 {
		return nil
	}
	var cancelRequested bool
	if err := tx.QueryRow(ctx, `SELECT cancel_requested FROM operations WHERE id=$1`, operationID).Scan(&cancelRequested); err != nil {
		return err
	}
	status := bridgeprotocol.OperationSucceeded
	if queued+running+succeeded+failed+cancelled == 0 && cancelRequested {
		status = bridgeprotocol.OperationCancelled
	}
	if failed > 0 && succeeded > 0 {
		status = bridgeprotocol.OperationPartial
	} else if failed > 0 {
		status = bridgeprotocol.OperationFailed
	} else if cancelled > 0 && succeeded == 0 {
		status = bridgeprotocol.OperationCancelled
	} else if cancelled > 0 {
		status = bridgeprotocol.OperationPartial
	}
	_, err := tx.Exec(ctx, `UPDATE operations SET status=$2,finished_at=now(),updated_at=now() WHERE id=$1`, operationID, status)
	return err
}

func (s *Store) CancelOperation(ctx context.Context, operationID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM operations WHERE id=$1 FOR UPDATE`, operationID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if status != bridgeprotocol.OperationQueued && status != bridgeprotocol.OperationRunning {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE operations SET cancel_requested=true,updated_at=now() WHERE id=$1`, operationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE operation_targets SET status='cancelled',finished_at=now(),updated_at=now() WHERE operation_id=$1 AND status='queued'`, operationID); err != nil {
		return err
	}
	var targetCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM operation_targets WHERE operation_id=$1`, operationID).Scan(&targetCount); err != nil {
		return err
	}
	if targetCount == 0 {
		if status == bridgeprotocol.OperationQueued {
			if _, err := tx.Exec(ctx, `UPDATE operations SET status='cancelled',finished_at=now(),updated_at=now() WHERE id=$1`, operationID); err != nil {
				return err
			}
		}
	} else if err := recalculateOperation(ctx, tx, operationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) EnqueueReconciles(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT target_id::text FROM target_desired_snapshots ORDER BY target_id`)
	if err != nil {
		return 0, err
	}
	var targets []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, id)
	}
	rows.Close()
	created := 0
	for _, targetID := range targets {
		var activeID string
		err := tx.QueryRow(ctx, `SELECT id::text FROM operation_targets WHERE target_id=$1 AND status IN ('queued','running')`, targetID).Scan(&activeID)
		if err == nil {
			if _, err := tx.Exec(ctx, `UPDATE operation_targets SET pending_rerun=true,updated_at=now() WHERE id=$1`, activeID); err != nil {
				return 0, err
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
		operationID := uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,'reconcile','queued','{"scheduled":true}')`, operationID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id) VALUES($1,$2,$3)`, uuid.NewString(), operationID, targetID); err != nil {
			return 0, err
		}
		created++
	}
	return created, tx.Commit(ctx)
}

func (s *Store) EnqueuePendingRerun(ctx context.Context, operationTargetID string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var targetID string
	err = tx.QueryRow(ctx, `SELECT target_id::text FROM operation_targets WHERE id=$1 AND pending_rerun AND status IN ('succeeded','failed','cancelled') FOR UPDATE`, operationTargetID).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE operation_targets SET pending_rerun=false WHERE id=$1`, operationTargetID); err != nil {
		return false, err
	}
	operationID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,'reconcile','queued','{"coalesced":true}')`, operationID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id) VALUES($1,$2,$3)`, uuid.NewString(), operationID, targetID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

type RunningOperationTarget struct {
	ID                string
	BridgeOperationID string
	UpdatedAt         time.Time
}

func (s *Store) RunningOperationTargets(ctx context.Context) ([]RunningOperationTarget, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,bridge_operation_id,updated_at FROM operation_targets WHERE status='running' ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RunningOperationTarget{}
	for rows.Next() {
		var item RunningOperationTarget
		if err := rows.Scan(&item.ID, &item.BridgeOperationID, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) RequeueRunningControlOperations(ctx context.Context) (int64, error) {
	command, err := s.pool.Exec(ctx, `UPDATE operations SET status='queued',started_at=NULL,finished_at=NULL,error_code='',error_reason='',updated_at=now() WHERE status='running' AND kind=ANY($1::text[]) AND NOT EXISTS(SELECT 1 FROM operation_targets ot WHERE ot.operation_id=operations.id)`, controlOperationKinds)
	return command.RowsAffected(), err
}

func (s *Store) RequeueRunningOperationTarget(ctx context.Context, operationTargetID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var operationID string
	err = tx.QueryRow(ctx, `UPDATE operation_targets SET status='queued',attempt=attempt+1,result=NULL,error_code='',error_reason='',started_at=NULL,finished_at=NULL,updated_at=now() WHERE id=$1 AND status='running' RETURNING operation_id::text`, operationTargetID).Scan(&operationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE operations SET status=CASE WHEN EXISTS(SELECT 1 FROM operation_targets WHERE operation_id=$1 AND status='running') THEN 'running' ELSE 'queued' END,finished_at=NULL,error_code='',error_reason='',updated_at=now() WHERE id=$1`, operationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RetryFailedTargets(ctx context.Context, operationID, idempotencyKey string) (domain.Operation, error) {
	var originalKind string
	if err := s.pool.QueryRow(ctx, `SELECT kind FROM operations WHERE id=$1`, operationID).Scan(&originalKind); errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, ErrNotFound
	} else if err != nil {
		return domain.Operation{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT target_id::text FROM operation_targets WHERE operation_id=$1 AND status='failed' ORDER BY target_id`, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return domain.Operation{}, err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return domain.Operation{}, ErrNotFound
	}
	requests := map[string]any{}
	requestRows, err := s.pool.Query(ctx, `SELECT target_id::text,request FROM operation_targets WHERE operation_id=$1 AND status='failed'`, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	for requestRows.Next() {
		var targetID string
		var request json.RawMessage
		if err := requestRows.Scan(&targetID, &request); err != nil {
			requestRows.Close()
			return domain.Operation{}, err
		}
		requests[targetID] = request
	}
	requestRows.Close()
	return s.CreateOperation(ctx, CreateOperationInput{Kind: originalKind, SourceID: operationID, IdempotencyKey: idempotencyKey, Request: map[string]any{"retryOf": operationID, "targetIds": ids}, Metadata: map[string]any{"retryOf": operationID, "originalKind": originalKind}, TargetIDs: ids, TargetRequests: requests})
}

func (s *Store) SetBridgeOperationMetadata(ctx context.Context, operationTargetID, bridgeOperationID, saltJID string) error {
	command, err := s.pool.Exec(ctx, `UPDATE operation_targets SET bridge_operation_id=$2,salt_jid=$3,updated_at=now() WHERE id=$1 AND status='running'`, operationTargetID, bridgeOperationID, saltJID)
	if err == nil && command.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

func (s *Store) TouchOperationTarget(ctx context.Context, operationTargetID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE operation_targets SET updated_at=$2 WHERE id=$1 AND status='running'`, operationTargetID, time.Now().UTC())
	return err
}
