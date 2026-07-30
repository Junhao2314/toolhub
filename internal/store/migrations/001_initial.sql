CREATE TABLE app_meta (
    key text PRIMARY KEY,
    value text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO app_meta(key,value) VALUES ('schema_generation','2');

CREATE TABLE account (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    username text NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9._-]{3,32}$' AND position('@' in username)=0),
    password_hash text NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    password_change_recommended boolean NOT NULL DEFAULT true,
    password_changed_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id_hash bytea PRIMARY KEY CHECK (octet_length(id_hash)=32),
    csrf_hash bytea NOT NULL CHECK (octet_length(csrf_hash)=32),
    expires_at timestamptz NOT NULL,
    ip_address inet,
    user_agent text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expiry_idx ON sessions(expires_at);

CREATE TABLE encrypted_secrets (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 200),
    kind text NOT NULL CHECK (kind IN ('mcp-env','mcp-header','ai-api-key')),
    ciphertext bytea NOT NULL CHECK (octet_length(ciphertext)>40),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE nodes (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    kind text NOT NULL CHECK (kind IN ('local','salt')),
    salt_minion_id text,
    managed_username_override text CHECK (managed_username_override IS NULL OR managed_username_override ~ '^[a-z_][a-z0-9_-]{0,31}$'),
    status text NOT NULL DEFAULT 'unavailable' CHECK (status IN ('online','unavailable','archived')),
    salt_version text NOT NULL DEFAULT '',
    last_seen_at timestamptz,
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind='local' AND salt_minion_id IS NULL) OR (kind='salt' AND length(salt_minion_id)>0))
);
CREATE UNIQUE INDEX nodes_one_local_idx ON nodes(kind) WHERE kind='local';
CREATE UNIQUE INDEX nodes_salt_minion_idx ON nodes(salt_minion_id) WHERE salt_minion_id IS NOT NULL;

CREATE TABLE targets (
    id uuid PRIMARY KEY,
    target_key text NOT NULL UNIQUE,
    node_id uuid NOT NULL REFERENCES nodes(id),
    runtime text NOT NULL CHECK (runtime IN ('claude','codex','hermes','shared-relay')),
    managed_username text NOT NULL CHECK (managed_username ~ '^[a-z_][a-z0-9_-]{0,31}$'),
    writable boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(node_id,runtime),
    CHECK ((runtime='hermes' AND NOT writable) OR (runtime<>'hermes' AND writable))
);

CREATE TABLE runtime_snapshots (
    target_id uuid PRIMARY KEY REFERENCES targets(id),
    revision text NOT NULL CHECK (length(revision)=64),
    inventory jsonb NOT NULL CHECK (jsonb_typeof(inventory)='object'),
    scanned_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE skill_sources (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('zip','git','skillsmp','xiaping','local')),
    name text NOT NULL,
    url text NOT NULL DEFAULT '',
    subdirectory text NOT NULL DEFAULT '',
    current_commit text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE skills (
    id uuid PRIMARY KEY,
    source_id uuid NOT NULL REFERENCES skill_sources(id),
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9._-]{0,127}$' AND left(slug,1)<>'.'),
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    current_version_id uuid,
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE skill_artifacts (
    id uuid PRIMARY KEY,
    canonical_sha256 text NOT NULL UNIQUE CHECK (canonical_sha256 ~ '^[a-f0-9]{64}$'),
	content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
    archive bytea NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes>0 AND size_bytes<=20971520),
    manifest jsonb NOT NULL CHECK (jsonb_typeof(manifest)='object'),
    scan_report jsonb NOT NULL CHECK (jsonb_typeof(scan_report)='object'),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE skill_versions (
    id uuid PRIMARY KEY,
    skill_id uuid NOT NULL REFERENCES skills(id),
    artifact_id uuid NOT NULL REFERENCES skill_artifacts(id),
    source_commit text NOT NULL DEFAULT '',
    provenance jsonb NOT NULL CHECK (jsonb_typeof(provenance)='object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(skill_id,artifact_id,source_commit)
);
ALTER TABLE skills ADD CONSTRAINT skills_current_version_fk FOREIGN KEY(current_version_id) REFERENCES skill_versions(id);

CREATE FUNCTION reject_skill_artifact_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'Skill artifacts and versions are immutable';
END $$;
CREATE TRIGGER skill_artifacts_immutable BEFORE UPDATE OR DELETE ON skill_artifacts
FOR EACH ROW EXECUTE FUNCTION reject_skill_artifact_mutation();
CREATE TRIGGER skill_versions_immutable BEFORE UPDATE OR DELETE ON skill_versions
FOR EACH ROW EXECUTE FUNCTION reject_skill_artifact_mutation();

CREATE TABLE mcp_servers (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    description text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
    transport text NOT NULL CHECK (transport IN ('stdio','http','sse')),
    command text NOT NULL DEFAULT '',
    args jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(args)='array'),
    url text NOT NULL DEFAULT '',
    env_refs jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(env_refs)='object'),
    header_refs jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(header_refs)='object'),
    content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((transport='stdio' AND length(command)>0 AND url='') OR (transport IN ('http','sse') AND command='' AND args='[]'::jsonb AND length(url)>0))
);

