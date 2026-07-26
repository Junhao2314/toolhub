package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
)

type JobInput struct {
	Kind    string
	Payload any
	DryRun  bool
}

func (s *Store) EnqueueJob(ctx context.Context, kind string, payload any, dryRun bool, createdBy string) (domain.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	job, err := s.enqueueJobTx(ctx, tx, kind, payload, dryRun, createdBy)
	if err != nil {
		return domain.Job{}, err
	}
	return job, tx.Commit(ctx)
}

func (s *Store) EnqueueJobs(ctx context.Context, inputs []JobInput, createdBy string) ([]domain.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	jobs := make([]domain.Job, 0, len(inputs))
	for _, input := range inputs {
		job, err := s.enqueueJobTx(ctx, tx, input.Kind, input.Payload, input.DryRun, createdBy)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, tx.Commit(ctx)
}

func (s *Store) enqueueJobTx(ctx context.Context, tx pgx.Tx, kind string, payload any, dryRun bool, createdBy string) (domain.Job, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.Job{}, err
	}
	now := time.Now().UTC()
	job := domain.Job{ID: uuid.NewString(), Kind: kind, Status: "pending", Payload: encoded, DryRun: dryRun, MaxAttempts: 5, RunAfter: now}
	var actor any
	if createdBy != "" {
		actor = createdBy
		job.CreatedBy = createdBy
	}
	_, err = tx.Exec(ctx, `INSERT INTO jobs(id,kind,payload,dry_run,created_by,run_after) VALUES($1,$2,$3,$4,$5,$6)`, job.ID, job.Kind, string(job.Payload), dryRun, actor, now)
	return job, err
}

func (s *Store) ClaimJob(ctx context.Context, owner string, lease time.Duration) (domain.Job, error) {
	if owner == "" {
		return domain.Job{}, errors.New("job lease owner is required")
	}
	if lease <= 0 {
		return domain.Job{}, errors.New("job lease duration must be positive")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now().UTC()
	_, _ = tx.Exec(ctx, `UPDATE jobs SET status='cancelled',
		result=jsonb_build_object('cancelled',true,'reason','cancel_requested'),finished_at=$1,
		lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=$1
		WHERE status='running' AND cancel_requested_at IS NOT NULL AND lease_expires_at IS NOT NULL AND lease_expires_at <= $1`, now)
	_, _ = tx.Exec(ctx, `UPDATE jobs SET status='failed',
		result=jsonb_build_object('error','job lease expired after max attempts','code','lease_expired'),
		finished_at=$1,lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=$1
		WHERE status='running' AND cancel_requested_at IS NULL AND lease_expires_at IS NOT NULL AND lease_expires_at <= $1 AND attempts >= max_attempts`, now)
	var job domain.Job
	err = tx.QueryRow(ctx, `SELECT id::text,kind,status,payload,dry_run,attempts,max_attempts,run_after,coalesce(created_by::text,'')
		FROM jobs
		WHERE attempts < max_attempts AND (
			(status='pending' AND run_after <= $1)
			OR (status='running' AND cancel_requested_at IS NULL AND lease_expires_at IS NOT NULL AND lease_expires_at <= $1)
		)
		ORDER BY CASE WHEN status='running' THEN 0 ELSE 1 END,run_after,created_at
		FOR UPDATE SKIP LOCKED LIMIT 1`, now).Scan(&job.ID, &job.Kind, &job.Status, &job.Payload, &job.DryRun, &job.Attempts, &job.MaxAttempts, &job.RunAfter, &job.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Job{}, ErrNotFound
	}
	if err != nil {
		return domain.Job{}, err
	}
	job.Attempts++
	expires := now.Add(lease)
	job.Status = "running"
	job.LeaseOwner = owner
	job.LeaseExpiresAt = &expires
	job.HeartbeatAt = &now
	if _, err := tx.Exec(ctx, `UPDATE jobs SET status='running',started_at=$2,attempts=$3,
		lease_owner=$4,lease_expires_at=$5,heartbeat_at=$2
		WHERE id=$1`, job.ID, now, job.Attempts, owner, expires); err != nil {
		return domain.Job{}, err
	}
	return job, tx.Commit(ctx)
}

func (s *Store) RenewJobLease(ctx context.Context, id, owner string, attempt int, lease time.Duration) error {
	if owner == "" || attempt <= 0 || lease <= 0 {
		return ErrLeaseLost
	}
	now := time.Now().UTC()
	command, err := s.pool.Exec(ctx, `UPDATE jobs SET lease_expires_at=$4,heartbeat_at=$5
		WHERE id=$1 AND status='running' AND lease_owner=$2 AND attempts=$3 AND cancel_requested_at IS NULL`, id, owner, attempt, now.Add(lease), now)
	if err != nil {
		return err
	}
	if command.RowsAffected() > 0 {
		return nil
	}
	var cancelled bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE id=$1 AND cancel_requested_at IS NOT NULL)", id).Scan(&cancelled); err != nil {
		return err
	}
	if cancelled {
		return ErrJobCancelled
	}
	return ErrLeaseLost
}

