-- Preserve the 006 routing validator while aligning Profile display names with
-- the Store contract and binding the manifest schema column to its JSON body.

ALTER FUNCTION validate_routing_bundle_v1(jsonb) RENAME TO validate_routing_bundle_v1_006;

CREATE FUNCTION validate_routing_bundle_v1(bundle jsonb) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    normalized_profiles jsonb;
    normalized_bundle jsonb;
BEGIN
    IF bundle IS NULL
       OR jsonb_typeof(bundle) <> 'object'
       OR jsonb_typeof(bundle->'profiles') <> 'array'
       OR EXISTS (
           SELECT 1
           FROM jsonb_array_elements(bundle->'profiles') profile
           WHERE jsonb_typeof(profile.value) <> 'object'
              OR jsonb_typeof(profile.value->'profileName') <> 'string'
              OR profile.value->>'profileName' <> btrim(profile.value->>'profileName')
              OR length(profile.value->>'profileName') = 0
              OR octet_length(profile.value->>'profileName') > 120
       )
       OR (
           SELECT count(*) <> count(DISTINCT profile.value->>'profileName')
           FROM jsonb_array_elements(bundle->'profiles') profile
       ) THEN
        RETURN false;
    END IF;

    SELECT coalesce(
        jsonb_agg(
            jsonb_set(profile.value,'{profileName}',to_jsonb(profile.value->>'profileId'),false)
            ORDER BY profile.ordinality
        ),
        '[]'::jsonb
    )
    INTO normalized_profiles
    FROM jsonb_array_elements(bundle->'profiles') WITH ORDINALITY profile(value,ordinality);

    normalized_bundle := jsonb_set(bundle,'{profiles}',normalized_profiles,false);
    RETURN validate_routing_bundle_v1_006(normalized_bundle);
EXCEPTION WHEN others THEN
    RETURN false;
END $$;

ALTER TABLE desired_snapshots ADD CONSTRAINT desired_snapshots_manifest_schema_match_check CHECK (
    jsonb_typeof(manifest->'schemaVersion') = 'number'
    AND manifest->>'schemaVersion' IN ('1','2')
    AND manifest_schema_version = (manifest->>'schemaVersion')::integer
) NOT VALID;

ALTER TABLE desired_snapshots VALIDATE CONSTRAINT desired_snapshots_manifest_schema_match_check;
