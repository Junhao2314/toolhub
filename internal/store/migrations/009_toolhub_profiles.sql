-- User-defined selection sets are distinct from mcp_profiles, which remain the
-- fixed toolhub-<runtime> mcpm delivery channels.
CREATE TABLE toolhub_profiles (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE toolhub_profile_mcp_servers (
    profile_id uuid NOT NULL REFERENCES toolhub_profiles(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    PRIMARY KEY (profile_id, server_id)
);

CREATE TABLE toolhub_profile_skills (
    profile_id uuid NOT NULL REFERENCES toolhub_profiles(id) ON DELETE CASCADE,
    skill_id uuid NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    PRIMARY KEY (profile_id, skill_id)
);

CREATE TABLE toolhub_profile_activations (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    runtime_kind text NOT NULL
        CHECK (runtime_kind IN ('codex','claude','hermes','grok','openclaw')),
    profile_id uuid NOT NULL REFERENCES toolhub_profiles(id) ON DELETE RESTRICT,
    previous_profile_id uuid REFERENCES toolhub_profiles(id) ON DELETE SET NULL,
    state text NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending','active','partial','failed')),
    last_error text NOT NULL DEFAULT '',
    skipped jsonb NOT NULL DEFAULT '[]'::jsonb,
    activated_by uuid REFERENCES users(id),
    activated_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (node_id, runtime_kind)
);

CREATE INDEX toolhub_profile_activations_profile_idx
    ON toolhub_profile_activations(profile_id);

ALTER TABLE mcp_servers ADD COLUMN archived_at timestamptz;
