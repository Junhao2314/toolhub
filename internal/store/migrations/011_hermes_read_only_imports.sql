-- Hermes is an observation/import source only. Persist explicit snapshot state
-- without turning an inventory row into central desired state.
ALTER TABLE nodes
    ADD COLUMN agent_capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT nodes_agent_capabilities_array_check
        CHECK (jsonb_typeof(agent_capabilities) = 'array');

ALTER TABLE skill_sources DROP CONSTRAINT skill_sources_kind_check;
ALTER TABLE skill_sources ADD CONSTRAINT skill_sources_kind_check
    CHECK (kind IN ('upload','git','skillsmp','openai','node','xiaping','hermes-import'));

ALTER TABLE skill_discoveries
    ADD COLUMN control_mode text NOT NULL DEFAULT 'managed_target',
    ADD COLUMN source_changed boolean NOT NULL DEFAULT false,
    ADD COLUMN import_status text NOT NULL DEFAULT 'not_applicable',
    ADD COLUMN import_error text NOT NULL DEFAULT '',
    ADD COLUMN import_job_id uuid REFERENCES jobs(id) ON DELETE SET NULL,
    ADD COLUMN imported_skill_id uuid REFERENCES skills(id) ON DELETE SET NULL,
    ADD COLUMN imported_version_id uuid REFERENCES skill_versions(id) ON DELETE SET NULL,
    ADD COLUMN last_imported_sha256 text NOT NULL DEFAULT '',
    ADD COLUMN last_imported_at timestamptz;

UPDATE skill_discoveries AS discovery
SET control_mode = 'read_only_source',
    managed = false,
    drift = false,
    imported_skill_id = discovery.adopted_skill_id,
    imported_version_id = discovery.adopted_version_id,
    last_imported_sha256 = coalesce(version.content_sha256, ''),
    last_imported_at = CASE WHEN discovery.adopted_version_id IS NOT NULL THEN discovery.updated_at ELSE NULL END,
    import_status = CASE WHEN discovery.adopted_version_id IS NOT NULL THEN 'imported' ELSE 'available' END,
    source_changed = CASE WHEN version.content_sha256 IS NOT NULL THEN discovery.directory_hash <> version.content_sha256 ELSE false END
FROM skill_versions AS version
WHERE discovery.runtime_kind = 'hermes'
  AND version.id = discovery.adopted_version_id;

UPDATE skill_discoveries
SET control_mode = 'read_only_source', managed = false, drift = false,
    import_status = CASE WHEN imported_version_id IS NOT NULL THEN 'imported' ELSE 'available' END
WHERE runtime_kind = 'hermes' AND control_mode <> 'read_only_source';

ALTER TABLE skill_discoveries
    ADD CONSTRAINT skill_discoveries_control_mode_check
        CHECK (control_mode IN ('managed_target','read_only_source')),
    ADD CONSTRAINT skill_discoveries_hermes_control_check
        CHECK ((runtime_kind = 'hermes') = (control_mode = 'read_only_source')),
    ADD CONSTRAINT skill_discoveries_import_status_check
        CHECK (import_status IN ('not_applicable','available','queued','importing','imported','failed'));

CREATE INDEX skill_discoveries_import_idx
    ON skill_discoveries(node_id, runtime_kind, import_status, source_changed)
    WHERE control_mode = 'read_only_source';

ALTER TABLE mcp_runtime_bindings
    ADD COLUMN control_mode text NOT NULL DEFAULT 'managed_target',
    ADD COLUMN descriptor jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN observed_generation bigint NOT NULL DEFAULT 1,
    ADD COLUMN pinned_generation bigint,
    ADD COLUMN source_changed boolean NOT NULL DEFAULT false,
    ADD COLUMN import_status text NOT NULL DEFAULT 'not_applicable',
    ADD COLUMN import_error text NOT NULL DEFAULT '',
    ADD COLUMN import_job_id uuid REFERENCES jobs(id) ON DELETE SET NULL,
    ADD COLUMN last_imported_config_fingerprint text NOT NULL DEFAULT '',
    ADD COLUMN last_imported_secret_fingerprint text NOT NULL DEFAULT '',
    ADD COLUMN last_imported_generation bigint,
    ADD COLUMN last_imported_server_id uuid REFERENCES mcp_servers(id) ON DELETE SET NULL,
    ADD COLUMN last_imported_at timestamptz;

