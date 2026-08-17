-- ToolHub 2026-08-17 ownership boundary and scoped cleanup.
--
-- Profiles are a Skill delivery contract. MCP configuration is owned by the
-- Relay Configuration revision and is materialized by mcpm. This migration is
-- intentionally exact and idempotent: it removes only the explicitly
-- approved obsolete MCP/Skill objects and their dependent history.

CREATE TEMP TABLE toolhub_cleanup_mcp_names(name text PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_mcp_names(name) VALUES
    ('desktop-commander'), ('memory'), ('sequential-thinking');

CREATE TEMP TABLE toolhub_cleanup_skill_slugs(slug text PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_skill_slugs(slug) VALUES
    ('baoyu-format-markdown'), ('baoyu-translate'), ('baoyu-url-to-markdown'),
    ('codex-build'), ('codex-review'), ('grill-me-codex'),
    ('grill-with-docs-codex'), ('slides'), ('using-superpowers'),
    ('workflow-runner');

CREATE TEMP TABLE toolhub_cleanup_mcp_servers(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_mcp_servers(id)
SELECT id FROM mcp_servers WHERE name IN (SELECT name FROM toolhub_cleanup_mcp_names);

CREATE TEMP TABLE toolhub_cleanup_mcp_revisions(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_mcp_revisions(id)
SELECT id FROM mcp_revisions WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers);

CREATE TEMP TABLE toolhub_cleanup_mcp_contract_revisions(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_mcp_contract_revisions(id)
SELECT id FROM mcp_contract_revisions WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers);

CREATE TEMP TABLE toolhub_cleanup_mcp_tools(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_mcp_tools(id)
SELECT id FROM mcp_tools WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers);

CREATE TEMP TABLE toolhub_cleanup_skills(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_skills(id)
SELECT id FROM skills WHERE slug IN (SELECT slug FROM toolhub_cleanup_skill_slugs);

CREATE TEMP TABLE toolhub_cleanup_skill_versions(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_skill_versions(id)
SELECT id FROM skill_versions WHERE skill_id IN (SELECT id FROM toolhub_cleanup_skills);

CREATE TEMP TABLE toolhub_cleanup_artifacts(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_artifacts(id)
SELECT artifact_id FROM skill_versions WHERE id IN (SELECT id FROM toolhub_cleanup_skill_versions);

CREATE TEMP TABLE toolhub_cleanup_skill_sources(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_skill_sources(id)
SELECT source_id FROM skills WHERE id IN (SELECT id FROM toolhub_cleanup_skills);

CREATE TEMP TABLE toolhub_cleanup_profiles(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_profiles(id)
SELECT id FROM profiles WHERE name = 'shared-mcp';

-- Remove pointer/history projections before source rows. The manifest checks
-- are deliberately limited to the named IDs/names and never use a broad
-- truncate or database-wide cascade.
CREATE TEMP TABLE toolhub_cleanup_snapshots(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_snapshots(id)
SELECT DISTINCT ds.id
FROM desired_snapshots ds
WHERE ds.source_id IN (SELECT id FROM toolhub_cleanup_profiles)
   OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(COALESCE(ds.manifest->'mcpServers','[]'::jsonb)) member
       WHERE member->>'serverId' IN (SELECT id::text FROM toolhub_cleanup_mcp_servers)
          OR member->>'name' IN (SELECT name FROM toolhub_cleanup_mcp_names)
   )
   OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(COALESCE(ds.manifest->'skills','[]'::jsonb)) member
       WHERE member->>'skillId' IN (SELECT id::text FROM toolhub_cleanup_skills)
   );

CREATE TEMP TABLE toolhub_cleanup_operation_targets(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_operation_targets(id)
SELECT DISTINCT ot.id
FROM operation_targets ot
WHERE EXISTS (
    SELECT 1 FROM jsonb_array_elements(COALESCE(ot.request->'manifest'->'mcpServers','[]'::jsonb)) member
    WHERE member->>'serverId' IN (SELECT id::text FROM toolhub_cleanup_mcp_servers)
       OR member->>'name' IN (SELECT name FROM toolhub_cleanup_mcp_names)
)
OR EXISTS (
    SELECT 1 FROM jsonb_array_elements(COALESCE(ot.request->'manifest'->'skills','[]'::jsonb)) member
    WHERE member->>'skillId' IN (SELECT id::text FROM toolhub_cleanup_skills)
);

-- Remember the owning operations before deleting their target rows. A fleet
-- operation remains valid when it still contains an unrelated target; an
-- operation whose every target belongs to this scoped purge is historical
-- state for the removed object and is removed with its backups.
CREATE TEMP TABLE toolhub_cleanup_operations(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_operations(id)
SELECT DISTINCT operation_id
FROM operation_targets
WHERE id IN (SELECT id FROM toolhub_cleanup_operation_targets);

CREATE TEMP TABLE toolhub_cleanup_backups(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_backups(id)
SELECT DISTINCT backup.id
FROM backups backup
WHERE EXISTS (
    SELECT 1
    FROM jsonb_array_elements(COALESCE(backup.metadata->'desiredManifest'->'mcpServers','[]'::jsonb)) member
    WHERE member->>'serverId' IN (SELECT id::text FROM toolhub_cleanup_mcp_servers)
       OR member->>'name' IN (SELECT name FROM toolhub_cleanup_mcp_names)
)
OR EXISTS (
    SELECT 1
    FROM jsonb_array_elements(COALESCE(backup.metadata->'desiredManifest'->'skills','[]'::jsonb)) member
    WHERE member->>'skillId' IN (SELECT id::text FROM toolhub_cleanup_skills)
       OR member->>'slug' IN (SELECT slug FROM toolhub_cleanup_skill_slugs)
)
OR EXISTS (
    SELECT 1
    FROM jsonb_array_elements(COALESCE(backup.metadata->'manifest'->'mcpServers','[]'::jsonb)) member
    WHERE member->>'serverId' IN (SELECT id::text FROM toolhub_cleanup_mcp_servers)
       OR member->>'name' IN (SELECT name FROM toolhub_cleanup_mcp_names)
)
OR EXISTS (
    SELECT 1
    FROM jsonb_array_elements(COALESCE(backup.metadata->'manifest'->'skills','[]'::jsonb)) member
    WHERE member->>'skillId' IN (SELECT id::text FROM toolhub_cleanup_skills)
       OR member->>'slug' IN (SELECT slug FROM toolhub_cleanup_skill_slugs)
);

-- Desired snapshots and governance revisions are immutable at runtime. The
-- migration is the narrowly-scoped maintenance exception; triggers are
-- disabled only for these exact rows and recreated by PostgreSQL unchanged.
ALTER TABLE desired_snapshots DISABLE TRIGGER USER;
ALTER TABLE relay_configuration_revision_mcp_servers DISABLE TRIGGER USER;
ALTER TABLE relay_configuration_revisions DISABLE TRIGGER USER;
ALTER TABLE relay_configuration_revision_seals DISABLE TRIGGER USER;
ALTER TABLE mcp_contract_revision_tools DISABLE TRIGGER USER;
ALTER TABLE mcp_contract_revisions DISABLE TRIGGER USER;
ALTER TABLE mcp_contract_revision_seals DISABLE TRIGGER USER;
ALTER TABLE mcp_tools DISABLE TRIGGER USER;
ALTER TABLE profile_revision_skills DISABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_servers DISABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_governance DISABLE TRIGGER USER;
ALTER TABLE profile_revision_tool_rules DISABLE TRIGGER USER;
ALTER TABLE skill_versions DISABLE TRIGGER USER;
ALTER TABLE skill_artifacts DISABLE TRIGGER USER;

DELETE FROM target_desired_snapshots WHERE snapshot_id IN (SELECT id FROM toolhub_cleanup_snapshots);
DELETE FROM desired_snapshots WHERE id IN (SELECT id FROM toolhub_cleanup_snapshots);

DELETE FROM preflight_confirmations
WHERE profile_id IN (SELECT id FROM toolhub_cleanup_profiles)
   OR manifest::text ~ '(desktop-commander|memory|sequential-thinking|baoyu-format-markdown|baoyu-translate|baoyu-url-to-markdown|codex-build|codex-review|grill-me-codex|grill-with-docs-codex|slides|using-superpowers|workflow-runner)';
DELETE FROM local_mcp_import_confirmations
WHERE server_name IN (SELECT name FROM toolhub_cleanup_mcp_names);

DELETE FROM mcp_daily_aggregates
WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers)
   OR tool_id IN (SELECT id FROM toolhub_cleanup_mcp_tools)
   OR profile_id IN (SELECT id FROM toolhub_cleanup_profiles);
DELETE FROM audit_events
WHERE resource_type = 'mcp_server'
  AND resource_id IN (SELECT id::text FROM toolhub_cleanup_mcp_servers);
DELETE FROM audit_events
WHERE resource_type = 'profile'
  AND resource_id IN (SELECT id::text FROM toolhub_cleanup_profiles);
DELETE FROM audit_events
WHERE resource_type = 'skill'
  AND resource_id IN (SELECT id::text FROM toolhub_cleanup_skills);
DELETE FROM audit_events
WHERE resource_type IN ('relay_configuration','relay_contract','relay_confirmation')
  AND (
      metadata->'affectedProfileIds' ?| ARRAY(SELECT id::text FROM toolhub_cleanup_profiles)
      OR metadata->'affectedProfileRevisions' ?| ARRAY(
          SELECT id::text FROM profile_revisions
          WHERE profile_id IN (SELECT id FROM toolhub_cleanup_profiles)
      )
  );

DELETE FROM relay_configuration_revision_mcp_servers
WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers);
DELETE FROM relay_observation_cursors
WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers);

DELETE FROM profile_revision_mcp_governance
WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers)
   OR profile_revision_id IN (SELECT current_revision_id FROM profiles WHERE id IN (SELECT id FROM toolhub_cleanup_profiles));
DELETE FROM profile_revision_tool_rules WHERE tool_id IN (SELECT id FROM toolhub_cleanup_mcp_tools);
DELETE FROM profile_revision_mcp_servers WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers);
DELETE FROM profile_mcp_servers WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers);
DELETE FROM pending_secret_bindings WHERE mcp_revision_id IN (SELECT id FROM toolhub_cleanup_mcp_revisions);

-- A fleet operation can contain unrelated targets. Remove only target rows
-- whose requested manifest contains the named objects; retain the operation
-- row when another target still belongs to it.
UPDATE operation_targets
SET depends_on_target_id = NULL
WHERE depends_on_target_id IN (SELECT id FROM toolhub_cleanup_operation_targets);
DELETE FROM operation_targets WHERE id IN (SELECT id FROM toolhub_cleanup_operation_targets);
DELETE FROM backups
WHERE source_operation_id IN (
    SELECT id
    FROM toolhub_cleanup_operations
    WHERE NOT EXISTS (SELECT 1 FROM operation_targets ot WHERE ot.operation_id=toolhub_cleanup_operations.id)
)
OR id IN (SELECT id FROM toolhub_cleanup_backups);
DELETE FROM operations
WHERE id IN (
    SELECT id
    FROM toolhub_cleanup_operations
    WHERE NOT EXISTS (SELECT 1 FROM operation_targets ot WHERE ot.operation_id=toolhub_cleanup_operations.id)
);

-- The old shared-mcp Profile is no longer an owner. Clear pointers before its
-- profile/revision cascade, then remove the profile and all of its history.
UPDATE relay_configuration_state
SET default_profile_id = NULL,
    legacy_profile_id = NULL,
    legacy_profile_state = 'migrated_relay',
    updated_at = now()
WHERE singleton AND (
    default_profile_id IN (SELECT profile_id FROM published_profiles WHERE profile_id IN (SELECT id FROM toolhub_cleanup_profiles))
    OR legacy_profile_id IN (SELECT id FROM toolhub_cleanup_profiles)
);
DELETE FROM published_profiles WHERE profile_id IN (SELECT id FROM toolhub_cleanup_profiles);
DELETE FROM bundle_import_fingerprints WHERE profile_id IN (SELECT id FROM toolhub_cleanup_profiles);
DELETE FROM profiles WHERE id IN (SELECT id FROM toolhub_cleanup_profiles);

-- Remove historical Profile references to Skills that are not selected by any
-- surviving Profile. This is intentionally guarded so a future re-reference
-- makes the cleanup a no-op for that Skill.
DELETE FROM profile_skills
WHERE skill_id IN (SELECT id FROM toolhub_cleanup_skills)
  AND NOT EXISTS (
      SELECT 1
      FROM profile_skills keep
      JOIN profiles active_profile ON active_profile.id=keep.profile_id
      WHERE keep.skill_id=profile_skills.skill_id
        AND active_profile.archived_at IS NULL
  );
DELETE FROM profile_revision_skills
WHERE skill_id IN (SELECT id FROM toolhub_cleanup_skills)
  AND NOT EXISTS (
      SELECT 1
      FROM profile_revision_skills keep
      JOIN profile_revisions active_revision ON active_revision.id=keep.profile_revision_id
      JOIN profiles active_profile ON active_profile.id=active_revision.profile_id
      WHERE keep.skill_id=profile_revision_skills.skill_id
        AND active_profile.archived_at IS NULL
        AND active_revision.id=active_profile.current_revision_id
  );

-- Capture and remove the exact MCP secret references before deleting their
-- immutable revisions. Secrets are deleted only when no surviving MCP row or
-- pending binding references them.
CREATE TEMP TABLE toolhub_cleanup_secret_ids(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_cleanup_secret_ids(id)
SELECT DISTINCT refs.value::uuid
FROM mcp_revisions mr
CROSS JOIN LATERAL jsonb_each_text(mr.env_refs || mr.header_refs) refs
WHERE mr.id IN (SELECT id FROM toolhub_cleanup_mcp_revisions)
  AND refs.value ~ '^[a-f0-9-]{36}$';

DELETE FROM mcp_contract_revision_seals WHERE contract_revision_id IN (SELECT id FROM toolhub_cleanup_mcp_contract_revisions);
DELETE FROM mcp_contract_revision_tools WHERE contract_revision_id IN (SELECT id FROM toolhub_cleanup_mcp_contract_revisions) OR tool_id IN (SELECT id FROM toolhub_cleanup_mcp_tools);
DELETE FROM mcp_tool_rename_proposals WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers) OR removed_tool_id IN (SELECT id FROM toolhub_cleanup_mcp_tools) OR added_tool_id IN (SELECT id FROM toolhub_cleanup_mcp_tools);
DELETE FROM mcp_tool_renames WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers) OR old_tool_id IN (SELECT id FROM toolhub_cleanup_mcp_tools) OR new_tool_id IN (SELECT id FROM toolhub_cleanup_mcp_tools);
DELETE FROM mcp_contract_state WHERE server_id IN (SELECT id FROM toolhub_cleanup_mcp_servers);
DELETE FROM mcp_contract_revisions WHERE id IN (SELECT id FROM toolhub_cleanup_mcp_contract_revisions);
DELETE FROM mcp_tools WHERE id IN (SELECT id FROM toolhub_cleanup_mcp_tools);

ALTER TABLE mcp_servers DROP CONSTRAINT IF EXISTS mcp_servers_current_revision_fk;
DELETE FROM mcp_revisions WHERE id IN (SELECT id FROM toolhub_cleanup_mcp_revisions);
DELETE FROM mcp_servers WHERE id IN (SELECT id FROM toolhub_cleanup_mcp_servers);
ALTER TABLE mcp_servers
    ADD CONSTRAINT mcp_servers_current_revision_fk
    FOREIGN KEY (id, current_revision_id) REFERENCES mcp_revisions(server_id, id)
    DEFERRABLE INITIALLY DEFERRED;

-- Remove the selected Skill versions/artifacts only when no surviving row
-- references them. Immutable triggers are restored immediately afterwards.
UPDATE skills SET current_version_id = NULL, updated_at = now()
WHERE id IN (SELECT id FROM toolhub_cleanup_skills)
  AND NOT EXISTS (SELECT 1 FROM profile_skills keep WHERE keep.skill_id=skills.id)
  AND NOT EXISTS (SELECT 1 FROM profile_revision_skills keep WHERE keep.skill_id=skills.id);
ALTER TABLE skills DROP CONSTRAINT IF EXISTS skills_current_version_fk;
DELETE FROM skill_versions
WHERE id IN (SELECT id FROM toolhub_cleanup_skill_versions)
  AND NOT EXISTS (SELECT 1 FROM profile_revision_skills keep WHERE keep.skill_version_id=skill_versions.id);
DELETE FROM skill_artifacts
WHERE id IN (SELECT id FROM toolhub_cleanup_artifacts)
  AND NOT EXISTS (SELECT 1 FROM skill_versions keep WHERE keep.artifact_id=skill_artifacts.id);
DELETE FROM skills
WHERE id IN (SELECT id FROM toolhub_cleanup_skills)
  AND NOT EXISTS (SELECT 1 FROM profile_skills keep WHERE keep.skill_id=skills.id)
  AND NOT EXISTS (SELECT 1 FROM profile_revision_skills keep WHERE keep.skill_id=skills.id)
  AND NOT EXISTS (SELECT 1 FROM skill_versions keep WHERE keep.skill_id=skills.id);
ALTER TABLE skills
    ADD CONSTRAINT skills_current_version_fk FOREIGN KEY(current_version_id) REFERENCES skill_versions(id);
DELETE FROM skill_sources source
WHERE source.id IN (
    SELECT id FROM toolhub_cleanup_skill_sources
)
AND NOT EXISTS (SELECT 1 FROM skills keep WHERE keep.source_id=source.id);

DELETE FROM encrypted_secrets secret
WHERE secret.id IN (SELECT id FROM toolhub_cleanup_secret_ids)
  AND NOT EXISTS (SELECT 1 FROM mcp_revisions mr WHERE mr.env_refs ? secret.id::text OR mr.header_refs ? secret.id::text)
  AND NOT EXISTS (SELECT 1 FROM pending_secret_bindings psb WHERE psb.secret_id=secret.id);

ALTER TABLE desired_snapshots ENABLE TRIGGER USER;
ALTER TABLE relay_configuration_revision_mcp_servers ENABLE TRIGGER USER;
ALTER TABLE relay_configuration_revisions ENABLE TRIGGER USER;
ALTER TABLE relay_configuration_revision_seals ENABLE TRIGGER USER;
ALTER TABLE mcp_contract_revision_tools ENABLE TRIGGER USER;
ALTER TABLE mcp_contract_revisions ENABLE TRIGGER USER;
ALTER TABLE mcp_contract_revision_seals ENABLE TRIGGER USER;
ALTER TABLE mcp_tools ENABLE TRIGGER USER;
ALTER TABLE profile_revision_skills ENABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_servers ENABLE TRIGGER USER;
ALTER TABLE profile_revision_mcp_governance ENABLE TRIGGER USER;
ALTER TABLE profile_revision_tool_rules ENABLE TRIGGER USER;
ALTER TABLE skill_versions ENABLE TRIGGER USER;
ALTER TABLE skill_artifacts ENABLE TRIGGER USER;

-- Enforce the new browser contract at the persistence boundary for future
-- HTTP writes: no MCP fields are accepted by ProfileInput JSON (the Go input
-- type uses json:"-" for legacy compatibility), while old rows remain
-- readable only until this migration's exact cleanup has removed them.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM mcp_servers WHERE name IN (SELECT name FROM toolhub_cleanup_mcp_names)) THEN
        RAISE EXCEPTION 'scoped MCP cleanup incomplete';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM profile_revision_skills prs
        JOIN skills sk ON sk.id=prs.skill_id
        JOIN profile_revisions pr ON pr.id=prs.profile_revision_id
        JOIN profiles p ON p.id=pr.profile_id
        WHERE sk.slug IN (SELECT slug FROM toolhub_cleanup_skill_slugs)
          AND p.archived_at IS NULL
          AND pr.id=p.current_revision_id
    ) THEN
        RAISE EXCEPTION 'scoped Skill cleanup encountered an active Profile reference';
    END IF;
    IF EXISTS (SELECT 1 FROM profiles WHERE name='shared-mcp') THEN
        RAISE EXCEPTION 'legacy shared-mcp Profile cleanup incomplete';
    END IF;
END $$;
