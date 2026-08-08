-- Fleet operations can retain a partial terminal result for independently
-- committed batch intake. Keep historical target rows readable while allowing
-- the worker's partial projection to be persisted.
ALTER TABLE operation_targets DROP CONSTRAINT IF EXISTS operation_targets_status_check;
ALTER TABLE operation_targets ADD CONSTRAINT operation_targets_status_check
    CHECK (status IN ('queued','running','succeeded','partial','failed','cancelled'));

CREATE TABLE mcp_revisions (
    id uuid PRIMARY KEY,
    server_id uuid NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    revision bigint NOT NULL CHECK (revision > 0),
    name text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    description text NOT NULL DEFAULT '',
    transport text NOT NULL CHECK (transport IN ('stdio','http','sse')),
    command text NOT NULL DEFAULT '',
    args jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(args) = 'array'),
    url text NOT NULL DEFAULT '',
    env_slots text[] NOT NULL DEFAULT '{}'::text[] CHECK (cardinality(env_slots) <= 100),
    header_slots text[] NOT NULL DEFAULT '{}'::text[] CHECK (cardinality(header_slots) <= 100),
    env_refs jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(env_refs) = 'object'),
    header_refs jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(header_refs) = 'object'),
    content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
    provenance jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(provenance) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(server_id, revision),
    CHECK ((transport = 'stdio' AND length(command) > 0 AND url = '') OR
           (transport IN ('http','sse') AND command = '' AND args = '[]'::jsonb AND length(url) > 0))
);
CREATE UNIQUE INDEX mcp_revisions_server_id_id_idx ON mcp_revisions(server_id, id);
CREATE INDEX mcp_revisions_content_idx ON mcp_revisions(server_id, content_hash);

INSERT INTO mcp_revisions(
    id, server_id, revision, name, description, transport, command, args, url,
    env_slots, header_slots, env_refs, header_refs, content_hash, provenance, created_at
)
SELECT id, id, revision, name, description, transport, command, args, url,
       ARRAY(SELECT jsonb_object_keys(env_refs) ORDER BY 1),
       ARRAY(SELECT jsonb_object_keys(header_refs) ORDER BY 1),
       env_refs, header_refs, content_hash, '{"source":"generation-2-backfill"}'::jsonb, created_at
FROM mcp_servers;

ALTER TABLE mcp_servers ADD COLUMN current_revision_id uuid;
UPDATE mcp_servers SET current_revision_id = id;
ALTER TABLE mcp_servers
    ALTER COLUMN current_revision_id SET NOT NULL,
    ADD CONSTRAINT mcp_servers_current_revision_fk
        FOREIGN KEY (id, current_revision_id) REFERENCES mcp_revisions(server_id, id)
        DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION reject_mcp_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'MCP revisions are immutable';
END $$;
CREATE TRIGGER mcp_revisions_immutable BEFORE UPDATE ON mcp_revisions
FOR EACH ROW EXECUTE FUNCTION reject_mcp_revision_update();

CREATE TABLE profile_revisions (
    id uuid PRIMARY KEY,
    profile_id uuid NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    revision bigint NOT NULL CHECK (revision > 0),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    description text NOT NULL DEFAULT '',
    canonical_hash text NOT NULL CHECK (canonical_hash ~ '^[a-f0-9]{64}$'),
    pending_bindings boolean NOT NULL DEFAULT false,
    archived_restore boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(profile_id, revision)
);
CREATE UNIQUE INDEX profile_revisions_profile_id_id_idx ON profile_revisions(profile_id, id);
CREATE UNIQUE INDEX skill_versions_skill_id_id_idx ON skill_versions(skill_id, id);

CREATE TABLE profile_revision_skills (
    profile_revision_id uuid NOT NULL REFERENCES profile_revisions(id) ON DELETE CASCADE,
    skill_id uuid NOT NULL REFERENCES skills(id),
    skill_version_id uuid NOT NULL,
    position integer NOT NULL DEFAULT 0 CHECK (position >= 0),
    PRIMARY KEY(profile_revision_id, skill_id),
    UNIQUE(profile_revision_id, position),
    FOREIGN KEY(skill_id, skill_version_id) REFERENCES skill_versions(skill_id, id)
);

