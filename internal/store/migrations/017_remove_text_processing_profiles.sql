-- Remove the two obsolete text-processing Profiles after their runtime
-- directories were retired.  These Profiles are archived, have no active
-- Relay/MCP ownership, and no current operation/snapshot references.
--
-- The migration is deliberately idempotent and scoped by exact Profile names.
-- Skills remain untouched: every Skill member is shared with another Profile
-- and is therefore not an uninstall candidate.

CREATE TEMP TABLE toolhub_text_processing_profiles(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_text_processing_profiles(id)
SELECT id
FROM profiles
WHERE name IN ('claude-text-processing', 'codex-text-processing');

CREATE TEMP TABLE toolhub_text_processing_revisions(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_text_processing_revisions(id)
SELECT id
FROM profile_revisions
WHERE profile_id IN (SELECT id FROM toolhub_text_processing_profiles);

CREATE TEMP TABLE toolhub_text_processing_operations(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_text_processing_operations(id)
SELECT id
FROM operations
WHERE source_id IN (SELECT id FROM toolhub_text_processing_profiles)
   OR metadata->>'profileId' IN (SELECT id::text FROM toolhub_text_processing_profiles)
   OR metadata->>'profileRevisionId' IN (SELECT id::text FROM toolhub_text_processing_revisions);

CREATE TEMP TABLE toolhub_text_processing_operation_targets(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_text_processing_operation_targets(id)
SELECT id
FROM operation_targets
WHERE operation_id IN (SELECT id FROM toolhub_text_processing_operations);

-- Remove dependent historical projections before deleting immutable Profile
-- revisions.  These tables are not expected to contain rows for the current
-- database, but keeping the exact cleanup makes the migration safe on an
-- upgraded generation-2 installation.
ALTER TABLE profile_revision_skills DISABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_servers DISABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_governance DISABLE TRIGGER USER;
ALTER TABLE profile_revision_tool_rules DISABLE TRIGGER USER;

DELETE FROM target_desired_snapshots
WHERE snapshot_id IN (
    SELECT id
    FROM desired_snapshots
    WHERE source_id IN (SELECT id FROM toolhub_text_processing_profiles)
       OR source_operation_target_id IN (SELECT id FROM toolhub_text_processing_operation_targets)
);
DELETE FROM desired_snapshots
WHERE source_id IN (SELECT id FROM toolhub_text_processing_profiles)
   OR source_operation_target_id IN (SELECT id FROM toolhub_text_processing_operation_targets);

DELETE FROM preflight_confirmations
WHERE profile_id IN (SELECT id FROM toolhub_text_processing_profiles);
DELETE FROM bundle_import_fingerprints
WHERE profile_id IN (SELECT id FROM toolhub_text_processing_profiles)
   OR profile_revision_id IN (SELECT id FROM toolhub_text_processing_revisions);
DELETE FROM mcp_daily_aggregates
WHERE profile_id IN (SELECT id FROM toolhub_text_processing_profiles)
   OR profile_revision_id IN (SELECT id FROM toolhub_text_processing_revisions);
DELETE FROM published_profiles
WHERE profile_id IN (SELECT id FROM toolhub_text_processing_profiles)
   OR profile_revision_id IN (SELECT id FROM toolhub_text_processing_revisions);

-- Operations are removed only when explicitly sourced from the retired
-- Profiles.  Any dependency edge from another operation is severed first;
-- unrelated operations are preserved.
UPDATE operation_targets
SET depends_on_target_id = NULL
WHERE depends_on_target_id IN (SELECT id FROM toolhub_text_processing_operation_targets);
DELETE FROM operation_targets
WHERE id IN (SELECT id FROM toolhub_text_processing_operation_targets);
DELETE FROM backups
WHERE source_operation_id IN (SELECT id FROM toolhub_text_processing_operations);
DELETE FROM operations
WHERE id IN (SELECT id FROM toolhub_text_processing_operations);

UPDATE relay_configuration_state
SET default_profile_id = NULL,
    legacy_profile_id = NULL,
    updated_at = now()
WHERE singleton
  AND (default_profile_id IN (SELECT id FROM toolhub_text_processing_profiles)
       OR legacy_profile_id IN (SELECT id FROM toolhub_text_processing_profiles));

-- Profile-scoped audit rows are historical Profile records, not account or
-- security events.  Operation audit rows are removed only for the operations
-- captured above; unrelated audit history remains intact.
DELETE FROM audit_events
WHERE (resource_type = 'profile' AND resource_id IN (SELECT id::text FROM toolhub_text_processing_profiles))
   OR metadata->>'profileId' IN (SELECT id::text FROM toolhub_text_processing_profiles)
   OR metadata->>'profileRevisionId' IN (SELECT id::text FROM toolhub_text_processing_revisions)
   OR metadata->>'operationId' IN (SELECT id::text FROM toolhub_text_processing_operations);

-- The child membership tables have immutable mutation guards in deployed
-- schemas.  They are disabled only for this exact maintenance transaction and
-- restored before the migration commits.
DELETE FROM profile_revision_skills
WHERE profile_revision_id IN (SELECT id FROM toolhub_text_processing_revisions);
DELETE FROM profile_revision_mcp_servers
WHERE profile_revision_id IN (SELECT id FROM toolhub_text_processing_revisions);
DELETE FROM profile_revision_mcp_governance
WHERE profile_revision_id IN (SELECT id FROM toolhub_text_processing_revisions);
DELETE FROM profile_revision_tool_rules
WHERE profile_revision_id IN (SELECT id FROM toolhub_text_processing_revisions);

DELETE FROM profiles
WHERE id IN (SELECT id FROM toolhub_text_processing_profiles)
  AND archived_at IS NOT NULL;

ALTER TABLE profile_revision_skills ENABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_servers ENABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_governance ENABLE TRIGGER USER;
ALTER TABLE profile_revision_tool_rules ENABLE TRIGGER USER;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM profiles
        WHERE name IN ('claude-text-processing', 'codex-text-processing')
    ) THEN
        RAISE EXCEPTION 'text-processing Profile cleanup incomplete';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM profile_revisions pr
        WHERE pr.profile_id IN (SELECT id FROM toolhub_text_processing_profiles)
    ) THEN
        RAISE EXCEPTION 'text-processing Profile history cleanup incomplete';
    END IF;
END $$;
