-- Retire the last Profile-owned MCP state. Profiles are Skill-only; the
-- Relay Configuration revision is the sole ToolHub MCP configuration owner.
-- This migration removes only legacy Profile MCP manifests/governance and
-- their dependent transient history. Current Skill Profile revisions and the
-- active mcpm Relay Configuration remain intact.

CREATE TEMP TABLE toolhub_legacy_profile_mcp_targets(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_legacy_profile_mcp_targets(id)
SELECT DISTINCT ot.id
FROM operation_targets ot
WHERE ot.request->>'sourceKind'='profile_apply'
  AND jsonb_array_length(COALESCE(ot.request->'manifest'->'mcpServers','[]'::jsonb)) > 0;

CREATE TEMP TABLE toolhub_legacy_profile_mcp_operations(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_legacy_profile_mcp_operations(id)
SELECT DISTINCT operation_id
FROM operation_targets
WHERE id IN (SELECT id FROM toolhub_legacy_profile_mcp_targets);

CREATE TEMP TABLE toolhub_legacy_profile_mcp_snapshots(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_legacy_profile_mcp_snapshots(id)
SELECT DISTINCT ds.id
FROM desired_snapshots ds
WHERE ds.source_operation_target_id IN (SELECT id FROM toolhub_legacy_profile_mcp_targets)
   OR (ds.source_kind='profile_apply'
       AND jsonb_array_length(COALESCE(ds.manifest->'mcpServers','[]'::jsonb)) > 0);

CREATE TEMP TABLE toolhub_legacy_profile_mcp_backups(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_legacy_profile_mcp_backups(id)
SELECT DISTINCT b.id
FROM backups b
WHERE b.source_operation_id IN (SELECT id FROM toolhub_legacy_profile_mcp_operations)
   OR jsonb_array_length(COALESCE(b.metadata->'desiredManifest'->'mcpServers','[]'::jsonb)) > 0
   OR jsonb_array_length(COALESCE(b.metadata->'manifest'->'mcpServers','[]'::jsonb)) > 0;

CREATE TEMP TABLE toolhub_legacy_profile_mcp_preflights(token_hash bytea PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_legacy_profile_mcp_preflights(token_hash)
SELECT token_hash
FROM preflight_confirmations
WHERE jsonb_array_length(COALESCE(manifest->'mcpServers','[]'::jsonb)) > 0;

ALTER TABLE desired_snapshots DISABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_governance DISABLE TRIGGER USER;
ALTER TABLE profile_revision_tool_rules DISABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_servers DISABLE TRIGGER USER;
ALTER TABLE profile_mcp_servers DISABLE TRIGGER USER;

-- Immutable desired snapshots are removed before their target pointers and
-- source operation-target rows. This is a scoped maintenance exception; the
-- runtime immutability triggers remain enabled outside this transaction.
DELETE FROM target_desired_snapshots
WHERE snapshot_id IN (SELECT id FROM toolhub_legacy_profile_mcp_snapshots);
DELETE FROM desired_snapshots
WHERE id IN (SELECT id FROM toolhub_legacy_profile_mcp_snapshots);

DELETE FROM preflight_confirmations
WHERE token_hash IN (SELECT token_hash FROM toolhub_legacy_profile_mcp_preflights);

UPDATE operation_targets
SET depends_on_target_id=NULL
WHERE depends_on_target_id IN (SELECT id FROM toolhub_legacy_profile_mcp_targets);
DELETE FROM operation_targets
WHERE id IN (SELECT id FROM toolhub_legacy_profile_mcp_targets);

DELETE FROM backups
WHERE id IN (SELECT id FROM toolhub_legacy_profile_mcp_backups)
   OR (source_operation_id IN (SELECT id FROM toolhub_legacy_profile_mcp_operations)
       AND NOT EXISTS (
           SELECT 1 FROM operation_targets ot
           WHERE ot.operation_id=backups.source_operation_id
       ));
DELETE FROM operations
WHERE id IN (
    SELECT id
    FROM toolhub_legacy_profile_mcp_operations
    WHERE NOT EXISTS (
        SELECT 1 FROM operation_targets ot
        WHERE ot.operation_id=toolhub_legacy_profile_mcp_operations.id
    )
);

-- Profile MCP apply audit entries are tied to the removed operation IDs. Do
-- not remove ordinary Profile publish/audit records for surviving Skill-only
-- revisions.
DELETE FROM audit_events
WHERE metadata->>'operationId' IN (
    SELECT id::text FROM toolhub_legacy_profile_mcp_operations
);

-- These tables are retained for schema compatibility with old bundle/import
-- fixtures, but no live Profile may carry MCP membership, governance, tool
-- rules, or pending MCP secret bindings after this point.
DELETE FROM profile_revision_mcp_governance;
DELETE FROM profile_revision_tool_rules;
DELETE FROM profile_revision_mcp_servers;
DELETE FROM profile_mcp_servers;
DELETE FROM pending_secret_bindings;

ALTER TABLE desired_snapshots ENABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_governance ENABLE TRIGGER USER;
ALTER TABLE profile_revision_tool_rules ENABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_servers ENABLE TRIGGER USER;
ALTER TABLE profile_mcp_servers ENABLE TRIGGER USER;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM profile_revision_mcp_governance)
       OR EXISTS (SELECT 1 FROM profile_revision_tool_rules)
       OR EXISTS (SELECT 1 FROM profile_revision_mcp_servers)
       OR EXISTS (SELECT 1 FROM profile_mcp_servers)
       OR EXISTS (SELECT 1 FROM pending_secret_bindings) THEN
        RAISE EXCEPTION 'Profile MCP ownership cleanup incomplete';
    END IF;
    IF EXISTS (
        SELECT 1 FROM operation_targets
        WHERE request->>'sourceKind'='profile_apply'
          AND jsonb_array_length(COALESCE(request->'manifest'->'mcpServers','[]'::jsonb)) > 0
    ) THEN
        RAISE EXCEPTION 'legacy Profile MCP operation history remains';
    END IF;
    IF EXISTS (
        SELECT 1 FROM preflight_confirmations
        WHERE jsonb_array_length(COALESCE(manifest->'mcpServers','[]'::jsonb)) > 0
    ) THEN
        RAISE EXCEPTION 'legacy Profile MCP preflight remains';
    END IF;
END $$;