CREATE TABLE profile_revision_mcp_servers (
    profile_revision_id uuid NOT NULL REFERENCES profile_revisions(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES mcp_servers(id),
    mcp_revision_id uuid NOT NULL,
    position integer NOT NULL DEFAULT 0 CHECK (position >= 0),
    PRIMARY KEY(profile_revision_id, server_id),
    UNIQUE(profile_revision_id, position),
    FOREIGN KEY(server_id, mcp_revision_id) REFERENCES mcp_revisions(server_id, id)
);

INSERT INTO profile_revisions(id, profile_id, revision, name, description, canonical_hash, created_at)
SELECT id, id, revision, name, description,
       md5(id::text || ':' || revision::text) || md5(revision::text || ':' || id::text),
       created_at
FROM profiles;

INSERT INTO profile_revision_skills(profile_revision_id, skill_id, skill_version_id, position)
SELECT ps.profile_id, ps.skill_id, sk.current_version_id,
       row_number() OVER (PARTITION BY ps.profile_id ORDER BY ps.skill_id)::integer - 1
FROM profile_skills ps
JOIN skills sk ON sk.id = ps.skill_id;

INSERT INTO profile_revision_mcp_servers(profile_revision_id, server_id, mcp_revision_id, position)
SELECT pm.profile_id, pm.server_id, ms.current_revision_id,
       row_number() OVER (PARTITION BY pm.profile_id ORDER BY pm.server_id)::integer - 1
FROM profile_mcp_servers pm
JOIN mcp_servers ms ON ms.id = pm.server_id;

ALTER TABLE profiles
    ADD COLUMN current_revision_id uuid,
    ADD COLUMN archived_at timestamptz;
UPDATE profiles SET current_revision_id = id;
ALTER TABLE profiles
    ALTER COLUMN current_revision_id SET NOT NULL,
    ADD CONSTRAINT profiles_current_revision_fk
        FOREIGN KEY (id, current_revision_id) REFERENCES profile_revisions(profile_id, id)
        DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX profiles_archived_idx ON profiles(archived_at) WHERE archived_at IS NOT NULL;

CREATE FUNCTION reject_profile_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'Profile revisions are immutable';
END $$;
CREATE TRIGGER profile_revisions_immutable BEFORE UPDATE ON profile_revisions
FOR EACH ROW EXECUTE FUNCTION reject_profile_revision_update();

CREATE TABLE pending_secret_bindings (
    profile_revision_id uuid NOT NULL REFERENCES profile_revisions(id) ON DELETE CASCADE,
    mcp_revision_id uuid NOT NULL REFERENCES mcp_revisions(id),
    namespace text NOT NULL CHECK (namespace IN ('env','header')),
    key text NOT NULL CHECK (length(key) BETWEEN 1 AND 200),
    slot_hash text NOT NULL CHECK (slot_hash ~ '^[a-f0-9]{64}$'),
    secret_id uuid REFERENCES encrypted_secrets(id),
    bound_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(profile_revision_id, mcp_revision_id, namespace, key),
    CHECK ((secret_id IS NULL AND bound_at IS NULL) OR
           (secret_id IS NOT NULL AND bound_at IS NOT NULL))
);

CREATE TABLE bundle_import_confirmations (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    bundle_hash text NOT NULL CHECK (bundle_hash ~ '^[a-f0-9]{64}$'),
    preview jsonb NOT NULL CHECK (jsonb_typeof(preview) = 'object'),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bundle_import_confirmations_expiry_idx ON bundle_import_confirmations(expires_at);

CREATE TABLE bundle_import_fingerprints (
    bundle_hash text PRIMARY KEY CHECK (bundle_hash ~ '^[a-f0-9]{64}$'),
    profile_id uuid NOT NULL REFERENCES profiles(id),
    profile_revision_id uuid NOT NULL REFERENCES profile_revisions(id),
    canonical_hash text NOT NULL CHECK (canonical_hash ~ '^[a-f0-9]{64}$'),
    imported_at timestamptz NOT NULL DEFAULT now(),
    retain_until timestamptz NOT NULL DEFAULT now() + interval '60 days',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);
CREATE INDEX bundle_import_fingerprints_retention_idx ON bundle_import_fingerprints(retain_until);

CREATE TABLE retention_runs (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('history_gc','secret_gc','library_gc_preview','library_gc')),
    status text NOT NULL CHECK (status IN ('preview','succeeded','failed')),
    cutoff_at timestamptz NOT NULL,
    counts jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(counts) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now()
);
