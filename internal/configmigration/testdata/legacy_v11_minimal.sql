CREATE TABLE schema_migrations (
    version bigint PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO schema_migrations(version) SELECT generate_series(1,11);

CREATE TABLE skill_sources (
    id uuid PRIMARY KEY,
    kind text NOT NULL,
    name text NOT NULL,
    url text,
    subdirectory text NOT NULL DEFAULT '',
    credentials_secret_id uuid
);
CREATE TABLE skills (
    id uuid PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    source_id uuid NOT NULL,
    review_status text NOT NULL,
    protected boolean NOT NULL DEFAULT false,
    current_version_id uuid,
    archived_at timestamptz
);
CREATE TABLE skill_artifacts (
    id uuid PRIMARY KEY,
    sha256 text NOT NULL UNIQUE,
    size_bytes bigint NOT NULL,
    content bytea NOT NULL,
    scan_report jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE TABLE skill_versions (
    id uuid PRIMARY KEY,
    skill_id uuid NOT NULL,
    source_commit text NOT NULL DEFAULT '',
    content_sha256 text NOT NULL,
    artifact_id uuid NOT NULL,
    provenance jsonb NOT NULL DEFAULT '{}'::jsonb,
    manifest jsonb NOT NULL DEFAULT '{}'::jsonb,
    approved_at timestamptz
);
CREATE TABLE encrypted_secrets (
    id uuid PRIMARY KEY,
    kind text NOT NULL,
    ciphertext bytea NOT NULL
);
CREATE TABLE deployments (
    skill_id uuid NOT NULL,
    runtime_kind text NOT NULL,
    desired_enabled boolean NOT NULL,
    state text NOT NULL
);
CREATE TABLE mcp_servers (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    runtime_name text NOT NULL DEFAULT '',
    transport text NOT NULL,
    command text NOT NULL DEFAULT '',
    args jsonb NOT NULL DEFAULT '[]'::jsonb,
    url text NOT NULL DEFAULT '',
    env_refs jsonb NOT NULL DEFAULT '{}'::jsonb,
    header_refs jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled boolean NOT NULL DEFAULT true,
    source text NOT NULL DEFAULT 'toolhub',
    origin jsonb NOT NULL DEFAULT '{}'::jsonb,
    authority text NOT NULL DEFAULT 'toolhub',
    credential_mode text NOT NULL DEFAULT 'toolhub-secret',
    archived_at timestamptz
);
CREATE TABLE mcp_profiles (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    enabled boolean NOT NULL DEFAULT true,
    source text NOT NULL DEFAULT 'toolhub',
    origin jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE TABLE mcp_profile_servers (
    profile_id uuid NOT NULL,
    server_id uuid NOT NULL,
    PRIMARY KEY(profile_id,server_id)
);
CREATE TABLE toolhub_profiles (id uuid PRIMARY KEY);
CREATE TABLE toolhub_profile_skills (profile_id uuid NOT NULL, skill_id uuid NOT NULL);
CREATE TABLE toolhub_profile_mcp_servers (profile_id uuid NOT NULL, server_id uuid NOT NULL);
CREATE TABLE toolhub_profile_activations (profile_id uuid NOT NULL, runtime_kind text NOT NULL, state text NOT NULL);
CREATE TABLE update_policies (
    scope_type text NOT NULL,
    scope_id text NOT NULL DEFAULT '',
    schedule text NOT NULL,
    timezone text NOT NULL,
    enabled boolean NOT NULL,
    UNIQUE(scope_type,scope_id)
);
INSERT INTO update_policies(scope_type,scope_id,schedule,timezone,enabled)
VALUES('global','','0 2 * * *','Asia/Shanghai',true);
CREATE TABLE ai_providers (id uuid PRIMARY KEY);