CREATE TABLE profiles (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 120),
    description text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE profile_skills (
    profile_id uuid NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    skill_id uuid NOT NULL REFERENCES skills(id),
    PRIMARY KEY(profile_id,skill_id)
);
CREATE TABLE profile_mcp_servers (
    profile_id uuid NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES mcp_servers(id),
    PRIMARY KEY(profile_id,server_id)
);

CREATE TABLE operations (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('skill_import','mcp_import','update_check','apply','edit','restore','scan','refresh','reconcile','relay_start','relay_stop','relay_restart','relay_health','backup_gc')),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','partial','failed','cancelled')),
    source_id uuid,
    idempotency_key text,
    request_hash text CHECK (request_hash IS NULL OR request_hash ~ '^[a-f0-9]{64}$'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object'),
    error_code text NOT NULL DEFAULT '',
    error_reason text NOT NULL DEFAULT '',
    cancel_requested boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(kind,idempotency_key)
);
CREATE INDEX operations_created_idx ON operations(created_at DESC);

CREATE TABLE operation_targets (
    id uuid PRIMARY KEY,
    operation_id uuid NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    target_id uuid NOT NULL REFERENCES targets(id),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','failed','cancelled')),
    attempt integer NOT NULL DEFAULT 1 CHECK (attempt>0),
    pending_rerun boolean NOT NULL DEFAULT false,
    bridge_operation_id text NOT NULL DEFAULT '',
    salt_jid text NOT NULL DEFAULT '',
    request jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(request)='object'),
    result jsonb,
    error_code text NOT NULL DEFAULT '',
    error_reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(operation_id,target_id)
);
CREATE UNIQUE INDEX operation_targets_one_active_idx ON operation_targets(target_id) WHERE status IN ('queued','running');
CREATE INDEX operation_targets_operation_idx ON operation_targets(operation_id);

CREATE FUNCTION validate_desired_manifest(body jsonb) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    item jsonb;
    refs jsonb;
    ref_json jsonb;
    ref_key text;
    member_id text;
    member_ids text[] := ARRAY[]::text[];
    managed_ids text[] := ARRAY[]::text[];
