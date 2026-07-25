package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/toolhub-dev/toolhub/internal/domain"
	"github.com/toolhub-dev/toolhub/internal/protocol"
	"github.com/toolhub-dev/toolhub/internal/security"
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
	encodedLabels, _ := json.Marshal(labels)
	_, err = s.pool.Exec(ctx, `INSERT INTO enrollment_tokens(id,token_hash,node_name,labels,expires_at,created_by)
		VALUES($1,$2,$3,$4,$5,$6)`, uuid.NewString(), security.TokenHash(token), nodeName, encodedLabels, expires, createdBy)
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
			VALUES($1,$2,$3,$4,$5,$6,'online',$7,$8,$9,$10,now())`, nodeID, nodeName, hostname, platform, architecture, ipValue, labels, publicKey, security.TokenHash(agentToken), secretID)
	} else if err == nil {
		_, err = tx.Exec(ctx, `UPDATE nodes SET hostname=$2,platform=$3,architecture=$4,tailscale_ip=$5,status='online',
			labels=labels || $6::jsonb,agent_public_key=$7,agent_token_hash=$8,task_key_secret_id=$9,last_seen_at=now(),updated_at=now()
			WHERE id=$1`, nodeID, hostname, platform, architecture, ipValue, labels, publicKey, security.TokenHash(agentToken), secretID)
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

func (s *Store) ReplaceInventory(ctx context.Context, nodeID string, runtimes []domain.InventoryRuntime) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, runtime := range runtimes {
		config, _ := json.Marshal(security.RedactMap(runtime.Config))
		inventory, _ := json.Marshal(security.RedactMap(runtime.Inventory))
		_, err := tx.Exec(ctx, `INSERT INTO runtimes(id,node_id,kind,root_path,version,config,inventory,scanned_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,now())
			ON CONFLICT(node_id,kind,root_path) DO UPDATE SET version=excluded.version,config=excluded.config,inventory=excluded.inventory,scanned_at=now()`, uuid.NewString(), nodeID, runtime.Kind, runtime.RootPath, runtime.Version, config, inventory)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, "UPDATE nodes SET last_seen_at=now(),status='online',updated_at=now() WHERE id=$1", nodeID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CreateNodeTask(ctx context.Context, nodeID, jobID, kind string, payload any) (domain.AgentTask, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.AgentTask{}, err
	}
	task := domain.AgentTask{ID: uuid.NewString(), Kind: kind, Payload: encoded, Attempt: 0, CreatedAt: time.Now().UTC()}
	signingBytes, err := protocol.TaskSigningBytes(task.ID, task.Kind, task.Payload)
	if err != nil {
		return domain.AgentTask{}, err
	}
	key, err := s.AgentTaskKey(ctx, nodeID)
	if err != nil {
		return domain.AgentTask{}, err
	}
	task.Signature = security.SignPayload(key, signingBytes)
	var job any
	if jobID != "" {
		job = jobID
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO node_tasks(id,node_id,job_id,kind,payload,signature) VALUES($1,$2,$3,$4,$5,$6)`, task.ID, nodeID, job, kind, encoded, task.Signature)
	return task, err
}

func (s *Store) PendingNodeTasks(ctx context.Context, nodeID string) ([]domain.AgentTask, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,kind,payload,signature,attempt,created_at FROM node_tasks
		WHERE node_id=$1 AND status IN ('pending','delivered') ORDER BY created_at LIMIT 100`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []domain.AgentTask
	for rows.Next() {
		var task domain.AgentTask
		if err := rows.Scan(&task.ID, &task.Kind, &task.Payload, &task.Signature, &task.Attempt, &task.CreatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) MarkTaskDelivered(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, "UPDATE node_tasks SET status='delivered',attempt=attempt+1,updated_at=now() WHERE id=$1 AND status='pending'", id)
	return err
}

func (s *Store) CompleteTask(ctx context.Context, nodeID, id, status string, result json.RawMessage) error {
	if status != "succeeded" && status != "failed" && status != "running" {
		return errors.New("invalid task status")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var kind string
	var payload []byte
	if err := tx.QueryRow(ctx, "SELECT kind,payload FROM node_tasks WHERE id=$1 AND node_id=$2 FOR UPDATE", id, nodeID).Scan(&kind, &payload); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE node_tasks SET status=$3,result=$4,updated_at=now() WHERE id=$1 AND node_id=$2", id, nodeID, status, result); err != nil {
		return err
	}
	if status == "succeeded" && kind == "deploy_skill" {
		var task struct {
			DeploymentID string `json:"deploymentId"`
			VersionID    string `json:"versionId"`
			Enabled      bool   `json:"enabled"`
		}
		if json.Unmarshal(payload, &task) == nil {
			_, _ = tx.Exec(ctx, `UPDATE deployments SET actual_version_id=$2,actual_enabled=$3,state='in_sync',last_error='',reconciled_at=now(),updated_at=now() WHERE id=$1`, task.DeploymentID, task.VersionID, task.Enabled)
		}
	} else if status == "succeeded" && kind == "apply_mcp" {
		var task struct {
			DeploymentID string `json:"deploymentId"`
		}
		if json.Unmarshal(payload, &task) == nil {
			_, _ = tx.Exec(ctx, "UPDATE mcp_deployments SET actual_hash=desired_hash,state='in_sync',last_error='',updated_at=now() WHERE id=$1", task.DeploymentID)
		}
	} else if status == "failed" {
		var task struct {
			DeploymentID string `json:"deploymentId"`
		}
		if json.Unmarshal(payload, &task) == nil && task.DeploymentID != "" {
			message := string(result)
			if len(message) > 2000 {
				message = message[:2000]
			}
			if kind == "deploy_skill" {
				_, _ = tx.Exec(ctx, "UPDATE deployments SET state='failed',last_error=$2,updated_at=now() WHERE id=$1", task.DeploymentID, message)
			} else if kind == "apply_mcp" {
				_, _ = tx.Exec(ctx, "UPDATE mcp_deployments SET state='failed',last_error=$2,updated_at=now() WHERE id=$1", task.DeploymentID, message)
			}
		}
	}
	return tx.Commit(ctx)
}
