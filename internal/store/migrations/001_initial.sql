CREATE TABLE IF NOT EXISTS schema_migrations (
    version bigint PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE roles (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (name IN ('admin', 'operator', 'viewer')),
    description text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO roles (id, name, description) VALUES
    ('00000000-0000-0000-0000-000000000001', 'admin', 'Full administrative access'),
    ('00000000-0000-0000-0000-000000000002', 'operator', 'Manage nodes, skills, jobs, and MCP'),
    ('00000000-0000-0000-0000-000000000003', 'viewer', 'Read-only access')
ON CONFLICT (name) DO NOTHING;

CREATE TABLE users (
    id uuid PRIMARY KEY,
    email text NOT NULL UNIQUE,
    display_name text NOT NULL,
    password_hash text NOT NULL,
    disabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_roles (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id uuid NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE sessions (
    id_hash bytea PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    ip_address inet,
    user_agent text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_idx ON sessions(user_id);
CREATE INDEX sessions_expiry_idx ON sessions(expires_at);

CREATE TABLE encrypted_secrets (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    kind text NOT NULL,
    ciphertext bytea NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE nodes (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    hostname text NOT NULL DEFAULT '',
    platform text NOT NULL DEFAULT 'unknown',
    architecture text NOT NULL DEFAULT 'unknown',
    tailscale_ip inet,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','online','offline','drift','conflict','archived')),
    labels jsonb NOT NULL DEFAULT '{}'::jsonb,
    connection_preference text NOT NULL DEFAULT 'agent' CHECK (connection_preference IN ('agent','ssh')),
    agent_public_key bytea,
    agent_token_hash bytea,
    task_key_secret_id uuid REFERENCES encrypted_secrets(id),
    last_seen_at timestamptz,
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE node_connections (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('agent','ssh')),
    address text NOT NULL,
    host_key_fingerprint text,
    secret_id uuid REFERENCES encrypted_secrets(id),
    priority integer NOT NULL DEFAULT 100,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE enrollment_tokens (
    id uuid PRIMARY KEY,
    token_hash bytea NOT NULL UNIQUE,
    node_name text NOT NULL,
    labels jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE runtimes (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('codex','claude','hermes')),
    root_path text NOT NULL,
    version text NOT NULL DEFAULT '',
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    inventory jsonb NOT NULL DEFAULT '{}'::jsonb,
    scanned_at timestamptz,
    UNIQUE(node_id, kind, root_path)
);

CREATE TABLE skill_sources (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('upload','git','skillsmp','openai')),
    name text NOT NULL,
    url text,
    subdirectory text NOT NULL DEFAULT '',
    default_branch text NOT NULL DEFAULT '',
    credentials_secret_id uuid REFERENCES encrypted_secrets(id),
    update_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE skills (
    id uuid PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    source_id uuid REFERENCES skill_sources(id),
    review_status text NOT NULL DEFAULT 'pending' CHECK (review_status IN ('pending','approved','rejected','quarantined')),
    protected boolean NOT NULL DEFAULT false,
    current_version_id uuid,
    archived_at timestamptz,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE skill_artifacts (
    id uuid PRIMARY KEY,
    sha256 text NOT NULL UNIQUE,
    media_type text NOT NULL DEFAULT 'application/zip',
    size_bytes bigint NOT NULL,
    content bytea NOT NULL,
    scan_report jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE skill_versions (
    id uuid PRIMARY KEY,
    skill_id uuid NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    source_commit text NOT NULL DEFAULT '',
    content_sha256 text NOT NULL,
    artifact_id uuid NOT NULL REFERENCES skill_artifacts(id),
    provenance jsonb NOT NULL,
    manifest jsonb NOT NULL,
    risk_level text NOT NULL CHECK (risk_level IN ('low','medium','high','critical')),
    approved_at timestamptz,
    approved_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(skill_id, source_commit, content_sha256)
);
ALTER TABLE skills ADD CONSTRAINT skills_current_version_fk FOREIGN KEY (current_version_id) REFERENCES skill_versions(id);

CREATE TABLE deployments (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    runtime_kind text NOT NULL CHECK (runtime_kind IN ('codex','claude','hermes')),
    skill_id uuid NOT NULL REFERENCES skills(id),
    desired_version_id uuid REFERENCES skill_versions(id),
    actual_version_id uuid REFERENCES skill_versions(id),
    previous_version_id uuid REFERENCES skill_versions(id),
    desired_enabled boolean NOT NULL DEFAULT true,
    actual_enabled boolean NOT NULL DEFAULT false,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','in_sync','drift','conflict','failed','rolling_back','archived')),
    last_error text NOT NULL DEFAULT '',
    reconciled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(node_id, runtime_kind, skill_id)
);

CREATE TABLE update_policies (
    id uuid PRIMARY KEY,
    scope_type text NOT NULL CHECK (scope_type IN ('global','source','skill','node_group')),
    scope_id text NOT NULL DEFAULT '',
    schedule text NOT NULL DEFAULT '0 2 * * *',
    timezone text NOT NULL DEFAULT 'Asia/Shanghai',
    enabled boolean NOT NULL DEFAULT true,
    require_approval boolean NOT NULL DEFAULT true,
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE(scope_type, scope_id)
);

CREATE TABLE sync_policies (
    id uuid PRIMARY KEY,
    scope_type text NOT NULL CHECK (scope_type IN ('global','source','skill','node_group')),
    scope_id text NOT NULL DEFAULT '',
    schedule text NOT NULL DEFAULT '30 3 * * *',
    timezone text NOT NULL DEFAULT 'Asia/Shanghai',
    enabled boolean NOT NULL DEFAULT true,
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE(scope_type, scope_id)
);

CREATE TABLE updates (
    id uuid PRIMARY KEY,
    skill_id uuid NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    from_version_id uuid REFERENCES skill_versions(id),
    candidate_commit text NOT NULL,
    candidate_sha256 text NOT NULL,
    diff jsonb NOT NULL,
    risk_change jsonb NOT NULL DEFAULT '{}'::jsonb,
    license_change jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'available' CHECK (status IN ('available','approved','rejected','superseded')),
    approved_by uuid REFERENCES users(id),
    approved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE jobs (
    id uuid PRIMARY KEY,
    kind text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','succeeded','failed','cancelled')),
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    result jsonb NOT NULL DEFAULT '{}'::jsonb,
    dry_run boolean NOT NULL DEFAULT false,
    attempts integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 5,
    run_after timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    cancel_requested_at timestamptz,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX jobs_claim_idx ON jobs(status, run_after, created_at);

CREATE TABLE node_tasks (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    job_id uuid REFERENCES jobs(id) ON DELETE SET NULL,
    kind text NOT NULL,
    payload jsonb NOT NULL,
    signature text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','running','succeeded','failed','cancelled')),
    attempt integer NOT NULL DEFAULT 0,
    result jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX node_tasks_pending_idx ON node_tasks(node_id, status, created_at);

CREATE TABLE mcp_servers (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    transport text NOT NULL CHECK (transport IN ('stdio','sse','streamable-http')),
    command text NOT NULL DEFAULT '',
    args jsonb NOT NULL DEFAULT '[]'::jsonb,
    url text NOT NULL DEFAULT '',
    env_refs jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled boolean NOT NULL DEFAULT true,
    source text NOT NULL DEFAULT 'toolhub',
    health_status text NOT NULL DEFAULT 'unknown',
    usage jsonb NOT NULL DEFAULT '{}'::jsonb,
    update_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mcp_profiles (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mcp_profile_servers (
    profile_id uuid NOT NULL REFERENCES mcp_profiles(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    overrides jsonb NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY(profile_id, server_id)
);

CREATE TABLE mcp_deployments (
    id uuid PRIMARY KEY,
    profile_id uuid NOT NULL REFERENCES mcp_profiles(id),
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    runtime_kind text NOT NULL CHECK (runtime_kind IN ('codex','claude','hermes')),
    desired_enabled boolean NOT NULL DEFAULT true,
    actual_hash text NOT NULL DEFAULT '',
    desired_hash text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending',
    last_error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(profile_id, node_id, runtime_kind)
);

CREATE TABLE ai_providers (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    base_url text NOT NULL,
    model text NOT NULL,
    api_key_secret_id uuid NOT NULL REFERENCES encrypted_secrets(id),
    is_default boolean NOT NULL DEFAULT false,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE market_providers (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    base_url text NOT NULL,
    api_key_secret_id uuid REFERENCES encrypted_secrets(id),
    enabled boolean NOT NULL DEFAULT true,
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_events (
    id uuid PRIMARY KEY,
    actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL DEFAULT '',
    outcome text NOT NULL,
    ip_address inet,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_created_idx ON audit_events(created_at DESC);

INSERT INTO update_policies (id, scope_type, schedule, timezone, enabled, require_approval)
VALUES ('10000000-0000-0000-0000-000000000001', 'global', '0 2 * * *', 'Asia/Shanghai', true, true)
ON CONFLICT (scope_type, scope_id) DO NOTHING;

INSERT INTO sync_policies (id, scope_type, schedule, timezone, enabled)
VALUES ('10000000-0000-0000-0000-000000000002', 'global', '30 3 * * *', 'Asia/Shanghai', true)
ON CONFLICT (scope_type, scope_id) DO NOTHING;

INSERT INTO market_providers (id, name, base_url, enabled)
VALUES ('10000000-0000-0000-0000-000000000003', 'SkillsMP', 'https://skillsmp.com/api/v1', true)
ON CONFLICT (name) DO NOTHING;
