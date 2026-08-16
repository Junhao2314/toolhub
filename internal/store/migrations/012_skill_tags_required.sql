ALTER TABLE skills
    ADD COLUMN tags text[] NOT NULL DEFAULT '{}'::text[]
        CHECK (cardinality(tags) <= 50 AND array_position(tags, NULL) IS NULL);

-- These are the common runtime Skills already present in every Claude/Codex
-- Profile. Marking them required makes the existing Profile set valid while
-- allowing future Skill tags to be managed explicitly.
UPDATE skills
SET tags = ARRAY['required']::text[], updated_at = now()
WHERE archived_at IS NULL
  AND slug IN (
      'cloakbrowser-agent-browser-guard',
      'firecrawl',
      'grill-me',
      'grill-with-docs',
      'grilling',
      'mcp-builder',
      'qu-ai-wei',
      'same-model-subagents'
  );
