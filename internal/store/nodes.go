package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/security"
)

const DefaultTaskLease = 120 * time.Second

type NodeTaskOptions struct {
	TargetKind       string
	TargetID         string
	TargetGeneration int64
	SemanticKey      string
}

type TaskCompletionOutcome string

const (
	TaskCompletionRecorded       TaskCompletionOutcome = "recorded"
	TaskCompletionProjected      TaskCompletionOutcome = "projected"
	TaskCompletionStaleIgnored   TaskCompletionOutcome = "stale_projection_ignored"
	TaskCompletionDuplicate      TaskCompletionOutcome = "duplicate_terminal"
	TaskCompletionCancelled      TaskCompletionOutcome = "cancelled"
	TaskCompletionHeartbeat      TaskCompletionOutcome = "heartbeat"
	TaskCompletionAttemptIgnored TaskCompletionOutcome = "stale_attempt_ignored"
)

func (s *Store) CreateEnrollmentToken(ctx context.Context, nodeName string, labels map[string]string, createdBy string) (string, time.Time, error) {
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" || len(nodeName) > 100 {
		return "", time.Time{}, errors.New("node name must contain 1-100 characters")
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().UTC().Add(30 * time.Minute)
	if labels == nil {
		labels = map[string]string{}
	}
	encodedLabels, _ := json.Marshal(labels)
	_, err = s.pool.Exec(ctx, `INSERT INTO enrollment_tokens(id,token_hash,node_name,labels,expires_at,created_by)
		VALUES($1,$2,$3,$4,$5,$6)`, uuid.NewString(), security.TokenHash(token), nodeName, string(encodedLabels), expires, createdBy)
	return token, expires, err
}

func (s *Store) BootstrapLocalNode(ctx context.Context, nodeName string) (string, bool, error) {
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" || len(nodeName) > 100 {
		return "", false, errors.New("local node name must contain 1-100 characters")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(1848001)); err != nil {
		return "", false, fmt.Errorf("lock project-host bootstrap: %w", err)
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE labels->>'scope'='local'
		ORDER BY (archived_at IS NULL) DESC,created_at LIMIT 1 FOR UPDATE`).Scan(&id)
	if err == nil {
		if _, err := tx.Exec(ctx, `UPDATE nodes SET name=$2,archived_at=NULL,
			status=CASE WHEN archived_at IS NOT NULL THEN 'pending' ELSE status END,
			labels=labels || '{"scope":"local","group":"canary"}'::jsonb,updated_at=now() WHERE id=$1`, id, nodeName); err != nil {
			return "", false, err
		}
		return id, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	id = uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO nodes(id,name,status,labels,connection_preference)
		VALUES($1,$2,'pending','{"scope":"local","group":"canary"}'::jsonb,'agent')`, id, nodeName); err != nil {
		return "", false, fmt.Errorf("create project-host node: %w", err)
	}
	return id, true, tx.Commit(ctx)
}

