-- Remove residual operation/target history whose result payloads predate the
-- mcpm ownership boundary. Migration 013 removed rows with stale request
-- manifests; this follow-up also covers relay health observations that only
-- mentioned retired members in the result projection.

CREATE TEMP TABLE toolhub_retired_mcp_names(name text PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_retired_mcp_names(name) VALUES
    ('desktop-commander'), ('memory'), ('sequential-thinking');

CREATE TEMP TABLE toolhub_retired_skill_slugs(slug text PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_retired_skill_slugs(slug) VALUES
    ('baoyu-format-markdown'), ('baoyu-translate'), ('baoyu-url-to-markdown'),
    ('codex-build'), ('codex-review'), ('grill-me-codex'),
    ('grill-with-docs-codex'), ('slides'), ('using-superpowers'),
    ('workflow-runner');

CREATE TEMP TABLE toolhub_retired_operation_targets(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_retired_operation_targets(id)
SELECT DISTINCT ot.id
FROM operation_targets ot
WHERE ot.request->>'name' IN (SELECT slug FROM toolhub_retired_skill_slugs)
   OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(COALESCE(ot.request->'manifest'->'mcpServers','[]'::jsonb)) member
       WHERE member->>'name' IN (SELECT name FROM toolhub_retired_mcp_names)
   )
   OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(COALESCE(ot.request->'manifest'->'skills','[]'::jsonb)) member
       WHERE member->>'slug' IN (SELECT slug FROM toolhub_retired_skill_slugs)
   )
   OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(COALESCE(ot.result->'relay'->'memberStatuses','[]'::jsonb)) member
       WHERE member->>'name' IN (SELECT name FROM toolhub_retired_mcp_names)
   )
   OR ot.result->'details'->>'name' IN (SELECT slug FROM toolhub_retired_skill_slugs);

CREATE TEMP TABLE toolhub_retired_operations(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_retired_operations(id)
SELECT DISTINCT operation_id FROM operation_targets
WHERE id IN (SELECT id FROM toolhub_retired_operation_targets);
INSERT INTO toolhub_retired_operations(id)
SELECT id FROM operations
WHERE metadata->>'name' IN (SELECT slug FROM toolhub_retired_skill_slugs)
ON CONFLICT DO NOTHING;

CREATE TEMP TABLE toolhub_retired_backups(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO toolhub_retired_backups(id)
SELECT DISTINCT b.id
FROM backups b
WHERE EXISTS (
    SELECT 1
    FROM jsonb_array_elements(COALESCE(b.metadata->'desiredManifest'->'mcpServers','[]'::jsonb)) member
    WHERE member->>'name' IN (SELECT name FROM toolhub_retired_mcp_names)
)
OR EXISTS (
    SELECT 1
    FROM jsonb_array_elements(COALESCE(b.metadata->'desiredManifest'->'skills','[]'::jsonb)) member
    WHERE member->>'slug' IN (SELECT slug FROM toolhub_retired_skill_slugs)
)
OR EXISTS (
    SELECT 1
    FROM jsonb_array_elements(COALESCE(b.metadata->'manifest'->'mcpServers','[]'::jsonb)) member
    WHERE member->>'name' IN (SELECT name FROM toolhub_retired_mcp_names)
)
OR EXISTS (
    SELECT 1
    FROM jsonb_array_elements(COALESCE(b.metadata->'manifest'->'skills','[]'::jsonb)) member
    WHERE member->>'slug' IN (SELECT slug FROM toolhub_retired_skill_slugs)
);

UPDATE operation_targets
SET depends_on_target_id = NULL
WHERE depends_on_target_id IN (SELECT id FROM toolhub_retired_operation_targets);

DELETE FROM operation_targets
WHERE id IN (SELECT id FROM toolhub_retired_operation_targets);

DELETE FROM backups
WHERE id IN (SELECT id FROM toolhub_retired_backups)
   OR source_operation_id IN (
       SELECT id
       FROM toolhub_retired_operations
       WHERE NOT EXISTS (
           SELECT 1 FROM operation_targets ot
           WHERE ot.operation_id=toolhub_retired_operations.id
       )
   );

DELETE FROM operations
WHERE id IN (
    SELECT id
    FROM toolhub_retired_operations
    WHERE NOT EXISTS (
        SELECT 1 FROM operation_targets ot
        WHERE ot.operation_id=toolhub_retired_operations.id
    )
);

DELETE FROM audit_events
WHERE metadata->>'name' IN (SELECT slug FROM toolhub_retired_skill_slugs);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM operation_targets ot
        WHERE ot.request->>'name' IN (SELECT slug FROM toolhub_retired_skill_slugs)
           OR EXISTS (
               SELECT 1
               FROM jsonb_array_elements(COALESCE(ot.result->'relay'->'memberStatuses','[]'::jsonb)) member
               WHERE member->>'name' IN (SELECT name FROM toolhub_retired_mcp_names)
           )
    ) THEN
        RAISE EXCEPTION 'retired operation history cleanup incomplete';
    END IF;
END $$;
