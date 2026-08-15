-- Align the persisted v2 manifest with the typed Bridge contract introduced
-- after migration 004. Migration 004 remains immutable for deployed databases.

ALTER TABLE desired_snapshots DROP CONSTRAINT desired_snapshots_manifest_check;
DROP FUNCTION validate_desired_manifest(jsonb);

CREATE FUNCTION validate_desired_manifest(body jsonb) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    schema_version integer;
    governance jsonb;
    legacy_body jsonb;
BEGIN
    IF body IS NULL OR jsonb_typeof(body) <> 'object' OR jsonb_typeof(body->'schemaVersion') <> 'number' THEN
        RETURN false;
    END IF;
    schema_version := (body->>'schemaVersion')::integer;
    IF schema_version = 1 THEN
        RETURN validate_desired_manifest_v1(body);
    END IF;
    IF schema_version <> 2
       OR body - ARRAY['schemaVersion','target','profileId','profileRevision','skills','mcpServers','managedMemberIds','relayPort','relayGovernance'] <> '{}'::jsonb
       OR NOT (body ?& ARRAY['schemaVersion','target','skills','mcpServers','managedMemberIds','relayGovernance'])
       OR jsonb_typeof(body->'target') <> 'object'
       OR body->'target'->>'runtime' <> 'shared-relay' THEN
        RETURN false;
    END IF;
    governance := body->'relayGovernance';
    IF jsonb_typeof(governance) <> 'object'
       OR governance - ARRAY['relayConfigurationRevisionId','relayConfigurationHash','routingBundle','routingHash'] <> '{}'::jsonb
       OR NOT (governance ?& ARRAY['relayConfigurationRevisionId','relayConfigurationHash','routingBundle','routingHash'])
       OR (SELECT count(*) FROM jsonb_object_keys(governance)) <> 4
       OR governance->>'relayConfigurationRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
       OR governance->>'relayConfigurationHash' !~ '^[a-f0-9]{64}$'
       OR jsonb_typeof(governance->'routingBundle') <> 'object'
       OR governance->>'routingHash' !~ '^[a-f0-9]{64}$' THEN
        RETURN false;
    END IF;
    legacy_body := jsonb_set(body - 'relayGovernance', '{schemaVersion}', '1'::jsonb, true);
    RETURN validate_desired_manifest_v1(legacy_body);
EXCEPTION WHEN others THEN
    RETURN false;
END $$;

ALTER TABLE desired_snapshots ADD CONSTRAINT desired_snapshots_manifest_check CHECK (validate_desired_manifest(manifest));

ALTER TABLE mcp_contract_revision_tools
    ADD COLUMN status text NOT NULL DEFAULT 'unchanged'
        CHECK (status IN ('unchanged','new_hidden','paused_incompatible','changed_presentation'));

CREATE TABLE profile_revision_seals (
    profile_revision_id uuid PRIMARY KEY REFERENCES profile_revisions(id) ON DELETE CASCADE,
    sealed_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO profile_revision_seals(profile_revision_id)
SELECT id FROM profile_revisions
ON CONFLICT DO NOTHING;

-- Membership rows are immutable after the owning revision is sealed by the
-- application transaction. The trigger function is shared by the next store
-- patch, which seals rows immediately after inserting a revision.
CREATE FUNCTION profile_revision_mcp_governance_immutable_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM profile_revision_seals WHERE profile_revision_id = NEW.profile_revision_id) THEN
        RAISE EXCEPTION 'profile revision governance is immutable';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER profile_revision_skills_immutable_insert BEFORE INSERT ON profile_revision_skills
FOR EACH ROW EXECUTE FUNCTION profile_revision_mcp_governance_immutable_insert();
CREATE TRIGGER profile_revision_mcp_servers_immutable_insert BEFORE INSERT ON profile_revision_mcp_servers
FOR EACH ROW EXECUTE FUNCTION profile_revision_mcp_governance_immutable_insert();
CREATE TRIGGER profile_revision_mcp_governance_immutable_insert_trigger BEFORE INSERT ON profile_revision_mcp_governance
FOR EACH ROW EXECUTE FUNCTION profile_revision_mcp_governance_immutable_insert();
CREATE TRIGGER profile_revision_tool_rules_immutable_insert BEFORE INSERT ON profile_revision_tool_rules
FOR EACH ROW EXECUTE FUNCTION profile_revision_mcp_governance_immutable_insert();
CREATE TRIGGER profile_revision_skills_immutable AFTER UPDATE OR DELETE ON profile_revision_skills
FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
CREATE TRIGGER profile_revision_mcp_servers_immutable AFTER UPDATE OR DELETE ON profile_revision_mcp_servers
FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
