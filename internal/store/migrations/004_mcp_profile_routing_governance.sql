-- Generation-2 governance is additive. Historical migrations remain unchanged.

ALTER TABLE profiles
    ADD COLUMN client_kind text NOT NULL DEFAULT 'unknown',
    ADD COLUMN category text NOT NULL DEFAULT '',
    ADD COLUMN variant text NOT NULL DEFAULT 'standard',
    ADD COLUMN migration_state text NOT NULL DEFAULT 'needs_review';

UPDATE profiles
SET client_kind = CASE
        WHEN name LIKE 'claude-%' AND length(name) > length('claude-') THEN 'claude'
        WHEN name LIKE 'codex-%' AND length(name) > length('codex-') THEN 'codex'
        WHEN name = 'shared-mcp' THEN 'shared'
        ELSE 'unknown'
    END,
    category = CASE
        WHEN name LIKE 'claude-%' AND length(name) > length('claude-') THEN substring(name FROM length('claude-') + 1)
        WHEN name LIKE 'codex-%' AND length(name) > length('codex-') THEN substring(name FROM length('codex-') + 1)
        WHEN name = 'shared-mcp' THEN 'relay'
        ELSE ''
    END,
    variant = 'standard',
    migration_state = CASE
        WHEN name LIKE 'claude-%' AND length(name) > length('claude-') THEN 'ready'
        WHEN name LIKE 'codex-%' AND length(name) > length('codex-') THEN 'ready'
        WHEN name = 'shared-mcp' THEN 'compatibility'
        ELSE 'needs_review'
    END;

ALTER TABLE profiles
    ADD CONSTRAINT profiles_client_kind_check CHECK (client_kind IN ('claude','codex','shared','unknown')),
    ADD CONSTRAINT profiles_category_check CHECK (length(category) <= 120),
    ADD CONSTRAINT profiles_variant_check CHECK (length(variant) BETWEEN 1 AND 64),
    ADD CONSTRAINT profiles_migration_state_check CHECK (migration_state IN ('ready','needs_review','compatibility'));

ALTER TABLE profile_revisions
    ADD COLUMN client_kind text NOT NULL DEFAULT 'unknown',
    ADD COLUMN category text NOT NULL DEFAULT '',
    ADD COLUMN variant text NOT NULL DEFAULT 'standard',
    ADD COLUMN migration_state text NOT NULL DEFAULT 'needs_review';

DROP TRIGGER profile_revisions_immutable ON profile_revisions;
UPDATE profile_revisions pr
SET client_kind = p.client_kind,
    category = p.category,
    variant = p.variant,
    migration_state = p.migration_state
FROM profiles p
WHERE p.id = pr.profile_id;
CREATE TRIGGER profile_revisions_immutable BEFORE UPDATE ON profile_revisions
FOR EACH ROW EXECUTE FUNCTION reject_profile_revision_update();

ALTER TABLE profile_revisions
    ADD CONSTRAINT profile_revisions_client_kind_check CHECK (client_kind IN ('claude','codex','shared','unknown')),
    ADD CONSTRAINT profile_revisions_category_check CHECK (length(category) <= 120),
    ADD CONSTRAINT profile_revisions_variant_check CHECK (length(variant) BETWEEN 1 AND 64),
    ADD CONSTRAINT profile_revisions_migration_state_check CHECK (migration_state IN ('ready','needs_review','compatibility'));

ALTER TABLE operations DROP CONSTRAINT IF EXISTS operations_kind_check;
ALTER TABLE operations ADD CONSTRAINT operations_kind_check CHECK (kind IN (
    'skill_import','mcp_import','update_check','apply','edit','restore','scan','refresh','reconcile',
    'relay_start','relay_stop','relay_restart','relay_health','backup_gc',
    'relay_config_apply','contract_observe','policy_apply','relay_telemetry_pull'
));

