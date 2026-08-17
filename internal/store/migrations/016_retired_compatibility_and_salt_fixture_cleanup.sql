-- Compatibility/pass-through relay no longer produces ToolHub governance
-- observations. Remove only the stale control-operation history that was
-- generated while the retired admin-socket path was still scheduled.
DELETE FROM operations
WHERE kind IN ('contract_observe', 'relay_telemetry_pull');

-- refresh-failure was a local fixture name, not an accepted Salt minion. The
-- normal refresh already archived it; remove its now-unreferenced target/node
-- rows so the retired fixture cannot reappear in historical target queries.
CREATE TEMP TABLE toolhub_refresh_fixture_nodes(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_refresh_fixture_nodes(id)
SELECT id
FROM nodes
WHERE kind='salt' AND salt_minion_id='refresh-failure';

CREATE TEMP TABLE toolhub_refresh_fixture_targets(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_refresh_fixture_targets(id)
SELECT id FROM targets
WHERE node_id IN (SELECT id FROM toolhub_refresh_fixture_nodes);

DELETE FROM target_desired_snapshots
WHERE target_id IN (SELECT id FROM toolhub_refresh_fixture_targets);
DELETE FROM desired_snapshots
WHERE target_id IN (SELECT id FROM toolhub_refresh_fixture_targets);
DELETE FROM backups
WHERE target_id IN (SELECT id FROM toolhub_refresh_fixture_targets);
DELETE FROM runtime_snapshots
WHERE target_id IN (SELECT id FROM toolhub_refresh_fixture_targets);
DELETE FROM alerts
WHERE target_id IN (SELECT id FROM toolhub_refresh_fixture_targets);

UPDATE operation_targets
SET depends_on_target_id=NULL
WHERE depends_on_target_id IN (SELECT id FROM operation_targets WHERE target_id IN (SELECT id FROM toolhub_refresh_fixture_targets));
DELETE FROM operation_targets
WHERE target_id IN (SELECT id FROM toolhub_refresh_fixture_targets);

DELETE FROM targets
WHERE id IN (SELECT id FROM toolhub_refresh_fixture_targets);
DELETE FROM nodes
WHERE id IN (SELECT id FROM toolhub_refresh_fixture_nodes)
  AND NOT EXISTS (SELECT 1 FROM targets WHERE targets.node_id=nodes.id);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM nodes WHERE salt_minion_id='refresh-failure')
       OR EXISTS (SELECT 1 FROM targets WHERE target_key LIKE 'salt:refresh-failure/%')
       OR EXISTS (SELECT 1 FROM operations WHERE kind IN ('contract_observe', 'relay_telemetry_pull')) THEN
        RAISE EXCEPTION 'retired compatibility or Salt fixture cleanup incomplete';
    END IF;
END $$;