func (s *Store) FinishJob(ctx context.Context, id, owner string, attempt int, result any) error {
	encoded, _ := json.Marshal(result)
	now := time.Now().UTC()
	command, err := s.pool.Exec(ctx, `UPDATE jobs SET status='succeeded',result=$4,finished_at=$5,
		lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=$5
		WHERE id=$1 AND status='running' AND lease_owner=$2 AND attempts=$3 AND cancel_requested_at IS NULL`, id, owner, attempt, string(encoded), now)
	if err != nil {
		return err
	}
	if command.RowsAffected() > 0 {
		return nil
	}
	cancelled, cancelErr := s.finishCancelledJob(ctx, id, owner, attempt, now)
	if cancelErr != nil {
		return cancelErr
	}
	if cancelled {
		return ErrJobCancelled
	}
	return ErrLeaseLost
}

func (s *Store) FailJob(ctx context.Context, job domain.Job, owner, message string) error {
	now := time.Now().UTC()
	cancelled, cancelErr := s.finishCancelledJob(ctx, job.ID, owner, job.Attempts, now)
	if cancelErr != nil {
		return cancelErr
	}
	if cancelled {
		return ErrJobCancelled
	}
	if job.Attempts < job.MaxAttempts {
		delay := time.Duration(job.Attempts*job.Attempts) * 10 * time.Second
		command, err := s.pool.Exec(ctx, `UPDATE jobs SET status='pending',
			result=jsonb_build_object('error',$4::text),run_after=$5,
			lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL
			WHERE id=$1 AND status='running' AND lease_owner=$2 AND attempts=$3 AND cancel_requested_at IS NULL`, job.ID, owner, job.Attempts, message, now.Add(delay))
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		return nil
	}
	command, err := s.pool.Exec(ctx, `UPDATE jobs SET status='failed',
		result=jsonb_build_object('error',$4::text),finished_at=$5,
		lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=$5
		WHERE id=$1 AND status='running' AND lease_owner=$2 AND attempts=$3 AND cancel_requested_at IS NULL`, job.ID, owner, job.Attempts, message, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (s *Store) CancelJob(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now().UTC()
	command, err := tx.Exec(ctx, `UPDATE jobs SET status='cancelled',cancel_requested_at=$2,finished_at=$2,
		result=jsonb_build_object('cancelled',true),
		lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=$2
		WHERE id=$1 AND status='pending'`, id, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		command, err = tx.Exec(ctx, `UPDATE jobs SET cancel_requested_at=COALESCE(cancel_requested_at,$2)
			WHERE id=$1 AND status='running'`, id, now)
		if err != nil {
			return err
		}
	}
	if command.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE id=$1)", id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrStateConflict
		}
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE node_tasks SET status='cancelled',cancel_requested_at=COALESCE(cancel_requested_at,$2),
		finished_at=COALESCE(finished_at,$2),updated_at=$2 WHERE job_id=$1 AND status IN ('pending','delivered')`, id, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE node_tasks SET cancel_requested_at=COALESCE(cancel_requested_at,$2),
		updated_at=$2 WHERE job_id=$1 AND status='running'`, id, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) finishCancelledJob(ctx context.Context, id, owner string, attempt int, now time.Time) (bool, error) {
	command, err := s.pool.Exec(ctx, `UPDATE jobs SET status='cancelled',
		result=jsonb_build_object('cancelled',true),finished_at=$4,
		lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=$4
		WHERE id=$1 AND status='running' AND lease_owner=$2 AND attempts=$3 AND cancel_requested_at IS NOT NULL`, id, owner, attempt, now)
	if err != nil {
		return false, err
	}
	return command.RowsAffected() > 0, nil
}
