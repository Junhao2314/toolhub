-- Persist the legacy shared-mcp transition separately from immutable Profile revisions.

ALTER TABLE relay_configuration_state
    ADD COLUMN legacy_profile_id uuid REFERENCES profiles(id),
    ADD COLUMN legacy_profile_state text NOT NULL DEFAULT 'pending'
        CHECK (legacy_profile_state IN ('pending','migrated_relay'));

UPDATE relay_configuration_state state
SET legacy_profile_id = profile.id
FROM profiles profile
WHERE state.singleton
  AND profile.name = 'shared-mcp'
  AND state.legacy_profile_id IS NULL;
