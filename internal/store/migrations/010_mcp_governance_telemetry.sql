-- Align durable aggregates with the complete payload-free relay outcome
-- contract and prevent duplicate active scheduler controls.

ALTER TABLE mcp_daily_aggregates
    DROP CONSTRAINT mcp_daily_aggregates_outcome_check;

ALTER TABLE mcp_daily_aggregates
    ADD CONSTRAINT mcp_daily_aggregates_outcome_check CHECK (outcome IN (
        'confirmation_required','confirmed','rejected','expired','denied',
        'not_executed','executed','failed','unknown'
    ));

CREATE UNIQUE INDEX operations_governance_control_one_active_idx
ON operations(kind)
WHERE kind IN ('contract_observe','relay_telemetry_pull')
  AND status IN ('queued','running');
