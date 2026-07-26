CREATE TABLE shared_sources (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    mode text NOT NULL DEFAULT 'observed'
        CONSTRAINT shared_sources_mode_check CHECK (mode IN ('observed','managed')),
    auto_sync boolean NOT NULL DEFAULT false,
    skills_root text NOT NULL,
    mcp_manifest_path text NOT NULL,
    config_fingerprint text NOT NULL DEFAULT '',
    source_fingerprint text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'observed'
        CONSTRAINT shared_sources_status_check CHECK (status IN ('observed','in_sync','drift','conflict','blocked','failed','missing')),
    last_scan_at timestamptz,
    last_sync_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT shared_sources_auto_sync_mode_check CHECK (NOT auto_sync OR mode='managed'),
    UNIQUE(node_id, name)
);
CREATE INDEX shared_sources_node_idx ON shared_sources(node_id, status);

CREATE TABLE shared_consumers (
    id uuid PRIMARY KEY,
    source_id uuid NOT NULL REFERENCES shared_sources(id) ON DELETE CASCADE,
    consumer_kind text NOT NULL
        CONSTRAINT shared_consumers_kind_check CHECK (consumer_kind IN ('codex','claude','hermes','grok','openclaw')),
    skills_path text NOT NULL DEFAULT '',
    mcp_path text NOT NULL DEFAULT '',
    mcp_format text NOT NULL DEFAULT '',
    inherits_from text NOT NULL DEFAULT '',
    skills_enabled boolean NOT NULL DEFAULT false,
    mcp_enabled boolean NOT NULL DEFAULT false,
    expected_fingerprint text NOT NULL DEFAULT '',
    actual_fingerprint text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'observed'
        CONSTRAINT shared_consumers_state_check CHECK (state IN ('observed','in_sync','missing','drift','conflict','blocked','failed')),
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(source_id, consumer_kind)
);

CREATE TABLE shared_skill_links (
    id uuid PRIMARY KEY,
    source_id uuid NOT NULL REFERENCES shared_sources(id) ON DELETE CASCADE,
    consumer_id uuid NOT NULL REFERENCES shared_consumers(id) ON DELETE CASCADE,
    skill_name text NOT NULL,
    source_path text NOT NULL,
    resolved_source_path text NOT NULL DEFAULT '',
    target_path text NOT NULL,
    expected_target text NOT NULL DEFAULT '',
    actual_target text NOT NULL DEFAULT '',
    managed boolean NOT NULL DEFAULT false,
    state text NOT NULL DEFAULT 'observed'
        CONSTRAINT shared_skill_links_state_check CHECK (state IN ('observed','in_sync','missing','drift','conflict','blocked','failed')),
    last_error text NOT NULL DEFAULT '',
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(source_id, consumer_id, skill_name)
);
CREATE INDEX shared_skill_links_source_idx ON shared_skill_links(source_id, state);

ALTER TABLE runtimes DROP CONSTRAINT runtimes_kind_check;
ALTER TABLE runtimes ADD CONSTRAINT runtimes_kind_check
    CHECK (kind IN ('codex','claude','hermes','grok','openclaw'));

ALTER TABLE deployments DROP CONSTRAINT deployments_runtime_kind_check;
ALTER TABLE deployments ADD CONSTRAINT deployments_runtime_kind_check
    CHECK (runtime_kind IN ('codex','claude','hermes','grok','openclaw','shared'));

ALTER TABLE skill_discoveries DROP CONSTRAINT skill_discoveries_runtime_kind_check;
ALTER TABLE skill_discoveries ADD CONSTRAINT skill_discoveries_runtime_kind_check
    CHECK (runtime_kind IN ('codex','claude','hermes','grok','openclaw','shared'));

ALTER TABLE mcp_deployments DROP CONSTRAINT mcp_deployments_runtime_kind_check;
ALTER TABLE mcp_deployments ADD CONSTRAINT mcp_deployments_runtime_kind_check
    CHECK (runtime_kind IN ('codex','claude','hermes','grok','openclaw'));

ALTER TABLE mcp_runtime_bindings DROP CONSTRAINT mcp_runtime_bindings_runtime_kind_check;
ALTER TABLE mcp_runtime_bindings ADD CONSTRAINT mcp_runtime_bindings_runtime_kind_check
    CHECK (runtime_kind IN ('codex','claude','hermes','grok','openclaw'));

ALTER TABLE mcp_capture_tokens DROP CONSTRAINT mcp_capture_tokens_runtime_kind_check;
ALTER TABLE mcp_capture_tokens ADD CONSTRAINT mcp_capture_tokens_runtime_kind_check
    CHECK (runtime_kind IN ('codex','claude','hermes','grok','openclaw'));

ALTER TABLE mcp_servers
    ADD COLUMN authority text NOT NULL DEFAULT 'toolhub'
        CONSTRAINT mcp_servers_authority_check CHECK (authority IN ('toolhub','shared-file')),
    ADD COLUMN shared_source_id uuid REFERENCES shared_sources(id) ON DELETE CASCADE,
    ADD COLUMN header_refs jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN credential_mode text NOT NULL DEFAULT 'toolhub-secret'
        CONSTRAINT mcp_servers_credential_mode_check CHECK (credential_mode IN ('toolhub-secret','node-local'));

ALTER TABLE mcp_servers ADD CONSTRAINT mcp_servers_authority_source_check CHECK (
    (authority='toolhub' AND shared_source_id IS NULL AND credential_mode='toolhub-secret')
    OR
    (authority='shared-file' AND shared_source_id IS NOT NULL AND credential_mode='node-local')
);

ALTER TABLE mcp_servers DROP CONSTRAINT mcp_servers_name_key;
CREATE UNIQUE INDEX mcp_servers_toolhub_name_idx
    ON mcp_servers(name)
    WHERE authority='toolhub';

CREATE UNIQUE INDEX mcp_servers_shared_source_name_idx
    ON mcp_servers(shared_source_id, runtime_name)
    WHERE authority='shared-file';

ALTER TABLE mcp_runtime_bindings
    ADD COLUMN shared_source_id uuid REFERENCES shared_sources(id) ON DELETE CASCADE,
    ADD COLUMN header_keys jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN desired_fingerprint text NOT NULL DEFAULT '',
    ADD COLUMN actual_fingerprint text NOT NULL DEFAULT '';

UPDATE mcp_runtime_bindings
SET desired_fingerprint=desired_config_fingerprint,
    actual_fingerprint=observed_config_fingerprint
WHERE desired_fingerprint='' AND actual_fingerprint='';

CREATE INDEX mcp_runtime_bindings_shared_source_idx
    ON mcp_runtime_bindings(shared_source_id, runtime_kind, server_name);