BEGIN
    IF body IS NULL
       OR jsonb_typeof(body)<>'object'
       OR body - ARRAY['schemaVersion','target','profileId','profileRevision','skills','mcpServers','managedMemberIds','relayPort'] <> '{}'::jsonb
       OR NOT (body ?& ARRAY['schemaVersion','target','skills','mcpServers','managedMemberIds'])
       OR jsonb_typeof(body->'schemaVersion')<>'number'
       OR (body->>'schemaVersion')::integer<>1
       OR jsonb_typeof(body->'target')<>'object'
       OR jsonb_typeof(body->'skills')<>'array'
       OR jsonb_typeof(body->'mcpServers')<>'array'
       OR jsonb_typeof(body->'managedMemberIds')<>'array'
       OR jsonb_array_length(body->'skills')>500
       OR jsonb_array_length(body->'mcpServers')>500
       OR ((body ? 'profileId')<>(body ? 'profileRevision')) THEN
        RETURN false;
    END IF;
    IF (body->'target') - ARRAY['id','nodeId','nodeKind','saltMinionId','runtime','managedUsername'] <> '{}'::jsonb
       OR NOT ((body->'target') ?& ARRAY['id','nodeId','nodeKind','runtime','managedUsername'])
       OR jsonb_typeof(body->'target'->'id')<>'string'
       OR jsonb_typeof(body->'target'->'nodeId')<>'string'
       OR jsonb_typeof(body->'target'->'nodeKind')<>'string'
       OR jsonb_typeof(body->'target'->'runtime')<>'string'
       OR jsonb_typeof(body->'target'->'managedUsername')<>'string'
       OR body->'target'->>'id' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
       OR body->'target'->>'nodeId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
       OR body->'target'->>'nodeKind' NOT IN ('local','salt')
       OR body->'target'->>'runtime' NOT IN ('claude','codex','hermes','shared-relay')
       OR body->'target'->>'runtime'='hermes'
       OR body->'target'->>'managedUsername' !~ '^[a-z_][a-z0-9_-]{0,31}$'
       OR (body->'target'->>'nodeKind'='salt' AND (
           NOT ((body->'target') ? 'saltMinionId')
           OR jsonb_typeof(body->'target'->'saltMinionId')<>'string'
           OR length(btrim(body->'target'->>'saltMinionId'))=0
       ))
       OR (body->'target'->>'nodeKind'='local' AND (body->'target') ? 'saltMinionId' AND body->'target'->>'saltMinionId'<>'')
       OR (body->'target'->>'runtime'='shared-relay' AND body->'target'->>'nodeKind'<>'local') THEN
        RETURN false;
    END IF;
    IF body ? 'profileId' AND (
           jsonb_typeof(body->'profileId')<>'string'
           OR body->>'profileId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
       ) THEN
        RETURN false;
    END IF;
    IF body ? 'profileRevision' AND (
           jsonb_typeof(body->'profileRevision')<>'number'
           OR body->>'profileRevision' !~ '^[1-9][0-9]*$'
       ) THEN
        RETURN false;
    END IF;
    IF body->'target'->>'runtime'='shared-relay' THEN
        IF NOT (body ? 'relayPort')
           OR jsonb_typeof(body->'relayPort')<>'number'
           OR body->>'relayPort' !~ '^[1-9][0-9]*$'
           OR (body->>'relayPort')::integer NOT BETWEEN 1 AND 65535 THEN
            RETURN false;
        END IF;
    ELSIF body ? 'relayPort' THEN
        RETURN false;
    END IF;
    IF (body->'target'->>'runtime'='shared-relay' AND jsonb_array_length(body->'skills')<>0)
       OR (body->'target'->>'nodeKind'='local' AND body->'target'->>'runtime'<>'shared-relay' AND jsonb_array_length(body->'mcpServers')<>0) THEN
        RETURN false;
    END IF;
    FOR item IN SELECT value FROM jsonb_array_elements(body->'skills') LOOP
        IF jsonb_typeof(item)<>'object'
           OR item - ARRAY['memberId','skillId','versionId','slug','sha256','contentHash'] <> '{}'::jsonb
           OR NOT (item ?& ARRAY['memberId','skillId','versionId','slug','sha256','contentHash'])
           OR jsonb_typeof(item->'memberId')<>'string'
           OR jsonb_typeof(item->'skillId')<>'string'
           OR jsonb_typeof(item->'versionId')<>'string'
           OR jsonb_typeof(item->'slug')<>'string'
           OR jsonb_typeof(item->'sha256')<>'string'
           OR jsonb_typeof(item->'contentHash')<>'string'
           OR item->>'memberId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
           OR item->>'skillId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
           OR item->>'versionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
           OR item->>'slug' !~ '^[a-z0-9][a-z0-9._-]{0,127}$'
           OR left(item->>'slug',1)='.'
           OR item->>'sha256' !~ '^[a-f0-9]{64}$'
           OR item->>'contentHash' !~ '^[a-f0-9]{64}$' THEN
            RETURN false;
        END IF;
        member_id := item->>'memberId';
        IF member_id=ANY(member_ids) THEN
            RETURN false;
        END IF;
        member_ids := array_append(member_ids,member_id);
    END LOOP;
    FOR item IN SELECT value FROM jsonb_array_elements(body->'mcpServers') LOOP
        IF jsonb_typeof(item)<>'object'
           OR item - ARRAY['memberId','serverId','revision','name','transport','command','args','url','envRefs','headerRefs','contentHash'] <> '{}'::jsonb
           OR NOT (item ?& ARRAY['memberId','serverId','revision','name','transport','contentHash'])
           OR jsonb_typeof(item->'memberId')<>'string'
           OR jsonb_typeof(item->'serverId')<>'string'
           OR jsonb_typeof(item->'revision')<>'number'
           OR jsonb_typeof(item->'name')<>'string'
           OR jsonb_typeof(item->'transport')<>'string'
           OR jsonb_typeof(item->'contentHash')<>'string'
           OR item->>'memberId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
           OR item->>'serverId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
           OR item->>'revision' !~ '^[1-9][0-9]*$'
           OR item->>'name' !~ '^[a-z0-9][a-z0-9._-]{0,127}$'
           OR item->>'transport' NOT IN ('stdio','http','sse')
           OR item->>'contentHash' !~ '^[a-f0-9]{64}$' THEN
            RETURN false;
        END IF;
        IF item ? 'args' AND (
               jsonb_typeof(item->'args')<>'array'
               OR EXISTS(SELECT 1 FROM jsonb_array_elements(item->'args') arg WHERE jsonb_typeof(arg.value)<>'string')
           ) THEN
            RETURN false;
        END IF;
        IF item->>'transport'='stdio' THEN
            IF NOT (item ? 'command')
               OR jsonb_typeof(item->'command')<>'string'
               OR length(btrim(item->>'command'))=0
               OR (item ? 'url' AND item->>'url'<>'') THEN
                RETURN false;
            END IF;
        ELSIF NOT (item ? 'url')
           OR jsonb_typeof(item->'url')<>'string'
           OR length(btrim(item->>'url'))=0
           OR (item ? 'command' AND item->>'command'<>'')
           OR (item ? 'args' AND jsonb_array_length(item->'args')<>0) THEN
            RETURN false;
        END IF;
        FOR refs IN SELECT value FROM jsonb_array_elements(jsonb_build_array(coalesce(item->'envRefs','{}'::jsonb),coalesce(item->'headerRefs','{}'::jsonb))) LOOP
            IF jsonb_typeof(refs)<>'object' THEN
                RETURN false;
            END IF;
            FOR ref_key,ref_json IN SELECT key,value FROM jsonb_each(refs) LOOP
                IF length(ref_key)=0
                   OR jsonb_typeof(ref_json)<>'string'
                   OR ref_json #>> '{}' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$' THEN
                    RETURN false;
                END IF;
            END LOOP;
        END LOOP;
        member_id := item->>'memberId';
        IF member_id=ANY(member_ids) THEN
            RETURN false;
        END IF;
        member_ids := array_append(member_ids,member_id);
    END LOOP;
    FOR item IN SELECT value FROM jsonb_array_elements(body->'managedMemberIds') LOOP
        IF jsonb_typeof(item)<>'string' THEN
            RETURN false;
        END IF;
        member_id := item #>> '{}';
        IF member_id !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
           OR NOT (member_id=ANY(member_ids))
           OR member_id=ANY(managed_ids) THEN
            RETURN false;
        END IF;
        managed_ids := array_append(managed_ids,member_id);
    END LOOP;
    IF cardinality(member_ids)<>cardinality(managed_ids) THEN
        RETURN false;
    END IF;
    RETURN true;