ALTER TABLE operation_targets ADD COLUMN depends_on_target_id uuid;
ALTER TABLE operation_targets
    ADD CONSTRAINT operation_targets_depends_on_fk
        FOREIGN KEY (depends_on_target_id) REFERENCES operation_targets(id),
    ADD CONSTRAINT operation_targets_not_self_dependent_check
        CHECK (depends_on_target_id IS NULL OR depends_on_target_id <> id);
CREATE INDEX operation_targets_dependency_idx ON operation_targets(depends_on_target_id);

CREATE FUNCTION validate_operation_target_dependency() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    dependency_operation uuid;
    cycle_found boolean;
BEGIN
    IF NEW.depends_on_target_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT operation_id INTO dependency_operation
    FROM operation_targets
    WHERE id = NEW.depends_on_target_id;
    IF dependency_operation IS NOT NULL AND dependency_operation <> NEW.operation_id THEN
        RAISE EXCEPTION 'operation target dependency must remain within one operation';
    END IF;
    WITH RECURSIVE dependency_chain(id) AS (
        SELECT NEW.depends_on_target_id
        UNION ALL
        SELECT ot.depends_on_target_id
        FROM operation_targets ot
        JOIN dependency_chain chain ON chain.id = ot.id
        WHERE ot.depends_on_target_id IS NOT NULL
    )
    SELECT EXISTS(SELECT 1 FROM dependency_chain WHERE id = NEW.id) INTO cycle_found;
    IF cycle_found THEN
        RAISE EXCEPTION 'operation target dependency cycle is not allowed';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER operation_targets_dependency_guard
BEFORE INSERT OR UPDATE OF depends_on_target_id, operation_id ON operation_targets
FOR EACH ROW EXECUTE FUNCTION validate_operation_target_dependency();

-- Revision/checksum validation is retained for v1 snapshots and dispatched to
-- a stricter v2 validator only for the shared relay target.
ALTER FUNCTION validate_desired_manifest(jsonb) RENAME TO validate_desired_manifest_v1;
ALTER TABLE desired_snapshots DROP CONSTRAINT desired_snapshots_manifest_schema_version_check;
ALTER TABLE desired_snapshots ADD CONSTRAINT desired_snapshots_manifest_schema_version_check CHECK (manifest_schema_version IN (1,2));
ALTER TABLE desired_snapshots DROP CONSTRAINT desired_snapshots_manifest_check;
ALTER TABLE desired_snapshots DROP CONSTRAINT desired_snapshots_source_kind_check;
ALTER TABLE desired_snapshots ADD CONSTRAINT desired_snapshots_source_kind_check CHECK (source_kind IN ('profile_apply','target_edit','restore','relay_config_apply'));

CREATE FUNCTION validate_desired_manifest(body jsonb) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    schema_version integer;
    governance jsonb;
    legacy_body jsonb;