UPDATE mcp_runtime_bindings AS binding
SET descriptor = jsonb_build_object(
        'name', binding.server_name,
        'identity', binding.identity,
        'transport', coalesce(server.transport, 'stdio'),
        'command', coalesce(server.command, ''),
        'args', coalesce(server.args, '[]'::jsonb),
        'url', coalesce(server.url, ''),
        'envKeys', binding.env_keys,
        'headerKeys', binding.header_keys,
        'configFingerprint', binding.observed_config_fingerprint,
        'secretFingerprint', binding.observed_secret_fingerprint
    )
FROM mcp_servers AS server
WHERE binding.server_id = server.id;

UPDATE mcp_runtime_bindings
SET control_mode = 'read_only_source',
    desired_enabled = false,
    drift = false,
    import_status = CASE WHEN shared_source_id IS NOT NULL THEN 'not_applicable' WHEN server_id IS NOT NULL THEN 'imported' ELSE 'available' END,
    last_imported_config_fingerprint = CASE WHEN server_id IS NOT NULL AND shared_source_id IS NULL THEN observed_config_fingerprint ELSE '' END,
    last_imported_secret_fingerprint = CASE WHEN server_id IS NOT NULL AND shared_source_id IS NULL THEN observed_secret_fingerprint ELSE '' END,
    last_imported_generation = CASE WHEN server_id IS NOT NULL AND shared_source_id IS NULL THEN observed_generation ELSE NULL END,
    last_imported_server_id = CASE WHEN shared_source_id IS NULL THEN server_id ELSE NULL END,
    last_imported_at = CASE WHEN server_id IS NOT NULL AND shared_source_id IS NULL THEN updated_at ELSE NULL END
WHERE runtime_kind = 'hermes';

ALTER TABLE mcp_runtime_bindings
    ADD CONSTRAINT mcp_runtime_bindings_control_mode_check
        CHECK (control_mode IN ('managed_target','read_only_source')),
    ADD CONSTRAINT mcp_runtime_bindings_hermes_control_check
        CHECK ((runtime_kind = 'hermes') = (control_mode = 'read_only_source')),
    ADD CONSTRAINT mcp_runtime_bindings_observed_generation_check
        CHECK (observed_generation >= 1),
    ADD CONSTRAINT mcp_runtime_bindings_pinned_generation_check
        CHECK (pinned_generation IS NULL OR pinned_generation >= 1),
    ADD CONSTRAINT mcp_runtime_bindings_imported_generation_check
        CHECK (last_imported_generation IS NULL OR last_imported_generation >= 1),
    ADD CONSTRAINT mcp_runtime_bindings_import_status_check
        CHECK (import_status IN ('not_applicable','available','queued','importing','imported','failed'));

CREATE INDEX mcp_runtime_bindings_import_idx
    ON mcp_runtime_bindings(node_id, import_status, observed_generation)
    WHERE control_mode = 'read_only_source';

ALTER TABLE mcp_capture_tokens
    ADD COLUMN purpose text NOT NULL DEFAULT 'runtime_baseline',
    ADD COLUMN discovery_binding_id uuid REFERENCES mcp_runtime_bindings(id) ON DELETE CASCADE,
    ADD CONSTRAINT mcp_capture_tokens_purpose_check
        CHECK (purpose IN ('runtime_baseline','mcp_import','hermes_snapshot')),
    ADD CONSTRAINT mcp_capture_tokens_hermes_binding_check
        CHECK ((purpose = 'hermes_snapshot') = (discovery_binding_id IS NOT NULL));

UPDATE mcp_capture_tokens
SET used_at = coalesce(used_at, now())
WHERE runtime_kind = 'hermes';

-- Preserve historical rows while making every existing Hermes target inert.
ALTER TABLE deployments DROP CONSTRAINT deployments_state_check;
ALTER TABLE deployments ADD CONSTRAINT deployments_state_check
    CHECK (state IN ('pending','in_sync','drift','conflict','failed','rolling_back','archived','legacy_read_only'));