EXCEPTION WHEN others THEN
    RETURN false;
END $$;

CREATE TABLE desired_snapshots (
    id uuid PRIMARY KEY,
    target_id uuid NOT NULL REFERENCES targets(id),
    revision bigint NOT NULL CHECK (revision>0),
    source_kind text NOT NULL CHECK (source_kind IN ('profile_apply','target_edit','restore')),
    source_id uuid,
    source_operation_target_id uuid REFERENCES operation_targets(id),
    profile_revision bigint,
    manifest_schema_version integer NOT NULL CHECK (manifest_schema_version=1),
    manifest_hash text NOT NULL CHECK (manifest_hash ~ '^[a-f0-9]{64}$'),
    manifest jsonb NOT NULL CHECK (validate_desired_manifest(manifest)),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(target_id,revision),
    UNIQUE(target_id,manifest_hash,revision),
    UNIQUE(source_operation_target_id)
);

CREATE FUNCTION reject_desired_snapshot_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'desired snapshots are immutable';
END $$;
CREATE TRIGGER desired_snapshots_immutable BEFORE UPDATE OR DELETE ON desired_snapshots
FOR EACH ROW EXECUTE FUNCTION reject_desired_snapshot_mutation();

CREATE TABLE target_desired_snapshots (
    target_id uuid PRIMARY KEY REFERENCES targets(id),
    snapshot_id uuid NOT NULL REFERENCES desired_snapshots(id),
    desired_revision bigint NOT NULL CHECK (desired_revision>0),
    health text NOT NULL DEFAULT 'drifted' CHECK (health IN ('healthy','drifted','repairing','blocked','unavailable')),
    drift_summary jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(drift_summary)='object'),
    error_code text NOT NULL DEFAULT '',
    error_reason text NOT NULL DEFAULT '',
    last_reconciled_at timestamptz,
    last_repair_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(snapshot_id)
);

