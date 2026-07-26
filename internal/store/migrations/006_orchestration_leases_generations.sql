ALTER TABLE jobs
    ADD COLUMN lease_owner text,
    ADD COLUMN lease_expires_at timestamptz,
    ADD COLUMN heartbeat_at timestamptz,
    ADD CONSTRAINT jobs_attempts_nonnegative_check CHECK (attempts >= 0 AND max_attempts > 0);

UPDATE jobs
SET lease_expires_at = COALESCE(lease_expires_at, now() - interval '1 second')
WHERE status = 'running';

DROP INDEX IF EXISTS jobs_claim_idx;
CREATE INDEX jobs_claim_pending_idx
    ON jobs(run_after, created_at)
    WHERE status = 'pending';
CREATE INDEX jobs_claim_expired_idx
    ON jobs(lease_expires_at, run_after, created_at)
    WHERE status = 'running';

ALTER TABLE node_tasks
    ADD COLUMN transport text,
    ADD COLUMN lease_owner text,
    ADD COLUMN lease_expires_at timestamptz,
    ADD COLUMN started_at timestamptz,
    ADD COLUMN finished_at timestamptz,
    ADD COLUMN cancel_requested_at timestamptz,
    ADD COLUMN target_kind text,
    ADD COLUMN target_id uuid,
    ADD COLUMN target_generation bigint,
    ADD COLUMN semantic_key text,
    ADD CONSTRAINT node_tasks_transport_check CHECK (transport IS NULL OR transport IN ('agent_wss','ssh')),
    ADD CONSTRAINT node_tasks_attempt_nonnegative_check CHECK (attempt >= 0),
    ADD CONSTRAINT node_tasks_target_generation_nonnegative_check CHECK (target_generation IS NULL OR target_generation >= 0);

UPDATE node_tasks
SET started_at = COALESCE(started_at, updated_at)
WHERE status IN ('running','succeeded','failed','cancelled');

UPDATE node_tasks
SET finished_at = COALESCE(finished_at, updated_at)
WHERE status IN ('succeeded','failed','cancelled');

UPDATE node_tasks
SET lease_expires_at = COALESCE(lease_expires_at, now() - interval '1 second')
WHERE status IN ('delivered','running');

UPDATE node_tasks
SET target_kind = 'skill_deployment',
    target_id = (payload->>'deploymentId')::uuid,
    target_generation = CASE WHEN payload->>'desiredGeneration' ~ '^[0-9]+$' THEN (payload->>'desiredGeneration')::bigint ELSE NULL END,
    semantic_key = CASE
        WHEN payload ? 'desiredGeneration' THEN kind || ':' || payload->>'deploymentId' || ':' || payload->>'desiredGeneration'
        ELSE semantic_key
    END
WHERE kind = 'deploy_skill'
  AND target_id IS NULL
  AND payload ? 'deploymentId'
  AND payload->>'deploymentId' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';

UPDATE node_tasks
SET target_kind = 'mcp_deployment',
    target_id = (payload->>'deploymentId')::uuid,
    target_generation = CASE WHEN payload->>'desiredGeneration' ~ '^[0-9]+$' THEN (payload->>'desiredGeneration')::bigint ELSE NULL END,
    semantic_key = CASE
        WHEN payload ? 'desiredGeneration' THEN kind || ':' || payload->>'deploymentId' || ':' || payload->>'desiredGeneration'
        ELSE semantic_key
    END
WHERE kind = 'apply_mcp'
  AND target_id IS NULL
  AND payload ? 'deploymentId'
  AND payload->>'deploymentId' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';

UPDATE node_tasks
SET status = 'cancelled',
    cancel_requested_at = COALESCE(cancel_requested_at, now()),
    finished_at = COALESCE(finished_at, now()),
    result = jsonb_build_object('error', 'legacy deployment task cancelled during orchestration migration', 'code', 'legacy_task_cancelled'),
    updated_at = now()
WHERE kind IN ('deploy_skill','apply_mcp')
  AND status IN ('pending','delivered','running')
  AND NOT (payload ? 'desiredGeneration');

CREATE INDEX node_tasks_delivery_idx
    ON node_tasks(node_id, status, lease_expires_at, created_at)
    WHERE status IN ('pending','delivered','running');
CREATE INDEX node_tasks_job_recovery_idx
    ON node_tasks(job_id, status, lease_expires_at)
    WHERE job_id IS NOT NULL;
CREATE UNIQUE INDEX node_tasks_semantic_active_idx
    ON node_tasks(semantic_key)
    WHERE semantic_key IS NOT NULL AND status IN ('pending','delivered','running');

ALTER TABLE deployments
    ADD COLUMN desired_generation bigint NOT NULL DEFAULT 1,
    ADD COLUMN actual_generation bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT deployments_desired_generation_check CHECK (desired_generation >= 1),
    ADD CONSTRAINT deployments_actual_generation_check CHECK (actual_generation >= 0);

UPDATE deployments
SET actual_generation = desired_generation
WHERE state = 'in_sync'
  AND desired_version_id IS NOT DISTINCT FROM actual_version_id
  AND desired_enabled = actual_enabled;

ALTER TABLE mcp_deployments
    ADD COLUMN desired_generation bigint NOT NULL DEFAULT 1,
    ADD COLUMN actual_generation bigint NOT NULL DEFAULT 0,
    ADD COLUMN actual_enabled boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT mcp_deployments_desired_generation_check CHECK (desired_generation >= 1),
    ADD CONSTRAINT mcp_deployments_actual_generation_check CHECK (actual_generation >= 0);

UPDATE mcp_deployments
SET actual_enabled = desired_enabled,
    actual_generation = desired_generation
WHERE state = 'in_sync'
  AND desired_hash <> ''
  AND actual_hash = desired_hash;
