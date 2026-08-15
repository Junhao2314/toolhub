-- A successful runtime step is not the end of a governance operation. Keep
-- target ownership until PostgreSQL pointers and desired snapshots finalize.

ALTER TABLE operation_targets
    ADD COLUMN governance_finalization_pending boolean NOT NULL DEFAULT false;

UPDATE operation_targets target
SET governance_finalization_pending = true
FROM operations operation
WHERE operation.id = target.operation_id
  AND operation.kind IN ('apply','relay_config_apply','policy_apply')
  AND operation.metadata ? 'routingHash'
  AND NOT (operation.metadata ? 'governanceFinalizedAction')
  AND operation.status IN ('queued','running');

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
