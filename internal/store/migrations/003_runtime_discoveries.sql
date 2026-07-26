ALTER TABLE skill_sources DROP CONSTRAINT skill_sources_kind_check;
ALTER TABLE skill_sources ADD CONSTRAINT skill_sources_kind_check
    CHECK (kind IN ('upload','git','skillsmp','openai','node'));

ALTER TABLE mcp_servers
    ADD COLUMN runtime_name text NOT NULL DEFAULT '',
    ADD COLUMN origin jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN config_fingerprint text NOT NULL DEFAULT '';
UPDATE mcp_servers SET runtime_name=name WHERE runtime_name='';

ALTER TABLE mcp_profiles
    ADD COLUMN source text NOT NULL DEFAULT 'toolhub',
    ADD COLUMN origin jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE UNIQUE INDEX mcp_profiles_runtime_auto_origin_idx
    ON mcp_profiles ((origin->>'nodeId'), (origin->>'runtime'))
    WHERE source='runtime-auto';

CREATE TABLE skill_discoveries (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    runtime_kind text NOT NULL CHECK (runtime_kind IN ('codex','claude','hermes')),
    canonical_path text NOT NULL,
    name text NOT NULL,
    directory_hash text NOT NULL DEFAULT '',
    managed boolean NOT NULL DEFAULT false,
    protected boolean NOT NULL DEFAULT false,
    disabled boolean NOT NULL DEFAULT false,
    missing boolean NOT NULL DEFAULT false,
    drift boolean NOT NULL DEFAULT false,
    adoption_status text NOT NULL DEFAULT 'discovered'
        CHECK (adoption_status IN ('discovered','adopting','imported','adopted','failed')),
    adoption_error text NOT NULL DEFAULT '',
    adopted_skill_id uuid REFERENCES skills(id) ON DELETE SET NULL,
    adopted_version_id uuid REFERENCES skill_versions(id) ON DELETE SET NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(node_id, runtime_kind, canonical_path)
);
CREATE INDEX skill_discoveries_node_idx ON skill_discoveries(node_id, runtime_kind, missing, drift);

CREATE TABLE mcp_runtime_bindings (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    runtime_kind text NOT NULL CHECK (runtime_kind IN ('codex','claude','hermes')),
    server_name text NOT NULL,
    identity text NOT NULL,
    server_id uuid REFERENCES mcp_servers(id) ON DELETE SET NULL,
    profile_id uuid REFERENCES mcp_profiles(id) ON DELETE SET NULL,
    deployment_id uuid REFERENCES mcp_deployments(id) ON DELETE SET NULL,
    env_keys jsonb NOT NULL DEFAULT '[]'::jsonb,
    observed_config_fingerprint text NOT NULL DEFAULT '',
    observed_secret_fingerprint text NOT NULL DEFAULT '',
    desired_config_fingerprint text NOT NULL DEFAULT '',
    desired_secret_fingerprint text NOT NULL DEFAULT '',
    desired_enabled boolean NOT NULL DEFAULT true,
    missing boolean NOT NULL DEFAULT false,
    drift boolean NOT NULL DEFAULT false,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(node_id, runtime_kind, server_name)
);
CREATE INDEX mcp_runtime_bindings_node_idx ON mcp_runtime_bindings(node_id, runtime_kind, missing, drift);
CREATE INDEX mcp_runtime_bindings_server_idx ON mcp_runtime_bindings(server_id);

CREATE TABLE mcp_capture_tokens (
    id uuid PRIMARY KEY,
    token_hash bytea NOT NULL UNIQUE,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    runtime_kind text NOT NULL CHECK (runtime_kind IN ('codex','claude','hermes')),
    server_name text NOT NULL,
    identity text NOT NULL,
    descriptor jsonb NOT NULL,
    config_fingerprint text NOT NULL,
    secret_fingerprint text NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mcp_capture_tokens_expiry_idx ON mcp_capture_tokens(expires_at) WHERE used_at IS NULL;