func (s *Store) EnrollAgent(ctx context.Context, token, hostname, platform, architecture, tailscaleIP string, publicKey []byte) (domain.EnrollmentResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var enrollmentID, nodeName string
	var labels []byte
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT id::text,node_name,labels,expires_at FROM enrollment_tokens
		WHERE token_hash=$1 AND used_at IS NULL FOR UPDATE`, security.TokenHash(token)).Scan(&enrollmentID, &nodeName, &labels, &expires)
	if errors.Is(err, pgx.ErrNoRows) || time.Now().After(expires) {
		return domain.EnrollmentResult{}, errors.New("invalid or expired enrollment token")
	}
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	agentToken, err := security.RandomToken(32)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	taskKeyRaw, err := security.RandomToken(32)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	taskKey, err := base64.RawURLEncoding.DecodeString(taskKeyRaw)
	if err != nil || len(taskKey) != 32 {
		return domain.EnrollmentResult{}, errors.New("generate task key")
	}
	secretID := uuid.NewString()
	ciphertext, err := s.cipher.Encrypt(taskKey, secretID)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO encrypted_secrets(id,name,kind,ciphertext,metadata)
		VALUES($1,$2,'agent-task-key',$3,'{}')`, secretID, "agent-task-key:"+nodeName, ciphertext); err != nil {
		return domain.EnrollmentResult{}, err
	}
	nodeID := ""
	var ipValue any
	if strings.TrimSpace(tailscaleIP) != "" {
		ipValue = strings.TrimSpace(tailscaleIP)
	}
	err = tx.QueryRow(ctx, "SELECT id::text FROM nodes WHERE name=$1 AND status='pending' AND archived_at IS NULL FOR UPDATE", nodeName).Scan(&nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		nodeID = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO nodes(id,name,hostname,platform,architecture,tailscale_ip,status,labels,agent_public_key,agent_token_hash,task_key_secret_id,last_seen_at)
			VALUES($1,$2,$3,$4,$5,$6,'online',$7,$8,$9,$10,now())`, nodeID, nodeName, hostname, platform, architecture, ipValue, string(labels), publicKey, security.TokenHash(agentToken), secretID)
	} else if err == nil {
		_, err = tx.Exec(ctx, `UPDATE nodes SET hostname=$2,platform=$3,architecture=$4,tailscale_ip=$5,status='online',
			labels=labels || $6::jsonb,agent_public_key=$7,agent_token_hash=$8,task_key_secret_id=$9,last_seen_at=now(),updated_at=now()
			WHERE id=$1`, nodeID, hostname, platform, architecture, ipValue, string(labels), publicKey, security.TokenHash(agentToken), secretID)
	}
	if err != nil {
		return domain.EnrollmentResult{}, fmt.Errorf("claim node: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE enrollment_tokens SET used_at=now() WHERE id=$1", enrollmentID); err != nil {
		return domain.EnrollmentResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EnrollmentResult{}, err
	}
	return domain.EnrollmentResult{NodeID: nodeID, AgentToken: agentToken, TaskKey: base64.StdEncoding.EncodeToString(taskKey), ConnectPath: "/agent/v1/connect"}, nil
}

func (s *Store) VerifyAgent(ctx context.Context, nodeID, token string) error {
	var valid bool
	err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND agent_token_hash=$2 AND archived_at IS NULL)", nodeID, security.TokenHash(token)).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AgentTaskKey(ctx context.Context, nodeID string) ([]byte, error) {
	var secretID string
	var ciphertext []byte
	err := s.pool.QueryRow(ctx, `SELECT es.id::text,es.ciphertext FROM nodes n
		JOIN encrypted_secrets es ON es.id=n.task_key_secret_id WHERE n.id=$1`, nodeID).Scan(&secretID, &ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.cipher.Decrypt(ciphertext, secretID)
}

func (s *Store) SetNodeStatus(ctx context.Context, nodeID, status string) error {
	_, err := s.pool.Exec(ctx, "UPDATE nodes SET status=$2,last_seen_at=CASE WHEN $2='online' THEN now() ELSE last_seen_at END,updated_at=now() WHERE id=$1", nodeID, status)
	return err
}

func (s *Store) UpdateHeartbeat(ctx context.Context, nodeID, hostname, platform, architecture string) error {
	_, err := s.pool.Exec(ctx, `UPDATE nodes SET hostname=$2,platform=$3,architecture=$4,status='online',last_seen_at=now(),updated_at=now() WHERE id=$1`, nodeID, hostname, platform, architecture)
	return err
}

func (s *Store) ReplaceInventory(ctx context.Context, nodeID string, inventory domain.AgentInventory) error {
	_, err := s.ProcessAgentInventory(ctx, nodeID, inventory, false)
	return err
}

func (s *Store) CreateNodeTask(ctx context.Context, nodeID, jobID, kind string, payload any) (domain.AgentTask, error) {
	return s.CreateNodeTaskWithOptions(ctx, nodeID, jobID, kind, payload, NodeTaskOptions{})
}

func (s *Store) CreateNodeTaskWithOptions(ctx context.Context, nodeID, jobID, kind string, payload any, options NodeTaskOptions) (domain.AgentTask, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.AgentTask{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AgentTask{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if options.SemanticKey != "" {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "node-task:"+options.SemanticKey); err != nil {
			return domain.AgentTask{}, err
		}
		existing, err := nodeTaskBySemanticKeyTx(ctx, tx, options.SemanticKey)
		if err == nil {
			return existing, tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return domain.AgentTask{}, err
		}
	}
	task := domain.AgentTask{ID: uuid.NewString(), Kind: kind, Payload: encoded, Attempt: 0, CreatedAt: time.Now().UTC(), TargetKind: options.TargetKind, TargetID: options.TargetID, TargetGeneration: options.TargetGeneration, SemanticKey: options.SemanticKey}
	signingBytes, err := protocol.TaskSigningBytes(task.ID, task.Kind, task.Payload)
	if err != nil {
		return domain.AgentTask{}, err
	}
	key, err := agentTaskKeyTx(ctx, tx, nodeID, s.cipher)
	if err != nil {
		return domain.AgentTask{}, err
	}
	task.Signature = security.SignPayload(key, signingBytes)
	var job any
	if jobID != "" {
		job = jobID
	}
	var targetID any
	if options.TargetID != "" {
		targetID = options.TargetID
	}
	var targetGeneration any
	if options.TargetGeneration > 0 {
		targetGeneration = options.TargetGeneration
	}
	var semanticKey any
	if options.SemanticKey != "" {
		semanticKey = options.SemanticKey
	}
	_, err = tx.Exec(ctx, `INSERT INTO node_tasks(id,node_id,job_id,kind,payload,signature,target_kind,target_id,target_generation,semantic_key)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, task.ID, nodeID, job, kind, string(encoded), task.Signature, nullString(options.TargetKind), targetID, targetGeneration, semanticKey)
	if err != nil {
		return domain.AgentTask{}, err
	}
	return task, tx.Commit(ctx)
}