BEGIN
    IF body IS NULL OR jsonb_typeof(body) <> 'object' OR jsonb_typeof(body->'schemaVersion') <> 'number' THEN
        RETURN false;
    END IF;
    schema_version := (body->>'schemaVersion')::integer;
    IF schema_version = 1 THEN
        RETURN validate_desired_manifest_v1(body);
    END IF;
    IF schema_version <> 2
       OR body - ARRAY['schemaVersion','target','profileId','profileRevision','skills','mcpServers','managedMemberIds','relayPort','relayGovernance'] <> '{}'::jsonb
       OR NOT (body ?& ARRAY['schemaVersion','target','skills','mcpServers','managedMemberIds','relayGovernance'])
       OR jsonb_typeof(body->'relayGovernance') <> 'object'
       OR jsonb_typeof(body->'target') <> 'object'
       OR body->'target'->>'runtime' <> 'shared-relay' THEN
        RETURN false;
    END IF;
    governance := body->'relayGovernance';
    IF governance - ARRAY['relayConfigurationRevisionId','relayConfigurationHash','routingBundleHash','profileRevisionId','profileRevisionHash','globalPolicyRevisionId','globalPolicyHash'] <> '{}'::jsonb
       OR NOT (governance ?& ARRAY['relayConfigurationRevisionId','relayConfigurationHash','routingBundleHash'])
       OR (SELECT count(*) FROM jsonb_object_keys(governance)) > 7
       OR governance->>'relayConfigurationRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
       OR governance->>'relayConfigurationHash' !~ '^[a-f0-9]{64}$'
       OR governance->>'routingBundleHash' !~ '^[a-f0-9]{64}$' THEN
        RETURN false;
    END IF;
    IF governance ? 'profileRevisionId' AND governance->>'profileRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$' THEN
        RETURN false;
    END IF;
    IF governance ? 'globalPolicyRevisionId' AND governance->>'globalPolicyRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$' THEN
        RETURN false;
    END IF;
    IF (governance ? 'profileRevisionHash' AND governance->>'profileRevisionHash' !~ '^[a-f0-9]{64}$')
       OR (governance ? 'globalPolicyHash' AND governance->>'globalPolicyHash' !~ '^[a-f0-9]{64}$') THEN
        RETURN false;
    END IF;
    legacy_body := jsonb_set(body - 'relayGovernance', '{schemaVersion}', '1'::jsonb, true);
    RETURN validate_desired_manifest_v1(legacy_body);
EXCEPTION WHEN others THEN
    RETURN false;
END $$;
ALTER TABLE desired_snapshots ADD CONSTRAINT desired_snapshots_manifest_check CHECK (validate_desired_manifest(manifest));

CREATE TABLE relay_configuration_revisions (
    id uuid PRIMARY KEY,
    revision bigint NOT NULL CHECK (revision > 0),
    canonical_hash text NOT NULL UNIQUE CHECK (canonical_hash ~ '^[a-f0-9]{64}$'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(revision)
);

CREATE TABLE mcp_contract_revisions (
    id uuid PRIMARY KEY,
    server_id uuid NOT NULL REFERENCES mcp_servers(id),
    revision bigint NOT NULL CHECK (revision > 0),
    canonical_hash text NOT NULL CHECK (canonical_hash ~ '^[a-f0-9]{64}$'),
    normalized_contract jsonb NOT NULL CHECK (jsonb_typeof(normalized_contract) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(server_id, revision),
    UNIQUE(server_id, canonical_hash)
);

CREATE TABLE mcp_tools (
    id uuid PRIMARY KEY,
    server_id uuid NOT NULL REFERENCES mcp_servers(id),
    name text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(server_id, name),
    UNIQUE(server_id, id)
);

CREATE TABLE mcp_contract_revision_tools (
    contract_revision_id uuid NOT NULL REFERENCES mcp_contract_revisions(id) ON DELETE CASCADE,
    tool_id uuid NOT NULL REFERENCES mcp_tools(id),
    position integer NOT NULL CHECK (position >= 0 AND position <= 500),
    input_schema jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_schema) = 'object'),
    output_schema jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(output_schema) = 'object'),
    annotations jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(annotations) = 'object'),
    presentation jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(presentation) = 'object'),
    PRIMARY KEY(contract_revision_id, tool_id),
    UNIQUE(contract_revision_id, position)
);

CREATE TABLE published_profiles (
    profile_id uuid PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE,
    profile_revision_id uuid NOT NULL REFERENCES profile_revisions(id),
    published_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(profile_revision_id)
);
ALTER TABLE published_profiles
    ADD CONSTRAINT published_profiles_profile_revision_owner_fk
        FOREIGN KEY(profile_id, profile_revision_id) REFERENCES profile_revisions(profile_id, id);