CREATE TABLE preflight_confirmations (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash)=32),
    profile_id uuid NOT NULL REFERENCES profiles(id),
    profile_revision bigint NOT NULL,
    target_id uuid NOT NULL REFERENCES targets(id),
    target_revision text NOT NULL,
    manifest_hash text NOT NULL CHECK (manifest_hash ~ '^[a-f0-9]{64}$'),
    manifest jsonb NOT NULL CHECK (jsonb_typeof(manifest)='object'),
    diff jsonb NOT NULL CHECK (jsonb_typeof(diff)='object'),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX preflight_expiry_idx ON preflight_confirmations(expires_at);

CREATE TABLE local_mcp_import_confirmations (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash)=32),
    target_id uuid NOT NULL REFERENCES targets(id),
    target_revision text NOT NULL CHECK (target_revision ~ '^[a-f0-9]{64}$'),
    server_name text NOT NULL,
    content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
    preview jsonb NOT NULL CHECK (jsonb_typeof(preview)='object'),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX local_mcp_import_confirmation_expiry_idx ON local_mcp_import_confirmations(expires_at);

CREATE TABLE backups (
    id uuid PRIMARY KEY,
    bridge_backup_id text NOT NULL UNIQUE,
    target_id uuid NOT NULL REFERENCES targets(id),
    source_operation_id uuid REFERENCES operations(id),
    target_revision text NOT NULL,
    manifest_hash text CHECK (manifest_hash IS NULL OR manifest_hash ~ '^[a-f0-9]{64}$'),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object')
);
CREATE INDEX backups_retention_idx ON backups(target_id,created_at DESC);

CREATE TABLE settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    managed_username text NOT NULL CHECK (managed_username ~ '^[a-z_][a-z0-9_-]{0,31}$'),
    update_cron text NOT NULL DEFAULT '0 2 * * *',
    timezone text NOT NULL DEFAULT 'Asia/Shanghai',
    relay_port integer NOT NULL DEFAULT 6276 CHECK (relay_port BETWEEN 1 AND 65535),
    relay_intentional_paused boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE market_providers (
    id uuid PRIMARY KEY,
    kind text NOT NULL UNIQUE CHECK (kind IN ('skillsmp','xiaping')),
    enabled boolean NOT NULL DEFAULT true,
    base_url text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO market_providers(id,kind,enabled,base_url) VALUES
    (gen_random_uuid(),'skillsmp',true,'https://skillsmp.com/api/v1'),
    (gen_random_uuid(),'xiaping',true,'https://xiaping.coze.com');

CREATE TABLE ai_providers (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    base_url text NOT NULL,
    model text NOT NULL,
    api_key_secret_id uuid NOT NULL REFERENCES encrypted_secrets(id),
    enabled boolean NOT NULL DEFAULT true,
    is_default boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ai_one_default_idx ON ai_providers(is_default) WHERE is_default AND enabled;

CREATE TABLE alerts (
    id uuid PRIMARY KEY,
    target_id uuid REFERENCES targets(id),
    severity text NOT NULL CHECK (severity IN ('warning','error')),
    code text NOT NULL,
    message text NOT NULL,
    acknowledged_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alerts_open_idx ON alerts(created_at DESC) WHERE acknowledged_at IS NULL;

CREATE TABLE audit_events (
    id uuid PRIMARY KEY,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL DEFAULT '',
    outcome text NOT NULL CHECK (outcome IN ('success','failure','denied')),
    ip_address inet,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_created_idx ON audit_events(created_at DESC);
