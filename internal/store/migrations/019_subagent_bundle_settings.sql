-- Keep external subagent dispatch bundles operator-configurable without
-- conflating them with Claude/Codex runtime Profiles.  Entries are Skill
-- slugs; the materializers resolve them against their configured source roots.
ALTER TABLE settings
    ADD COLUMN kimi_frontend_bundle text[] NOT NULL DEFAULT ARRAY[
        'ui-ux-pro-max-cn',
        'responsive-check',
        'performance-audit',
        'browser-ui-verification'
    ]::text[],
    ADD COLUMN pi_bundle text[] NOT NULL DEFAULT '{}';

ALTER TABLE settings
    ADD CONSTRAINT settings_kimi_frontend_bundle_size_check
        CHECK (cardinality(kimi_frontend_bundle) <= 100),
    ADD CONSTRAINT settings_pi_bundle_size_check
        CHECK (cardinality(pi_bundle) <= 100);
