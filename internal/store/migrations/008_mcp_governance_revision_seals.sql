-- Child rows participate in immutable revision hashes. Seal each parent only
-- after its complete child set is inserted, then reject every later append.

CREATE TABLE relay_configuration_revision_seals (
    relay_configuration_revision_id uuid PRIMARY KEY REFERENCES relay_configuration_revisions(id) ON DELETE CASCADE,
    sealed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mcp_contract_revision_seals (
    contract_revision_id uuid PRIMARY KEY REFERENCES mcp_contract_revisions(id) ON DELETE CASCADE,
    sealed_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO relay_configuration_revision_seals(relay_configuration_revision_id)
SELECT id FROM relay_configuration_revisions;

INSERT INTO mcp_contract_revision_seals(contract_revision_id)
SELECT id FROM mcp_contract_revisions;

CREATE FUNCTION reject_sealed_relay_configuration_member_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM relay_configuration_revision_seals
        WHERE relay_configuration_revision_id = NEW.relay_configuration_revision_id
    ) THEN
        RAISE EXCEPTION 'relay configuration revision members are immutable';
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION reject_sealed_contract_tool_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM mcp_contract_revision_seals
        WHERE contract_revision_id = NEW.contract_revision_id
    ) THEN
        RAISE EXCEPTION 'contract revision tools are immutable';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER relay_configuration_members_immutable_insert
BEFORE INSERT ON relay_configuration_revision_mcp_servers
FOR EACH ROW EXECUTE FUNCTION reject_sealed_relay_configuration_member_insert();

CREATE TRIGGER mcp_contract_tools_immutable_insert
BEFORE INSERT ON mcp_contract_revision_tools
FOR EACH ROW EXECUTE FUNCTION reject_sealed_contract_tool_insert();

CREATE TRIGGER relay_configuration_revision_seals_immutable
BEFORE UPDATE OR DELETE ON relay_configuration_revision_seals
FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();

CREATE TRIGGER mcp_contract_revision_seals_immutable
BEFORE UPDATE OR DELETE ON mcp_contract_revision_seals
FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();

CREATE FUNCTION require_relay_configuration_revision_seal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM relay_configuration_revision_seals
        WHERE relay_configuration_revision_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'relay configuration revision must be sealed before commit';
    END IF;
    RETURN NULL;
END $$;

CREATE FUNCTION require_mcp_contract_revision_seal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM mcp_contract_revision_seals
        WHERE contract_revision_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'contract revision must be sealed before commit';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER relay_configuration_revision_requires_seal
AFTER INSERT ON relay_configuration_revisions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION require_relay_configuration_revision_seal();

CREATE CONSTRAINT TRIGGER mcp_contract_revision_requires_seal
AFTER INSERT ON mcp_contract_revisions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION require_mcp_contract_revision_seal();
