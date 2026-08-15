-- A successful runtime step is not the end of a governance operation. Keep
-- target ownership until PostgreSQL pointers and desired snapshots finalize.

ALTER TABLE operation_targets
    ADD COLUMN governance_finalization_pending boolean NOT NULL DEFAULT false;

CREATE TEMP TABLE governance_finalization_superseded_operations
ON COMMIT DROP
AS
SELECT DISTINCT stale_target.operation_id
FROM operation_targets stale_target
JOIN operations stale_operation ON stale_operation.id = stale_target.operation_id
JOIN operation_targets active_target
  ON active_target.target_id = stale_target.target_id
 AND active_target.operation_id <> stale_target.operation_id
WHERE stale_target.status = 'succeeded'
  AND active_target.status IN ('queued','running')
  AND stale_operation.kind IN ('apply','relay_config_apply','policy_apply')
  AND stale_operation.metadata ? 'routingHash'
  AND NOT (stale_operation.metadata ? 'governanceFinalizedAction')
  AND stale_operation.status IN ('queued','running');

UPDATE operation_targets target
SET status = 'failed',
    error_code = 'governance_finalization_interrupted',
    error_reason = 'Governance finalization was superseded by a newer active target owner',
    finished_at = now(),
    updated_at = now()
WHERE target.operation_id IN (SELECT operation_id FROM governance_finalization_superseded_operations)
  AND target.status = 'queued';

UPDATE operations operation
SET status = 'failed',
    error_code = 'governance_finalization_interrupted',
    error_reason = 'Governance finalization was superseded by a newer active target owner',
    finished_at = now(),
    updated_at = now()
WHERE operation.id IN (SELECT operation_id FROM governance_finalization_superseded_operations)
  AND NOT EXISTS (
      SELECT 1
      FROM operation_targets target
      WHERE target.operation_id = operation.id
        AND target.status = 'running'
  );

-- A running Bridge step must still be recovered. Its pending flag retains
-- ownership until replay reaches a terminal result; the incomplete pending
-- set then makes the deterministic finalizer fail and release the operation.
UPDATE operation_targets target
SET governance_finalization_pending = true
WHERE target.operation_id IN (SELECT operation_id FROM governance_finalization_superseded_operations)
  AND target.status = 'running';

UPDATE operations operation
SET metadata = jsonb_set(metadata, '{governanceFinalizationInterrupted}', 'true'::jsonb, true),
    updated_at = now()
WHERE operation.id IN (SELECT operation_id FROM governance_finalization_superseded_operations)
  AND operation.status = 'running';

UPDATE operation_targets target
SET governance_finalization_pending = true
FROM operations operation
WHERE operation.id = target.operation_id
  AND operation.kind IN ('apply','relay_config_apply','policy_apply')
  AND operation.metadata ? 'routingHash'
  AND NOT (operation.metadata ? 'governanceFinalizedAction')
  AND operation.status IN ('queued','running')
  AND operation.id NOT IN (SELECT operation_id FROM governance_finalization_superseded_operations);

UPDATE operations operation
SET status = 'failed',
    error_code = 'governance_finalization_interrupted',
    error_reason = 'Runtime work succeeded before governance finalization; submit a new Apply',
    updated_at = now()
WHERE operation.status = 'succeeded'
  AND operation.kind IN ('apply','relay_config_apply','policy_apply')
  AND operation.metadata ? 'routingHash'
  AND NOT (operation.metadata ? 'governanceFinalizedAction');

DROP INDEX operation_targets_one_active_idx;
CREATE UNIQUE INDEX operation_targets_one_active_idx ON operation_targets(target_id)
WHERE status IN ('queued','running') OR governance_finalization_pending;
