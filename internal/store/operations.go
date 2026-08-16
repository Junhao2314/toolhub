package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
)

type CreateOperationInput struct {
	Kind               string
	SourceID           string
	IdempotencyKey     string
	Request            any
	Metadata           map[string]any
	TargetIDs          []string
	TargetRequests     map[string]any
	TargetDependencies map[string]string
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var controlOperationKinds = []string{"skill_import", "update_check", "refresh", "backup_gc", "contract_observe", "relay_telemetry_pull"}

var coalescedControlOperationKinds = map[string]bool{"contract_observe": true, "relay_telemetry_pull": true}

var errCoalescedControlOperationRace = errors.New("coalesced control operation raced with another creator")

type createOperationTxResult struct {
	ID     string
	Replay bool
}

func (s *Store) CreateOperation(ctx context.Context, input CreateOperationInput) (domain.Operation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Operation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := s.createOperationTx(ctx, tx, input, false)
	if errors.Is(err, errCoalescedControlOperationRace) {
		_ = tx.Rollback(ctx)
		var existingID string
		if queryErr := s.pool.QueryRow(ctx, `SELECT id::text FROM operations WHERE kind=$1 AND status IN ('queued','running') ORDER BY created_at LIMIT 1`, input.Kind).Scan(&existingID); queryErr != nil {
			return domain.Operation{}, queryErr
		}
		return s.Operation(ctx, existingID)
	}
	if err != nil {
		return domain.Operation{}, err
	}
	if result.Replay {
		_ = tx.Rollback(ctx)
		return s.Operation(ctx, result.ID)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, err
	}
	return s.Operation(ctx, result.ID)
}

func (s *Store) createOperationTx(ctx context.Context, tx pgx.Tx, input CreateOperationInput, targetsLocked bool) (createOperationTxResult, error) {
	dependencyIdentity := make(map[string]any, len(input.TargetDependencies))
	for targetID, dependencyID := range input.TargetDependencies {
		dependencyIdentity[targetID] = dependencyID
	}
	requestHash, err := operationRequestHashWithDependencies(input.Request, input.TargetRequests, dependencyIdentity)
	if err != nil {
		return createOperationTxResult{}, err
	}
	metadataJSON, err := marshalJSONObject(input.Metadata)
	if err != nil {
		return createOperationTxResult{}, err
	}
	if input.IdempotencyKey != "" {
		var existingID, existingHash string
		err := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM operations WHERE kind=$1 AND idempotency_key=$2`, input.Kind, input.IdempotencyKey).Scan(&existingID, &existingHash)
		if err == nil {
			if existingHash != requestHash {
				return createOperationTxResult{}, ErrIdempotencyConflict
			}
			return createOperationTxResult{ID: existingID, Replay: true}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return createOperationTxResult{}, err
		}
	}
	if coalescedControlOperationKinds[input.Kind] && len(input.TargetIDs) == 0 {
		var existingID string
		err := tx.QueryRow(ctx, `SELECT id::text FROM operations WHERE kind=$1 AND status IN ('queued','running') ORDER BY created_at LIMIT 1 FOR UPDATE`, input.Kind).Scan(&existingID)
		if err == nil {
			return createOperationTxResult{ID: existingID, Replay: true}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return createOperationTxResult{}, err
		}
	}
	if !targetsLocked {
		if err := lockActiveTargets(ctx, tx, input.TargetIDs); err != nil {
			return createOperationTxResult{}, err
		}
	}
	dependencies := input.TargetDependencies
	for dependent, dependency := range dependencies {
		if !containsString(uniqueIDs(input.TargetIDs), dependent) || !containsString(uniqueIDs(input.TargetIDs), dependency) || dependent == dependency {
			return createOperationTxResult{}, ErrConflict
		}
	}
	id := uuid.NewString()
	var source any
	if uuid.Validate(input.SourceID) == nil {
		source = input.SourceID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,source_id,idempotency_key,request_hash,metadata) VALUES($1,$2,'queued',$3,$4,$5,$6)`, id, input.Kind, source, nullableText(input.IdempotencyKey), requestHash, jsonText(metadataJSON)); err != nil {
		var pgErr *pgconn.PgError
		if coalescedControlOperationKinds[input.Kind] && errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "operations_governance_control_one_active_idx" {
			return createOperationTxResult{}, errCoalescedControlOperationRace
		}
		return createOperationTxResult{}, err
	}
	targetRowIDs := make(map[string]string)
	for _, targetID := range uniqueIDs(input.TargetIDs) {
		targetRowIDs[targetID] = uuid.NewString()
	}
	for _, targetID := range uniqueIDs(input.TargetIDs) {
		requestJSON, err := marshalJSONObject(input.TargetRequests[targetID])
		if err != nil {
			return createOperationTxResult{}, err
		}
		var dependency any
		if dependencyID := targetRowIDs[dependencies[targetID]]; dependencyID != "" {
			dependency = dependencyID
		}
		governancePending := input.Kind == "relay_config_apply" || input.Kind == "policy_apply"
		if _, err := tx.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,depends_on_target_id,request,governance_finalization_pending) VALUES($1,$2,$3,$4,$5,$6)`, targetRowIDs[targetID], id, targetID, dependency, jsonText(requestJSON), governancePending); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return createOperationTxResult{}, ErrOperationActive
			}
			return createOperationTxResult{}, err
		}
	}
	return createOperationTxResult{ID: id}, nil
}

func lockActiveTargets(ctx context.Context, tx pgx.Tx, targetIDs []string) error {
	ids := uniqueIDs(targetIDs)
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if uuid.Validate(id) != nil {
			return ErrNotFound
		}
	}
	rows, err := tx.Query(ctx, `SELECT t.id::text FROM targets t JOIN nodes n ON n.id=t.node_id WHERE t.id=ANY($1::uuid[]) AND n.archived_at IS NULL ORDER BY n.id,t.id FOR SHARE OF n`, ids)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if count != len(ids) {
		return ErrNotFound
	}
	return nil
}

func operationRequestHash(request any, targetRequests map[string]any) (string, error) {
	return operationRequestHashWithDependencies(request, targetRequests, nil)
}

func operationRequestHashWithDependencies(request any, targetRequests, dependencies map[string]any) (string, error) {
	hashInput := struct {
		Request        any            `json:"request"`
		TargetRequests map[string]any `json:"targetRequests,omitempty"`
		Dependencies   map[string]any `json:"dependencies,omitempty"`
	}{Request: request, TargetRequests: targetRequests, Dependencies: dependencies}
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
	return s.JSONList(ctx, `SELECT o.id::text,o.kind,o.status,coalesce(o.source_id::text,'') AS "sourceId",o.metadata,o.error_code AS "errorCode",o.error_reason AS "errorReason",o.cancel_requested AS "cancelRequested",o.created_at AS "createdAt",o.started_at AS "startedAt",o.finished_at AS "finishedAt",o.updated_at AS "updatedAt",coalesce((SELECT jsonb_agg(jsonb_build_object('id',ot.id::text,'targetId',ot.target_id::text,'targetKey',t.target_key,'dependsOnTargetId',ot.depends_on_target_id,'status',ot.status,'attempt',ot.attempt,'pendingRerun',ot.pending_rerun,'errorCode',ot.error_code,'errorReason',ot.error_reason) ORDER BY t.target_key) FROM operation_targets ot JOIN targets t ON t.id=ot.target_id WHERE ot.operation_id=o.id),'[]'::jsonb) AS targets FROM operations o ORDER BY o.created_at DESC LIMIT $1`, limit)
}

func (s *Store) OperationDetail(ctx context.Context, id string) (json.RawMessage, error) {
	return s.JSONObject(ctx, `SELECT o.id::text,o.kind,o.status,coalesce(o.source_id::text,'') AS "sourceId",o.metadata,o.error_code AS "errorCode",o.error_reason AS "errorReason",o.cancel_requested AS "cancelRequested",o.created_at AS "createdAt",o.started_at AS "startedAt",o.finished_at AS "finishedAt",o.updated_at AS "updatedAt",coalesce((SELECT jsonb_agg(jsonb_build_object('id',ot.id::text,'targetId',ot.target_id::text,'targetKey',t.target_key,'dependsOnTargetId',ot.depends_on_target_id,'status',ot.status,'attempt',ot.attempt,'pendingRerun',ot.pending_rerun,'bridgeOperationId',ot.bridge_operation_id,'saltJid',ot.salt_jid,'result',ot.result,'errorCode',ot.error_code,'errorReason',ot.error_reason,'createdAt',ot.created_at,'startedAt',ot.started_at,'finishedAt',ot.finished_at,'updatedAt',ot.updated_at) ORDER BY t.target_key) FROM operation_targets ot JOIN targets t ON t.id=ot.target_id WHERE ot.operation_id=o.id),'[]'::jsonb) AS targets FROM operations o WHERE o.id=$1`, id)
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
		bounded := bridgeprotocol.BoundedAPIError(apiErr, bridgeprotocol.ErrInvalidRequest)
		errorCode, errorReason = bounded.Code, bounded.Message
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
	type candidate struct {
		operationID       string
		operationTargetID string
		nodeID            string
	}
	rows, err := tx.Query(ctx, `SELECT o.id::text,ot.id::text,n.id::text FROM operation_targets ot JOIN operations o ON o.id=ot.operation_id JOIN targets t ON t.id=ot.target_id JOIN nodes n ON n.id=t.node_id WHERE ot.status='queued' AND NOT o.cancel_requested AND (ot.depends_on_target_id IS NULL OR EXISTS(SELECT 1 FROM operation_targets dep WHERE dep.id=ot.depends_on_target_id AND dep.status='succeeded')) AND (n.archived_at IS NULL OR ot.bridge_operation_id<>'') ORDER BY o.id,ot.id,n.id LIMIT 100`)
	if err != nil {
		return WorkItem{}, err
	}
	candidates := []candidate{}
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.operationID, &item.operationTargetID, &item.nodeID); err != nil {
			rows.Close()
			return WorkItem{}, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return WorkItem{}, err
	}
	rows.Close()
	var item WorkItem
	claimed := false
	for _, candidate := range candidates {
		var operationID string
		err := tx.QueryRow(ctx, `SELECT id::text FROM operations WHERE id=$1 AND NOT cancel_requested FOR UPDATE SKIP LOCKED`, candidate.operationID).Scan(&operationID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return WorkItem{}, err
		}
		err = tx.QueryRow(ctx, `SELECT ot.id::text,ot.operation_id::text,ot.target_id::text,coalesce(ot.depends_on_target_id::text,''),ot.status,ot.attempt,ot.pending_rerun,ot.bridge_operation_id,ot.salt_jid,ot.request,ot.created_at,ot.updated_at FROM operation_targets ot JOIN operations o ON o.id=ot.operation_id JOIN targets t ON t.id=ot.target_id JOIN nodes n ON n.id=t.node_id WHERE ot.id=$1 AND ot.operation_id=$2 AND ot.status='queued' AND NOT o.cancel_requested AND (ot.depends_on_target_id IS NULL OR EXISTS(SELECT 1 FROM operation_targets dep WHERE dep.id=ot.depends_on_target_id AND dep.status='succeeded')) AND (n.archived_at IS NULL OR ot.bridge_operation_id<>'') FOR UPDATE OF ot SKIP LOCKED`, candidate.operationTargetID, operationID).Scan(&item.OperationTarget.ID, &item.OperationTarget.OperationID, &item.OperationTarget.TargetID, &item.OperationTarget.DependsOnTargetID, &item.OperationTarget.Status, &item.OperationTarget.Attempt, &item.OperationTarget.PendingRerun, &item.OperationTarget.BridgeOperationID, &item.OperationTarget.SaltJID, &item.OperationTarget.Request, &item.OperationTarget.CreatedAt, &item.OperationTarget.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
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
		claimed = true
		break
	}
	if !claimed {
		return WorkItem{}, ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkItem{}, err
	}
	item.Operation, err = s.Operation(ctx, item.OperationTarget.OperationID)
	if err != nil {
		return WorkItem{}, err
	}
	item.Target, err = s.historicalTarget(ctx, item.OperationTarget.TargetID)
	if err != nil {
		return WorkItem{}, err
	}
	item.OperationTarget.Status = bridgeprotocol.OperationRunning
	return item, nil
}

func (s *Store) FinishOperationTarget(ctx context.Context, operationTargetID, status string, result any, apiErr *bridgeprotocol.APIError) error {
	if status != bridgeprotocol.OperationSucceeded && status != bridgeprotocol.OperationPartial && status != bridgeprotocol.OperationFailed && status != bridgeprotocol.OperationCancelled {
		return errors.New("operation target terminal status is invalid")
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	errorCode, errorReason := "", ""
	if apiErr != nil {
		bounded := bridgeprotocol.BoundedAPIError(apiErr, bridgeprotocol.ErrInvalidRequest)
		errorCode, errorReason = bounded.Code, bounded.Message
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var operationID string
	if err := tx.QueryRow(ctx, `SELECT operation_id::text FROM operation_targets WHERE id=$1`, operationTargetID).Scan(&operationID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM operations WHERE id=$1 FOR UPDATE`, operationID).Scan(&operationID); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE operation_targets SET status=$2,result=$3,error_code=$4,error_reason=$5,finished_at=now(),updated_at=now() WHERE id=$1 AND status='running'`, operationTargetID, status, jsonText(resultJSON), errorCode, errorReason)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	if status != bridgeprotocol.OperationSucceeded {
		if _, err := tx.Exec(ctx, `UPDATE operation_targets SET status='failed',error_code='dependency_failed',error_reason='dependency target did not succeed',finished_at=now(),updated_at=now() WHERE depends_on_target_id=$1 AND status='queued'`, operationTargetID); err != nil {
			return err
		}
	}
	var terminalFailures int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM operation_targets WHERE operation_id=$1 AND status IN ('partial','failed','cancelled')`, operationID).Scan(&terminalFailures); err != nil {
		return err
	}
	if terminalFailures > 0 {
		if _, err := tx.Exec(ctx, `UPDATE operation_targets SET governance_finalization_pending=false,updated_at=now() WHERE operation_id=$1 AND governance_finalization_pending`, operationID); err != nil {
			return err
		}
	}
	if err := recalculateOperation(ctx, tx, operationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) FailGovernanceFinalization(ctx context.Context, operationID string, apiErr *bridgeprotocol.APIError) error {
	if uuid.Validate(operationID) != nil {
		return ErrNotFound
	}
	if apiErr == nil || strings.TrimSpace(apiErr.Code) == "" || strings.TrimSpace(apiErr.Message) == "" {
		return errors.New("governance finalization error is required")
	}
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
	if status != bridgeprotocol.OperationRunning {
		return ErrConflict
	}
	command, err := tx.Exec(ctx, `UPDATE operation_targets SET governance_finalization_pending=false,updated_at=now() WHERE operation_id=$1 AND governance_finalization_pending`, operationID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrConflict
	}
	bounded := bridgeprotocol.BoundedAPIError(apiErr, bridgeprotocol.ErrInvalidRequest)
	command, err = tx.Exec(ctx, `UPDATE operations SET status='failed',error_code=$2,error_reason=$3,finished_at=now(),updated_at=now() WHERE id=$1 AND status='running'`, operationID, bounded.Code, bounded.Message)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func recalculateOperation(ctx context.Context, tx pgx.Tx, operationID string) error {
	var queued, running, succeeded, partial, failed, cancelled, finalizationPending int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='queued'),count(*) FILTER(WHERE status='running'),count(*) FILTER(WHERE status='succeeded'),count(*) FILTER(WHERE status='partial'),count(*) FILTER(WHERE status='failed'),count(*) FILTER(WHERE status='cancelled'),count(*) FILTER(WHERE governance_finalization_pending) FROM operation_targets WHERE operation_id=$1`, operationID).Scan(&queued, &running, &succeeded, &partial, &failed, &cancelled, &finalizationPending); err != nil {
		return err
	}
	if queued > 0 || running > 0 || finalizationPending > 0 {
		return nil
	}
	var cancelRequested bool
	if err := tx.QueryRow(ctx, `SELECT cancel_requested FROM operations WHERE id=$1`, operationID).Scan(&cancelRequested); err != nil {
		return err
	}
	status := bridgeprotocol.OperationSucceeded
	if queued+running+succeeded+partial+failed+cancelled == 0 && cancelRequested {
		status = bridgeprotocol.OperationCancelled
	} else if partial > 0 || (failed > 0 && succeeded > 0) || (cancelled > 0 && succeeded > 0) {
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

// UpdateOperationTargetRequest narrows a retry payload after a batch has
// completed. The worker uses it to retain only failed/stale items so the
// existing failed-target retry endpoint is also a failed-item retry.
func (s *Store) UpdateOperationTargetRequest(ctx context.Context, operationTargetID string, request any) error {
	body, err := marshalJSONObject(request)
	if err != nil {
		return err
	}
	command, err := s.pool.Exec(ctx, `UPDATE operation_targets SET request=$2,updated_at=now() WHERE id=$1 AND status='running'`, operationTargetID, jsonText(body))
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
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
	cancelledTargets, err := tx.Exec(ctx, `UPDATE operation_targets SET status='cancelled',finished_at=now(),updated_at=now() WHERE operation_id=$1 AND status='queued'`, operationID)
	if err != nil {
		return err
	}
	if cancelledTargets.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, `UPDATE operation_targets SET governance_finalization_pending=false,updated_at=now() WHERE operation_id=$1 AND governance_finalization_pending`, operationID); err != nil {
			return err
		}
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
	rows, err := tx.Query(ctx, `SELECT ds.target_id::text,t.runtime,(t.runtime='shared-relay' AND (ds.health='blocked' OR ds.relay_last_member_check_at IS NULL OR ds.relay_last_member_check_at<=now()-interval '30 minutes')) AS full_relay_probe FROM target_desired_snapshots ds JOIN targets t ON t.id=ds.target_id JOIN nodes n ON n.id=t.node_id WHERE n.archived_at IS NULL AND (t.runtime<>'shared-relay' OR (NOT ds.relay_suspended AND (ds.health<>'blocked' OR ds.relay_next_retry_at IS NULL OR ds.relay_next_retry_at<=now()))) ORDER BY n.id,ds.target_id FOR SHARE OF n`)
	if err != nil {
		return 0, fmt.Errorf("list active reconcile targets: %w", err)
	}
	type reconcileTarget struct {
		id             string
		fullRelayProbe bool
	}
	var targets []reconcileTarget
	for rows.Next() {
		var target reconcileTarget
		var runtime string
		if err := rows.Scan(&target.id, &runtime, &target.fullRelayProbe); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	created := 0
	for _, target := range targets {
		var activeID string
		err := tx.QueryRow(ctx, `SELECT id::text FROM operation_targets WHERE target_id=$1 AND status IN ('queued','running') FOR UPDATE`, target.id).Scan(&activeID)
		if err == nil {
			if _, err := tx.Exec(ctx, `UPDATE operation_targets SET pending_rerun=true,updated_at=now() WHERE id=$1`, activeID); err != nil {
				return 0, err
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("lock active reconcile step: %w", err)
		}
		operationID := uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,'reconcile','queued','{"scheduled":true}')`, operationID); err != nil {
			return 0, fmt.Errorf("insert reconcile operation: %w", err)
		}
		request := `{}`
		if target.fullRelayProbe {
			request = `{"fullRelayProbe":true}`
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,request) VALUES($1,$2,$3,$4)`, uuid.NewString(), operationID, target.id, request); err != nil {
			return 0, fmt.Errorf("insert reconcile target: %w", err)
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
	var nodeID string
	var nodeActive bool
	err = tx.QueryRow(ctx, `SELECT n.id::text,n.archived_at IS NULL FROM operation_targets ot JOIN targets t ON t.id=ot.target_id JOIN nodes n ON n.id=t.node_id WHERE ot.id=$1 FOR SHARE OF n`, operationTargetID).Scan(&nodeID, &nodeActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var targetID string
	var eligible, fullRelayProbe bool
	err = tx.QueryRow(ctx, `SELECT ot.target_id::text,$2 AND (t.runtime<>'shared-relay' OR (NOT ds.relay_suspended AND (ds.health<>'blocked' OR ds.relay_next_retry_at IS NULL OR ds.relay_next_retry_at<=now()))) AS eligible,(t.runtime='shared-relay' AND (ds.health='blocked' OR ds.relay_last_member_check_at IS NULL OR ds.relay_last_member_check_at<=now()-interval '30 minutes')) AS full_relay_probe FROM operation_targets ot JOIN targets t ON t.id=ot.target_id JOIN target_desired_snapshots ds ON ds.target_id=ot.target_id WHERE ot.id=$1 AND ot.pending_rerun AND ot.status IN ('succeeded','failed','cancelled') FOR UPDATE OF ot`, operationTargetID, nodeActive).Scan(&targetID, &eligible, &fullRelayProbe)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE operation_targets SET pending_rerun=false WHERE id=$1`, operationTargetID); err != nil {
		return false, err
	}
	if !eligible {
		return false, tx.Commit(ctx)
	}
	operationID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,metadata) VALUES($1,'reconcile','queued','{"coalesced":true}')`, operationID); err != nil {
		return false, err
	}
	request := `{}`
	if fullRelayProbe {
		request = `{"fullRelayProbe":true}`
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,request) VALUES($1,$2,$3,$4)`, uuid.NewString(), operationID, targetID, request); err != nil {
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

func (s *Store) PendingProfileApplyFinalizations(ctx context.Context) ([]domain.Operation, error) {
	operations, err := s.PendingGovernanceFinalizations(ctx)
	if err != nil {
		return nil, err
	}
	result := operations[:0]
	for _, operation := range operations {
		if operation.Kind == "apply" {
			result = append(result, operation)
		}
	}
	return result, nil
}

func (s *Store) PendingGovernanceFinalizations(ctx context.Context) ([]domain.Operation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id::text,o.kind,o.status,coalesce(o.source_id::text,''),coalesce(o.idempotency_key,''),o.metadata,o.error_code,o.error_reason,o.cancel_requested,o.created_at,o.started_at,o.finished_at,o.updated_at
		FROM operations o
		WHERE o.kind IN ('apply','relay_config_apply','policy_apply')
		  AND o.status='running'
		  AND EXISTS (
		      SELECT 1 FROM operation_targets pending
		      WHERE pending.operation_id=o.id AND pending.governance_finalization_pending
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM operation_targets unfinished
		      WHERE unfinished.operation_id=o.id AND unfinished.status<>'succeeded'
		  )
		ORDER BY o.created_at,o.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Operation{}
	for rows.Next() {
		var operation domain.Operation
		if err := rows.Scan(&operation.ID, &operation.Kind, &operation.Status, &operation.SourceID, &operation.IdempotencyKey, &operation.Metadata, &operation.ErrorCode, &operation.ErrorReason, &operation.CancelRequested, &operation.CreatedAt, &operation.StartedAt, &operation.FinishedAt, &operation.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, operation)
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
	var active bool
	err = tx.QueryRow(ctx, `SELECT ot.operation_id::text,n.archived_at IS NULL FROM operation_targets ot JOIN targets t ON t.id=ot.target_id JOIN nodes n ON n.id=t.node_id WHERE ot.id=$1 FOR SHARE OF n`, operationTargetID).Scan(&operationID, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM operations WHERE id=$1 FOR UPDATE`, operationID).Scan(&operationID); err != nil {
		return err
	}
	var bridgeOperationID string
	err = tx.QueryRow(ctx, `SELECT bridge_operation_id FROM operation_targets WHERE id=$1 AND status='running' FOR UPDATE`, operationTargetID).Scan(&bridgeOperationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !active && bridgeOperationID == "" {
		if _, err := tx.Exec(ctx, `UPDATE operation_targets SET status='cancelled',pending_rerun=false,finished_at=now(),updated_at=now() WHERE id=$1`, operationTargetID); err != nil {
			return err
		}
		if err := recalculateOperation(ctx, tx, operationID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE operation_targets SET status='queued',attempt=attempt+1,result=NULL,error_code='',error_reason='',started_at=NULL,finished_at=NULL,updated_at=now() WHERE id=$1`, operationTargetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE operations SET status=CASE WHEN EXISTS(SELECT 1 FROM operation_targets WHERE operation_id=$1 AND status='running') THEN 'running' ELSE 'queued' END,finished_at=NULL,error_code='',error_reason='',updated_at=now() WHERE id=$1`, operationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RetryFailedTargets(ctx context.Context, operationID, idempotencyKey string) (domain.Operation, error) {
	original, err := s.Operation(ctx, operationID)
	if errors.Is(err, ErrNotFound) {
		return domain.Operation{}, ErrNotFound
	} else if err != nil {
		return domain.Operation{}, err
	}
	if original.Kind == "apply" && original.SourceID != "" && bridgeprotocol.IsSHA256(stringMetadata(original.Metadata, "routingHash")) && uuid.Validate(stringMetadata(original.Metadata, "profileRevisionId")) == nil {
		return s.retryProfileApplyTargets(ctx, operationID, idempotencyKey)
	}
	rows, err := s.pool.Query(ctx, `SELECT target_id::text FROM operation_targets WHERE operation_id=$1 AND status IN ('failed','partial') ORDER BY target_id`, operationID)
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
	requestRows, err := s.pool.Query(ctx, `SELECT target_id::text,request FROM operation_targets WHERE operation_id=$1 AND status IN ('failed','partial')`, operationID)
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
	metadata := map[string]any{}
	if len(original.Metadata) > 0 {
		if err := json.Unmarshal(original.Metadata, &metadata); err != nil || metadata == nil {
			return domain.Operation{}, ErrConflict
		}
	}
	delete(metadata, "governanceFinalizedAction")
	metadata["retryOf"] = operationID
	metadata["originalKind"] = original.Kind
	return s.CreateOperation(ctx, CreateOperationInput{Kind: original.Kind, SourceID: operationID, IdempotencyKey: idempotencyKey, Request: map[string]any{"retryOf": operationID, "targetIds": ids}, Metadata: metadata, TargetIDs: ids, TargetRequests: requests})
}

type profileApplyRetryTarget struct {
	targetID string
	runtime  string
	status   string
	request  json.RawMessage
	result   json.RawMessage
}

func (s *Store) retryProfileApplyTargets(ctx context.Context, operationID, idempotencyKey string) (domain.Operation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Operation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var original domain.Operation
	if err := tx.QueryRow(ctx, `SELECT id::text,kind,status,coalesce(source_id::text,''),coalesce(idempotency_key,''),metadata,error_code,error_reason,cancel_requested,created_at,started_at,finished_at,updated_at FROM operations WHERE id=$1 FOR UPDATE`, operationID).Scan(&original.ID, &original.Kind, &original.Status, &original.SourceID, &original.IdempotencyKey, &original.Metadata, &original.ErrorCode, &original.ErrorReason, &original.CancelRequested, &original.CreatedAt, &original.StartedAt, &original.FinishedAt, &original.UpdatedAt); errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, ErrNotFound
	} else if err != nil {
		return domain.Operation{}, err
	}
	if original.Kind != "apply" || (original.Status != bridgeprotocol.OperationFailed && original.Status != bridgeprotocol.OperationPartial) || uuid.Validate(original.SourceID) != nil || uuid.Validate(stringMetadata(original.Metadata, "profileRevisionId")) != nil || !bridgeprotocol.IsSHA256(stringMetadata(original.Metadata, "routingHash")) {
		return domain.Operation{}, ErrConflict
	}
	rows, err := tx.Query(ctx, `SELECT ot.target_id::text,t.runtime,ot.status,ot.request,ot.result FROM operation_targets ot JOIN targets t ON t.id=ot.target_id WHERE ot.operation_id=$1 ORDER BY ot.target_id`, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	targets := make([]profileApplyRetryTarget, 0, 2)
	for rows.Next() {
		var target profileApplyRetryTarget
		if err := rows.Scan(&target.targetID, &target.runtime, &target.status, &target.request, &target.result); err != nil {
			rows.Close()
			return domain.Operation{}, err
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.Operation{}, err
	}
	rows.Close()
	if len(targets) != 2 {
		return domain.Operation{}, ErrConflict
	}
	var skillTargetID, relayTargetID string
	failedTargets := 0
	targetRequests := make(map[string]any, len(targets))
	for _, target := range targets {
		if target.status != bridgeprotocol.OperationSucceeded && target.status != bridgeprotocol.OperationFailed && target.status != bridgeprotocol.OperationPartial {
			return domain.Operation{}, ErrConflict
		}
		if target.status == bridgeprotocol.OperationFailed || target.status == bridgeprotocol.OperationPartial {
			failedTargets++
		}
		var requestMetadata struct {
			SourceKind string `json:"sourceKind"`
			SourceID   string `json:"sourceId"`
		}
		if err := json.Unmarshal(target.request, &requestMetadata); err != nil || requestMetadata.SourceKind != "profile_apply" || requestMetadata.SourceID != original.SourceID {
			return domain.Operation{}, ErrConflict
		}
		targetRequests[target.targetID] = target.request
		switch target.runtime {
		case domain.RuntimeSharedRelay:
			if relayTargetID != "" {
				return domain.Operation{}, ErrConflict
			}
			relayTargetID = target.targetID
		case domain.RuntimeClaude, domain.RuntimeCodex:
			if skillTargetID != "" {
				return domain.Operation{}, ErrConflict
			}
			skillTargetID = target.targetID
		default:
			return domain.Operation{}, ErrConflict
		}
	}
	if failedTargets == 0 || skillTargetID == "" || relayTargetID == "" {
		return domain.Operation{}, ErrConflict
	}
	request := map[string]any{"retryOf": operationID, "targetIds": []string{skillTargetID, relayTargetID}}
	dependencies := map[string]any{relayTargetID: skillTargetID}
	requestHash, err := operationRequestHashWithDependencies(request, targetRequests, dependencies)
	if err != nil {
		return domain.Operation{}, err
	}
	if idempotencyKey != "" {
		var existingID, existingHash string
		err := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM operations WHERE kind='apply' AND idempotency_key=$1`, idempotencyKey).Scan(&existingID, &existingHash)
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
	if err := validatePublishedProfilePredecessorTx(ctx, tx, original.SourceID, stringMetadata(original.Metadata, "expectedPublishedProfileRevisionId")); err != nil {
		return domain.Operation{}, err
	}
	if err := lockActiveTargets(ctx, tx, []string{skillTargetID, relayTargetID}); err != nil {
		return domain.Operation{}, err
	}
	var metadata map[string]any
	if err := json.Unmarshal(original.Metadata, &metadata); err != nil || metadata == nil {
		return domain.Operation{}, ErrConflict
	}
	delete(metadata, "governanceFinalizedAction")
	metadata["retryOf"] = operationID
	metadata["originalKind"] = original.Kind
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return domain.Operation{}, err
	}
	retryOperationID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,source_id,idempotency_key,request_hash,metadata) VALUES($1,'apply','queued',$2,$3,$4,$5)`, retryOperationID, original.SourceID, nullableText(idempotencyKey), requestHash, jsonText(metadataJSON)); err != nil {
		return domain.Operation{}, err
	}
	rowIDs := map[string]string{skillTargetID: uuid.NewString(), relayTargetID: uuid.NewString()}
	targetByID := make(map[string]profileApplyRetryTarget, len(targets))
	for _, target := range targets {
		targetByID[target.targetID] = target
	}
	for _, targetID := range []string{skillTargetID, relayTargetID} {
		target := targetByID[targetID]
		status := bridgeprotocol.OperationQueued
		var result any
		var finishedAt any
		if target.status == bridgeprotocol.OperationSucceeded {
			status = bridgeprotocol.OperationSucceeded
			finishedAt = time.Now().UTC()
			if len(target.result) > 0 {
				result = jsonText(target.result)
			}
		}
		var dependency any
		if target.targetID == relayTargetID {
			dependency = rowIDs[skillTargetID]
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,depends_on_target_id,status,request,result,finished_at,governance_finalization_pending) VALUES($1,$2,$3,$4,$5,$6,$7,$8,true)`, rowIDs[target.targetID], retryOperationID, target.targetID, dependency, status, jsonText(target.request), result, finishedAt); err != nil {
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
	return s.Operation(ctx, retryOperationID)
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