UPDATE deployments
SET desired_enabled = false, state = 'legacy_read_only', updated_at = now()
WHERE runtime_kind = 'hermes';
ALTER TABLE deployments ADD CONSTRAINT deployments_hermes_read_only_check
    CHECK (runtime_kind <> 'hermes' OR state IN ('legacy_read_only','archived'));

UPDATE mcp_deployments
SET desired_enabled = false, state = 'legacy_read_only', updated_at = now()
WHERE runtime_kind = 'hermes';
ALTER TABLE mcp_deployments ADD CONSTRAINT mcp_deployments_hermes_read_only_check
    CHECK (runtime_kind <> 'hermes' OR state IN ('legacy_read_only','archived'));

ALTER TABLE toolhub_profile_activations DROP CONSTRAINT toolhub_profile_activations_state_check;
ALTER TABLE toolhub_profile_activations ADD CONSTRAINT toolhub_profile_activations_state_check
    CHECK (state IN ('pending','active','partial','failed','legacy_read_only','archived'));
UPDATE toolhub_profile_activations
SET state = 'legacy_read_only', last_error = 'Hermes is a read-only import source', updated_at = now()
WHERE runtime_kind = 'hermes';
ALTER TABLE toolhub_profile_activations ADD CONSTRAINT toolhub_profile_activations_hermes_read_only_check
    CHECK (runtime_kind <> 'hermes' OR state IN ('legacy_read_only','archived'));

CREATE FUNCTION reject_new_hermes_target() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.runtime_kind = 'hermes' THEN
        RAISE EXCEPTION 'Hermes is a read-only import source' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER deployments_reject_new_hermes
    BEFORE INSERT ON deployments FOR EACH ROW EXECUTE FUNCTION reject_new_hermes_target();
CREATE TRIGGER mcp_deployments_reject_new_hermes
    BEFORE INSERT ON mcp_deployments FOR EACH ROW EXECUTE FUNCTION reject_new_hermes_target();
CREATE TRIGGER profile_activations_reject_new_hermes
    BEFORE INSERT ON toolhub_profile_activations FOR EACH ROW EXECUTE FUNCTION reject_new_hermes_target();

UPDATE node_tasks
SET status = 'cancelled',
    cancel_requested_at = coalesce(cancel_requested_at, now()),
    finished_at = coalesce(finished_at, now()),
    lease_owner = NULL,
    lease_expires_at = NULL,
    result = jsonb_build_object('error', 'Hermes writer task cancelled during read-only migration', 'code', 'hermes_read_only'),
    updated_at = now()
WHERE status IN ('pending','delivered','running')
  AND (
      (kind IN ('deploy_skill','apply_mcp','adopt_skill') AND payload->>'runtime' = 'hermes')
      OR (kind = 'adopt_skill' AND EXISTS (
          SELECT 1 FROM skill_discoveries discovery
          WHERE discovery.id::text = node_tasks.payload->>'discoveryId' AND discovery.runtime_kind = 'hermes'
      ))
  );

UPDATE jobs
SET status = 'cancelled', cancel_requested_at = coalesce(cancel_requested_at, now()),
    finished_at = coalesce(finished_at, now()),
    lease_owner = NULL,
    lease_expires_at = NULL,
    result = jsonb_build_object('error', 'Hermes adoption cancelled during read-only migration', 'code', 'hermes_read_only')
WHERE status IN ('pending','running') AND kind = 'skill_adopt'
  AND EXISTS (
      SELECT 1 FROM skill_discoveries discovery
      WHERE discovery.id::text = jobs.payload->>'discoveryId' AND discovery.runtime_kind = 'hermes'
  );

UPDATE jobs
SET status = 'cancelled', cancel_requested_at = coalesce(cancel_requested_at, now()),
    finished_at = coalesce(finished_at, now()), lease_owner = NULL, lease_expires_at = NULL,
    result = jsonb_build_object('error', 'Hermes Profile activation cancelled during read-only migration', 'code', 'hermes_read_only')
WHERE status IN ('pending','running') AND kind = 'profile_activate' AND payload->>'runtime' = 'hermes';