func (s *Store) PendingNodeTasks(ctx context.Context, nodeID string) ([]domain.AgentTask, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,kind,payload,signature,attempt,transport,lease_owner,lease_expires_at,
		started_at,finished_at,cancel_requested_at,coalesce(target_kind,''),coalesce(target_id::text,''),coalesce(target_generation,0),coalesce(semantic_key,''),created_at
		FROM node_tasks
		WHERE node_id=$1 AND status IN ('pending','delivered','running') AND cancel_requested_at IS NULL
		  AND (status='pending' OR lease_expires_at IS NULL OR lease_expires_at <= now())
		ORDER BY created_at LIMIT 100`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []domain.AgentTask
	for rows.Next() {
		var task domain.AgentTask
		if err := rows.Scan(&task.ID, &task.Kind, &task.Payload, &task.Signature, &task.Attempt, &task.Transport, &task.LeaseOwner, &task.LeaseExpiresAt,
			&task.StartedAt, &task.FinishedAt, &task.CancelRequestedAt, &task.TargetKind, &task.TargetID, &task.TargetGeneration, &task.SemanticKey, &task.CreatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) ReserveNodeTask(ctx context.Context, nodeID, id, transport, owner string, lease time.Duration) (domain.AgentTask, error) {
	if transport != "agent_wss" && transport != "ssh" {
		return domain.AgentTask{}, errors.New("invalid task transport")
	}
	if owner == "" {
		return domain.AgentTask{}, errors.New("task lease owner is required")
	}
	if lease <= 0 {
		return domain.AgentTask{}, errors.New("task lease duration must be positive")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AgentTask{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now().UTC()
	var task domain.AgentTask
	var status string
	err = tx.QueryRow(ctx, `SELECT id::text,kind,payload,signature,attempt,transport,lease_owner,lease_expires_at,
		started_at,finished_at,cancel_requested_at,coalesce(target_kind,''),coalesce(target_id::text,''),coalesce(target_generation,0),coalesce(semantic_key,''),created_at,status
		FROM node_tasks WHERE id=$1 AND node_id=$2 FOR UPDATE`, id, nodeID).
		Scan(&task.ID, &task.Kind, &task.Payload, &task.Signature, &task.Attempt, &task.Transport, &task.LeaseOwner, &task.LeaseExpiresAt,
			&task.StartedAt, &task.FinishedAt, &task.CancelRequestedAt, &task.TargetKind, &task.TargetID, &task.TargetGeneration, &task.SemanticKey, &task.CreatedAt, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentTask{}, ErrNotFound
	}
	if err != nil {
		return domain.AgentTask{}, err
	}
	if task.CancelRequestedAt != nil || status == "cancelled" || status == "succeeded" || status == "failed" {
		return domain.AgentTask{}, ErrStateConflict
	}
	if status != "pending" && task.LeaseExpiresAt != nil && task.LeaseExpiresAt.After(now) {
		return domain.AgentTask{}, ErrLeaseLost
	}
	task.Attempt++
	expires := now.Add(lease)
	task.Transport = transport
	task.LeaseOwner = owner
	task.LeaseExpiresAt = &expires
	if _, err := tx.Exec(ctx, `UPDATE node_tasks SET status='delivered',transport=$3,lease_owner=$4,
		lease_expires_at=$5,attempt=$6,updated_at=$7
		WHERE id=$1 AND node_id=$2`, id, nodeID, transport, owner, expires, task.Attempt, now); err != nil {
		return domain.AgentTask{}, err
	}
	return task, tx.Commit(ctx)
}

func (s *Store) CompleteTask(ctx context.Context, nodeID, id, status string, result json.RawMessage) error {
	_, err := s.CompleteTaskAttempt(ctx, nodeID, id, 0, status, result)
	return err
}

func (s *Store) CompleteTaskAttempt(ctx context.Context, nodeID, id string, attempt int, status string, result json.RawMessage) (TaskCompletionOutcome, error) {
	if status != "succeeded" && status != "failed" && status != "running" {
		return "", errors.New("invalid task status")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var kind, currentStatus string
	var payload, currentResult []byte
	var currentAttempt int
	var cancelRequestedAt *time.Time
	if err := tx.QueryRow(ctx, "SELECT kind,payload,status,attempt,result,cancel_requested_at FROM node_tasks WHERE id=$1 AND node_id=$2 FOR UPDATE", id, nodeID).
		Scan(&kind, &payload, &currentStatus, &currentAttempt, &currentResult, &cancelRequestedAt); err != nil {
		return "", err
	}
	if attempt > 0 && attempt != currentAttempt {
		return TaskCompletionAttemptIgnored, ErrLeaseLost
	}
	if attempt == 0 && currentAttempt > 1 {
		return TaskCompletionAttemptIgnored, ErrLeaseLost
	}
	if currentStatus == "succeeded" || currentStatus == "failed" || currentStatus == "cancelled" {
		if currentStatus == status && compactJSONEqual(currentResult, result) {
			return TaskCompletionDuplicate, tx.Commit(ctx)
		}
		if currentStatus == "cancelled" {
			return TaskCompletionCancelled, tx.Commit(ctx)
		}
		return TaskCompletionDuplicate, ErrStateConflict
	}
	if cancelRequestedAt != nil {
		if err := cancelTaskTx(ctx, tx, id, nodeID, result); err != nil {
			return "", err
		}
		return TaskCompletionCancelled, tx.Commit(ctx)
	}
	if status == "running" {
		if err := markTaskRunningTx(ctx, tx, id, nodeID, result); err != nil {
			return "", err
		}
		return TaskCompletionHeartbeat, tx.Commit(ctx)
	}
	finalStatus := status
	finalResult := result
	var projection TaskCompletionOutcome = TaskCompletionRecorded
	var completedInventory *domain.AgentInventory
	if status == "succeeded" && kind == "scan_inventory" {
		var inventory domain.AgentInventory
		if err := json.Unmarshal(result, &inventory); err != nil {
			finalStatus = "failed"
			finalResult = marshalTaskError("inventory task returned an invalid result", "invalid_result")
		} else {
			completedInventory = &inventory
		}
	} else if (status == "succeeded" || status == "failed") && kind == "sync_shared" {
		var task protocol.SyncSharedPayload
		if json.Unmarshal(payload, &task) != nil || task.SourceID == "" {
			finalStatus = "failed"
			finalResult = marshalTaskError("shared sync task payload is invalid", "invalid_payload")
		} else if err := projectSharedSyncResultTx(ctx, tx, nodeID, task.SourceID, finalResult, finalStatus == "succeeded"); err != nil {
			finalStatus = "failed"
			finalResult = marshalTaskError(err.Error(), "projection_failed")
		}
	} else if kind == "deploy_skill" {
		outcome, projectedStatus, projectedResult, err := completeDeploySkillTx(ctx, tx, payload, finalStatus, finalResult)
		if err != nil {
			return "", err
		}
		projection, finalStatus, finalResult = outcome, projectedStatus, projectedResult
	} else if kind == "apply_mcp" {
		outcome, projectedStatus, projectedResult, err := completeApplyMCPTx(ctx, tx, payload, finalStatus, finalResult)
		if err != nil {
			return "", err
		}
		projection, finalStatus, finalResult = outcome, projectedStatus, projectedResult
	} else if status == "succeeded" && kind == "adopt_skill" {
		var task struct {
			DiscoveryID string `json:"discoveryId"`
		}
		if json.Unmarshal(payload, &task) == nil {
			_, _ = tx.Exec(ctx, "UPDATE skill_discoveries SET managed=true,missing=false,drift=false,adoption_status='adopted',adoption_error='',updated_at=now() WHERE id=$1", task.DiscoveryID)
		}
	} else if finalStatus == "failed" {
		var task struct {
			DeploymentID string `json:"deploymentId"`
		}
		if json.Unmarshal(payload, &task) == nil && task.DeploymentID != "" {
			message := truncateSharedError(taskResultMessage(finalResult))
			if kind == "deploy_skill" {
				_, _ = tx.Exec(ctx, "UPDATE deployments SET state='failed',last_error=$2,updated_at=now() WHERE id=$1", task.DeploymentID, message)
			} else if kind == "apply_mcp" {
				_, _ = tx.Exec(ctx, "UPDATE mcp_deployments SET state='failed',last_error=$2,updated_at=now() WHERE id=$1", task.DeploymentID, message)
			}
		}
		if kind == "adopt_skill" {
			var adoption struct {
				DiscoveryID string `json:"discoveryId"`
			}
			if json.Unmarshal(payload, &adoption) == nil && adoption.DiscoveryID != "" {
				message := truncateSharedError(taskResultMessage(finalResult))
				_, _ = tx.Exec(ctx, "UPDATE skill_discoveries SET adoption_status='failed',adoption_error=$2,updated_at=now() WHERE id=$1 AND NOT managed", adoption.DiscoveryID, message)
			}
		}
	}
	if err := terminalizeTaskTx(ctx, tx, id, nodeID, finalStatus, finalResult); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if completedInventory != nil {
		if err := s.ReplaceInventory(ctx, nodeID, *completedInventory); err != nil {
			return "", err
		}
	}
	return projection, nil
}

func nodeTaskBySemanticKeyTx(ctx context.Context, tx pgx.Tx, semanticKey string) (domain.AgentTask, error) {
	var task domain.AgentTask
	err := tx.QueryRow(ctx, `SELECT id::text,kind,payload,signature,attempt,transport,lease_owner,lease_expires_at,
		started_at,finished_at,cancel_requested_at,coalesce(target_kind,''),coalesce(target_id::text,''),coalesce(target_generation,0),coalesce(semantic_key,''),created_at
		FROM node_tasks WHERE semantic_key=$1 AND status IN ('pending','delivered','running') ORDER BY created_at LIMIT 1`, semanticKey).
		Scan(&task.ID, &task.Kind, &task.Payload, &task.Signature, &task.Attempt, &task.Transport, &task.LeaseOwner, &task.LeaseExpiresAt,
			&task.StartedAt, &task.FinishedAt, &task.CancelRequestedAt, &task.TargetKind, &task.TargetID, &task.TargetGeneration, &task.SemanticKey, &task.CreatedAt)
	return task, err
}

func markTaskRunningTx(ctx context.Context, tx pgx.Tx, id, nodeID string, result json.RawMessage) error {
	now := time.Now().UTC()
	_, err := tx.Exec(ctx, `UPDATE node_tasks SET status='running',result=$3,started_at=COALESCE(started_at,$4),
		lease_expires_at=$5,updated_at=$4 WHERE id=$1 AND node_id=$2`, id, nodeID, string(result), now, now.Add(DefaultTaskLease))
	return err
}

func terminalizeTaskTx(ctx context.Context, tx pgx.Tx, id, nodeID, status string, result json.RawMessage) error {
	now := time.Now().UTC()
	_, err := tx.Exec(ctx, `UPDATE node_tasks SET status=$3,result=$4,finished_at=$5,lease_owner=NULL,
		lease_expires_at=NULL,updated_at=$5 WHERE id=$1 AND node_id=$2`, id, nodeID, status, string(result), now)
	return err
}

func cancelTaskTx(ctx context.Context, tx pgx.Tx, id, nodeID string, result json.RawMessage) error {
	now := time.Now().UTC()
	late := marshalTaskError("task result ignored after cancellation", "task_cancelled")
	if len(result) > 0 {
		late, _ = json.Marshal(map[string]any{"error": "task result ignored after cancellation", "code": "task_cancelled", "lateResult": json.RawMessage(result)})
	}
	_, err := tx.Exec(ctx, `UPDATE node_tasks SET status='cancelled',result=$3,finished_at=$4,lease_owner=NULL,
		lease_expires_at=NULL,updated_at=$4 WHERE id=$1 AND node_id=$2`, id, nodeID, string(late), now)
	return err
}

func completeDeploySkillTx(ctx context.Context, tx pgx.Tx, payload []byte, status string, result json.RawMessage) (TaskCompletionOutcome, string, json.RawMessage, error) {
	var task protocol.DeploySkillPayload
	if err := json.Unmarshal(payload, &task); err != nil || task.DeploymentID == "" || task.VersionID == "" || task.DesiredGeneration <= 0 {
		return TaskCompletionRecorded, "failed", marshalTaskError("deploy_skill task payload is invalid", "invalid_payload"), nil
	}
	if status == "succeeded" {
		var actual protocol.DeploySkillResult
		if err := json.Unmarshal(result, &actual); err != nil || actual.ActualHash != task.SHA256 || actual.ActualEnabled != task.Enabled {
			return projectSkillFailureTx(ctx, tx, task, marshalTaskError("deploy_skill task returned an invalid result", "invalid_result"))
		}
		command, err := tx.Exec(ctx, `UPDATE deployments SET actual_version_id=$2,actual_enabled=$3,actual_generation=$4,
			state='in_sync',last_error='',reconciled_at=now(),updated_at=now()
			WHERE id=$1 AND desired_generation=$4 AND desired_version_id=$2 AND desired_enabled=$3`,
			task.DeploymentID, task.VersionID, task.Enabled, task.DesiredGeneration)
		if err != nil {
			return "", "", nil, err
		}
		if command.RowsAffected() == 0 {
			return TaskCompletionStaleIgnored, status, result, nil
		}
		return TaskCompletionProjected, status, result, nil
	}
	return projectSkillFailureTx(ctx, tx, task, result)
}

func projectSkillFailureTx(ctx context.Context, tx pgx.Tx, task protocol.DeploySkillPayload, result json.RawMessage) (TaskCompletionOutcome, string, json.RawMessage, error) {
	message := truncateSharedError(taskResultMessage(result))
	command, err := tx.Exec(ctx, `UPDATE deployments SET state='failed',last_error=$5,updated_at=now()
		WHERE id=$1 AND desired_generation=$4 AND desired_version_id=$2 AND desired_enabled=$3`,
		task.DeploymentID, task.VersionID, task.Enabled, task.DesiredGeneration, message)
	if err != nil {
		return "", "", nil, err
	}
	if command.RowsAffected() == 0 {
		return TaskCompletionStaleIgnored, "failed", result, nil
	}
	return TaskCompletionProjected, "failed", result, nil
}

func completeApplyMCPTx(ctx context.Context, tx pgx.Tx, payload []byte, status string, result json.RawMessage) (TaskCompletionOutcome, string, json.RawMessage, error) {
	var task protocol.ApplyMCPPayload
	if err := json.Unmarshal(payload, &task); err != nil || task.DeploymentID == "" || task.DesiredGeneration <= 0 || task.DesiredHash == "" {
		return TaskCompletionRecorded, "failed", marshalTaskError("apply_mcp task payload is invalid", "invalid_payload"), nil
	}
	if status == "succeeded" {
		var actual protocol.ApplyMCPResult
		if err := json.Unmarshal(result, &actual); err != nil || actual.ActualHash != task.DesiredHash || actual.ActualEnabled != task.Enabled {
			return projectMCPFailureTx(ctx, tx, task, marshalTaskError("apply_mcp task returned an invalid result", "invalid_result"))
		}
		command, err := tx.Exec(ctx, `UPDATE mcp_deployments SET actual_hash=$2,actual_enabled=$3,actual_generation=$4,
			state='in_sync',last_error='',updated_at=now()
			WHERE id=$1 AND desired_hash=$2 AND desired_enabled=$3 AND desired_generation=$4`,
			task.DeploymentID, task.DesiredHash, task.Enabled, task.DesiredGeneration)
		if err != nil {
			return "", "", nil, err
		}
		if command.RowsAffected() == 0 {
			return TaskCompletionStaleIgnored, status, result, nil
		}
		return TaskCompletionProjected, status, result, nil
	}
	return projectMCPFailureTx(ctx, tx, task, result)
}

func projectMCPFailureTx(ctx context.Context, tx pgx.Tx, task protocol.ApplyMCPPayload, result json.RawMessage) (TaskCompletionOutcome, string, json.RawMessage, error) {
	message := truncateSharedError(taskResultMessage(result))
	command, err := tx.Exec(ctx, `UPDATE mcp_deployments SET state='failed',last_error=$5,updated_at=now()
		WHERE id=$1 AND desired_hash=$2 AND desired_enabled=$3 AND desired_generation=$4`,
		task.DeploymentID, task.DesiredHash, task.Enabled, task.DesiredGeneration, message)
	if err != nil {
		return "", "", nil, err
	}
	if command.RowsAffected() == 0 {
		return TaskCompletionStaleIgnored, "failed", result, nil
	}
	return TaskCompletionProjected, "failed", result, nil
}

func marshalTaskError(message, code string) json.RawMessage {
	body, _ := json.Marshal(map[string]any{"error": message, "code": code})
	return body
}

func taskResultMessage(result json.RawMessage) string {
	var value struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.Unmarshal(result, &value) == nil && value.Error != "" {
		if value.Code != "" {
			return value.Code + ": " + value.Error
		}
		return value.Error
	}
	return string(result)
}

func compactJSONEqual(first, second []byte) bool {
	var compactFirst, compactSecond bytes.Buffer
	if json.Compact(&compactFirst, first) != nil || json.Compact(&compactSecond, second) != nil {
		return bytes.Equal(bytes.TrimSpace(first), bytes.TrimSpace(second))
	}
	return bytes.Equal(compactFirst.Bytes(), compactSecond.Bytes())
}