CREATE TABLE relay_configuration_revision_mcp_servers (
    relay_configuration_revision_id uuid NOT NULL REFERENCES relay_configuration_revisions(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES mcp_servers(id),
    mcp_revision_id uuid NOT NULL,
    position integer NOT NULL CHECK (position >= 0 AND position <= 500),
    PRIMARY KEY(relay_configuration_revision_id, server_id),
    UNIQUE(relay_configuration_revision_id, position),
    FOREIGN KEY(server_id, mcp_revision_id) REFERENCES mcp_revisions(server_id, id)
);

CREATE TABLE relay_configuration_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    current_revision_id uuid NOT NULL REFERENCES relay_configuration_revisions(id),
    applied_revision_id uuid NOT NULL REFERENCES relay_configuration_revisions(id),
    default_profile_id uuid REFERENCES published_profiles(profile_id),
    mode text NOT NULL DEFAULT 'compatibility' CHECK (mode IN ('compatibility','enforced')),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mcp_contract_state (
    server_id uuid PRIMARY KEY REFERENCES mcp_servers(id) ON DELETE CASCADE,
    latest_revision_id uuid REFERENCES mcp_contract_revisions(id),
    accepted_revision_id uuid REFERENCES mcp_contract_revisions(id),
    review_state text NOT NULL DEFAULT 'unreviewed' CHECK (review_state IN ('unreviewed','accepted','changed','paused')),
    updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE mcp_contract_revisions ADD CONSTRAINT mcp_contract_revisions_server_id_id_unique UNIQUE(server_id, id);
ALTER TABLE mcp_contract_state
    DROP CONSTRAINT mcp_contract_state_latest_revision_id_fkey,
    DROP CONSTRAINT mcp_contract_state_accepted_revision_id_fkey,
    ADD CONSTRAINT mcp_contract_state_latest_revision_fk
        FOREIGN KEY(server_id, latest_revision_id) REFERENCES mcp_contract_revisions(server_id, id),
    ADD CONSTRAINT mcp_contract_state_accepted_revision_fk
        FOREIGN KEY(server_id, accepted_revision_id) REFERENCES mcp_contract_revisions(server_id, id);

CREATE TABLE mcp_tool_rename_proposals (
    id uuid PRIMARY KEY,
    server_id uuid NOT NULL REFERENCES mcp_servers(id),
    removed_tool_id uuid NOT NULL REFERENCES mcp_tools(id),
    added_tool_id uuid NOT NULL REFERENCES mcp_tools(id),
    removed_contract_revision_id uuid NOT NULL REFERENCES mcp_contract_revisions(id),
    added_contract_revision_id uuid NOT NULL REFERENCES mcp_contract_revisions(id),
    status text NOT NULL CHECK (status IN ('suspected','confirmed','rejected','ambiguous')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(server_id, removed_tool_id, added_tool_id, removed_contract_revision_id, added_contract_revision_id)
);

CREATE TABLE mcp_tool_renames (
    id uuid PRIMARY KEY,
    server_id uuid NOT NULL REFERENCES mcp_servers(id),
    old_tool_id uuid NOT NULL REFERENCES mcp_tools(id),
    new_tool_id uuid NOT NULL REFERENCES mcp_tools(id),
    confirmed_removed_contract_revision_id uuid NOT NULL REFERENCES mcp_contract_revisions(id),
    confirmed_added_contract_revision_id uuid NOT NULL REFERENCES mcp_contract_revisions(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(server_id, old_tool_id, new_tool_id)
);

CREATE TABLE global_policy_revisions (
    id uuid PRIMARY KEY,
    revision bigint NOT NULL CHECK (revision > 0),
    canonical_hash text NOT NULL UNIQUE CHECK (canonical_hash ~ '^[a-f0-9]{64}$'),
    catalog_version integer NOT NULL CHECK (catalog_version > 0 AND catalog_version <= 100),
    explicit_overrides jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(explicit_overrides) = 'object'),
    unclassified_mutating text NOT NULL CHECK (unclassified_mutating IN ('deny','confirm','allow')),
    reviewed_read_only text NOT NULL CHECK (reviewed_read_only IN ('deny','confirm','allow')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(revision)
);

CREATE TABLE global_policy_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    current_revision_id uuid NOT NULL REFERENCES global_policy_revisions(id),
    applied_revision_id uuid NOT NULL REFERENCES global_policy_revisions(id),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE profile_revision_mcp_governance (
    profile_revision_id uuid NOT NULL REFERENCES profile_revisions(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES mcp_servers(id),
    mcp_revision_id uuid NOT NULL,
    accepted_contract_revision_id uuid REFERENCES mcp_contract_revisions(id),
    visibility_mode text NOT NULL CHECK (visibility_mode IN ('all_accepted','selected','hidden')),
    PRIMARY KEY(profile_revision_id, server_id),
    FOREIGN KEY(server_id, mcp_revision_id) REFERENCES mcp_revisions(server_id, id)
);
ALTER TABLE profile_revision_mcp_governance
    ADD CONSTRAINT profile_governance_contract_owner_fk
        FOREIGN KEY(server_id, accepted_contract_revision_id) REFERENCES mcp_contract_revisions(server_id, id);

CREATE TABLE profile_revision_tool_rules (
    profile_revision_id uuid NOT NULL REFERENCES profile_revisions(id) ON DELETE CASCADE,
    tool_id uuid NOT NULL REFERENCES mcp_tools(id),
    visible boolean NOT NULL DEFAULT true,
    decision text NOT NULL CHECK (decision IN ('deny','confirm','allow')),
    reason_codes text[] NOT NULL DEFAULT '{}'::text[] CHECK (cardinality(reason_codes) <= 16),
    PRIMARY KEY(profile_revision_id, tool_id)
);

CREATE TABLE relay_observation_cursors (
    server_id uuid PRIMARY KEY REFERENCES mcp_servers(id) ON DELETE CASCADE,
    boot_id text NOT NULL DEFAULT '' CHECK (length(boot_id) <= 128),
    cursor bigint NOT NULL DEFAULT 0 CHECK (cursor >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mcp_daily_aggregates (
    id uuid PRIMARY KEY DEFAULT md5(random()::text || clock_timestamp()::text)::uuid,
    day date NOT NULL,
    profile_id uuid REFERENCES profiles(id),
    profile_revision_id uuid REFERENCES profile_revisions(id),
    server_id uuid REFERENCES mcp_servers(id),
    tool_id uuid REFERENCES mcp_tools(id),
    client_kind text NOT NULL CHECK (client_kind IN ('claude','codex','shared','unknown')),
    decision text NOT NULL CHECK (decision IN ('deny','confirm','allow')),
    outcome text NOT NULL CHECK (outcome IN ('denied','confirmation_required','not_executed','executed','failed','unknown')),
    error_class text NOT NULL DEFAULT '' CHECK (length(error_class) <= 64),
    call_count bigint NOT NULL DEFAULT 0 CHECK (call_count >= 0),
    error_count bigint NOT NULL DEFAULT 0 CHECK (error_count >= 0),
    duration_bucket text NOT NULL DEFAULT '' CHECK (length(duration_bucket) <= 32)
);
CREATE UNIQUE INDEX mcp_daily_aggregates_dimensions_idx ON mcp_daily_aggregates(
    day,
    coalesce(profile_id, '00000000-0000-0000-0000-000000000000'::uuid),
    coalesce(profile_revision_id, '00000000-0000-0000-0000-000000000000'::uuid),
    coalesce(server_id, '00000000-0000-0000-0000-000000000000'::uuid),
    coalesce(tool_id, '00000000-0000-0000-0000-000000000000'::uuid),
    client_kind,
    decision,
    outcome,
    error_class,
    duration_bucket
);

CREATE FUNCTION reject_governance_revision_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'governance revisions are immutable';
END $$;
CREATE TRIGGER relay_configuration_revisions_immutable BEFORE UPDATE OR DELETE ON relay_configuration_revisions FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
CREATE TRIGGER relay_configuration_members_immutable BEFORE UPDATE OR DELETE ON relay_configuration_revision_mcp_servers FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
CREATE TRIGGER mcp_contract_revisions_immutable BEFORE UPDATE OR DELETE ON mcp_contract_revisions FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
CREATE TRIGGER mcp_contract_tools_immutable BEFORE UPDATE OR DELETE ON mcp_contract_revision_tools FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
CREATE TRIGGER mcp_tools_immutable BEFORE UPDATE OR DELETE ON mcp_tools FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
CREATE TRIGGER global_policy_revisions_immutable BEFORE UPDATE OR DELETE ON global_policy_revisions FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
CREATE TRIGGER profile_governance_immutable BEFORE UPDATE OR DELETE ON profile_revision_mcp_governance FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
CREATE TRIGGER profile_tool_rules_immutable BEFORE UPDATE OR DELETE ON profile_revision_tool_rules FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
CREATE TRIGGER mcp_tool_renames_immutable BEFORE UPDATE OR DELETE ON mcp_tool_renames FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();

INSERT INTO relay_configuration_revisions(id, revision, canonical_hash, metadata)
VALUES (md5('relay-configuration:1')::uuid,
        1,
        md5('relay-configuration:1') || md5('relay-configuration:1:hash'),
        '{"source":"shared-mcp-compatibility-backfill"}'::jsonb)
ON CONFLICT (revision) DO NOTHING;

INSERT INTO relay_configuration_revision_mcp_servers(relay_configuration_revision_id, server_id, mcp_revision_id, position)
SELECT r.id, pm.server_id, pm.mcp_revision_id, pm.position
FROM relay_configuration_revisions r
JOIN profiles p ON p.name = 'shared-mcp'
JOIN profile_revisions pr ON pr.id = p.current_revision_id
JOIN profile_revision_mcp_servers pm ON pm.profile_revision_id = pr.id
WHERE r.revision = 1
ON CONFLICT DO NOTHING;

INSERT INTO mcp_contract_state(server_id)
SELECT id FROM mcp_servers
ON CONFLICT DO NOTHING;

INSERT INTO global_policy_revisions(id, revision, canonical_hash, catalog_version, explicit_overrides, unclassified_mutating, reviewed_read_only)
VALUES (md5('global-policy:1')::uuid,
        1,
        md5('global-policy:1') || md5('global-policy:1:hash'),
        1,
        '{}'::jsonb,
        'confirm',
        'allow')
ON CONFLICT (revision) DO NOTHING;

INSERT INTO published_profiles(profile_id, profile_revision_id)
SELECT p.id, p.current_revision_id
FROM profiles p
WHERE false;

INSERT INTO relay_configuration_state(singleton, current_revision_id, applied_revision_id, mode)
VALUES (true, md5('relay-configuration:1')::uuid, md5('relay-configuration:1')::uuid, 'compatibility')
ON CONFLICT (singleton) DO NOTHING;
INSERT INTO global_policy_state(singleton, current_revision_id, applied_revision_id)
VALUES (true, md5('global-policy:1')::uuid, md5('global-policy:1')::uuid)
ON CONFLICT (singleton) DO NOTHING;

INSERT INTO profile_revision_mcp_governance(profile_revision_id, server_id, mcp_revision_id, accepted_contract_revision_id, visibility_mode)
SELECT pr.id, relay.server_id, relay.mcp_revision_id, NULL, 'all_accepted'
FROM profiles p
JOIN profile_revisions pr ON pr.id = p.current_revision_id
JOIN relay_configuration_revision_mcp_servers relay ON relay.relay_configuration_revision_id = md5('relay-configuration:1')::uuid
WHERE p.name <> 'shared-mcp'
ON CONFLICT DO NOTHING;
