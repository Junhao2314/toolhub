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

func (s *Store) EnqueueJob(ctx context.Context, kind string, payload any, dryRun bool, createdBy string) (domain.Job, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.Job{}, err
	}
	job := domain.Job{ID: uuid.NewString(), Kind: kind, Status: "pending", Payload: encoded, DryRun: dryRun, MaxAttempts: 5, RunAfter: time.Now().UTC()}
	var actor any
	if createdBy != "" {
		actor = createdBy
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO jobs(id,kind,payload,dry_run,created_by) VALUES($1,$2,$3,$4,$5)`, job.ID, job.Kind, string(job.Payload), dryRun, actor)
	return job, err
}

func (s *Store) ClaimJob(ctx context.Context) (domain.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var job domain.Job
	err = tx.QueryRow(ctx, `SELECT id::text,kind,status,payload,dry_run,attempts,max_attempts,run_after,coalesce(created_by::text,'')
		FROM jobs WHERE status='pending' AND run_after<=now() ORDER BY created_at
		FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&job.ID, &job.Kind, &job.Status, &job.Payload, &job.DryRun, &job.Attempts, &job.MaxAttempts, &job.RunAfter, &job.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Job{}, ErrNotFound
	}
	if err != nil {
		return domain.Job{}, err
	}
	job.Attempts++
	if _, err := tx.Exec(ctx, "UPDATE jobs SET status='running',started_at=now(),attempts=$2 WHERE id=$1", job.ID, job.Attempts); err != nil {
		return domain.Job{}, err
	}
	return job, tx.Commit(ctx)
}

func (s *Store) FinishJob(ctx context.Context, id string, result any) error {
	encoded, _ := json.Marshal(result)
	_, err := s.pool.Exec(ctx, "UPDATE jobs SET status='succeeded',result=$2,finished_at=now() WHERE id=$1", id, string(encoded))
	return err
}

func (s *Store) FailJob(ctx context.Context, job domain.Job, message string) error {
	if job.Attempts < job.MaxAttempts {
		delay := time.Duration(job.Attempts*job.Attempts) * 10 * time.Second
		_, err := s.pool.Exec(ctx, "UPDATE jobs SET status='pending',result=jsonb_build_object('error',$2::text),run_after=now()+$3::interval WHERE id=$1", job.ID, message, delay.String())
		return err
	}
	_, err := s.pool.Exec(ctx, "UPDATE jobs SET status='failed',result=jsonb_build_object('error',$2::text),finished_at=now() WHERE id=$1", job.ID, message)
	return err
}

func (s *Store) CancelJob(ctx context.Context, id string) error {
	command, err := s.pool.Exec(ctx, `UPDATE jobs SET status='cancelled',cancel_requested_at=now(),finished_at=now()
		WHERE id=$1 AND status IN ('pending','running')`, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, _ = s.pool.Exec(ctx, "UPDATE node_tasks SET status='cancelled',updated_at=now() WHERE job_id=$1 AND status IN ('pending','delivered')", id)
	return nil
}
