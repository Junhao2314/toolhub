-- Bind persisted Manifest v2 routing bytes to their RFC 8785 hash and repair
-- deterministic Contract tool statuses for databases that already applied 005.

ALTER TABLE desired_snapshots DROP CONSTRAINT desired_snapshots_manifest_check;
DROP FUNCTION validate_desired_manifest(jsonb);

CREATE FUNCTION validate_routing_bundle_v1(bundle jsonb) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    server jsonb;
    tool jsonb;
    profile jsonb;
    profile_server jsonb;
    referenced_server jsonb;
    referenced_tool jsonb;
    item jsonb;
    server_ids text[] := ARRAY[]::text[];
    server_names text[] := ARRAY[]::text[];
    profile_ids text[] := ARRAY[]::text[];
    profile_names text[] := ARRAY[]::text[];
    runtime_tool_names text[] := ARRAY[]::text[];
    local_ids text[];
    local_names text[];
    override_ids text[];
    rule_ids text[];
    runtime_tool_name text;
    total_tools integer := 0;
    total_rules integer := 0;
BEGIN
    IF bundle IS NULL
       OR jsonb_typeof(bundle) <> 'object'
       OR octet_length(bundle::text) > 1048576
       OR bundle - ARRAY['schemaVersion','mode','relayConfigurationRevisionId','relayConfigurationHash','globalPolicyRevisionId','globalPolicyHash','defaultProfileId','servers','profiles'] <> '{}'::jsonb
       OR NOT (bundle ?& ARRAY['schemaVersion','mode','relayConfigurationRevisionId','relayConfigurationHash','globalPolicyRevisionId','globalPolicyHash','defaultProfileId','servers','profiles'])
       OR jsonb_typeof(bundle->'schemaVersion') <> 'number'
       OR bundle->>'schemaVersion' <> '1'
       OR jsonb_typeof(bundle->'mode') <> 'string'
       OR bundle->>'mode' NOT IN ('compatibility','enforced')
       OR jsonb_typeof(bundle->'relayConfigurationRevisionId') <> 'string'
       OR bundle->>'relayConfigurationRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
       OR jsonb_typeof(bundle->'relayConfigurationHash') <> 'string'
       OR bundle->>'relayConfigurationHash' !~ '^[a-f0-9]{64}$'
       OR jsonb_typeof(bundle->'globalPolicyRevisionId') <> 'string'
       OR bundle->>'globalPolicyRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
       OR jsonb_typeof(bundle->'globalPolicyHash') <> 'string'
       OR bundle->>'globalPolicyHash' !~ '^[a-f0-9]{64}$'
       OR jsonb_typeof(bundle->'defaultProfileId') NOT IN ('null','string')
       OR (jsonb_typeof(bundle->'defaultProfileId') = 'string' AND bundle->>'defaultProfileId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$')
       OR jsonb_typeof(bundle->'servers') <> 'array'
       OR jsonb_array_length(bundle->'servers') > 500
       OR jsonb_typeof(bundle->'profiles') <> 'array'
       OR jsonb_array_length(bundle->'profiles') > 100 THEN
        RETURN false;
    END IF;

    FOR server IN SELECT value FROM jsonb_array_elements(bundle->'servers') LOOP
        IF jsonb_typeof(server) <> 'object'
           OR server - ARRAY['serverId','serverName','mcpConfigRevisionId','acceptedContractRevisionId','acceptedContractHash','tools'] <> '{}'::jsonb
           OR NOT (server ?& ARRAY['serverId','serverName','mcpConfigRevisionId','acceptedContractRevisionId','acceptedContractHash','tools'])
           OR jsonb_typeof(server->'serverId') <> 'string'
           OR server->>'serverId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
           OR jsonb_typeof(server->'serverName') <> 'string'
           OR server->>'serverName' !~ '^[a-z0-9][a-z0-9._-]{0,127}$'
           OR jsonb_typeof(server->'mcpConfigRevisionId') <> 'string'
           OR server->>'mcpConfigRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
           OR jsonb_typeof(server->'acceptedContractRevisionId') NOT IN ('null','string')
           OR jsonb_typeof(server->'acceptedContractHash') NOT IN ('null','string')
           OR (jsonb_typeof(server->'acceptedContractRevisionId') = 'null') <> (jsonb_typeof(server->'acceptedContractHash') = 'null')
           OR (jsonb_typeof(server->'acceptedContractRevisionId') = 'string' AND server->>'acceptedContractRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$')
           OR (jsonb_typeof(server->'acceptedContractHash') = 'string' AND server->>'acceptedContractHash' !~ '^[a-f0-9]{64}$')
           OR (bundle->>'mode' = 'enforced' AND jsonb_typeof(server->'acceptedContractRevisionId') = 'null')
           OR jsonb_typeof(server->'tools') <> 'array' THEN
            RETURN false;
        END IF;
        IF server->>'serverId' = ANY(server_ids) OR server->>'serverName' = ANY(server_names) THEN
            RETURN false;
        END IF;
        server_ids := array_append(server_ids,server->>'serverId');
        server_names := array_append(server_names,server->>'serverName');
        local_ids := ARRAY[]::text[];
        local_names := ARRAY[]::text[];
        FOR tool IN SELECT value FROM jsonb_array_elements(server->'tools') LOOP
            IF jsonb_typeof(tool) <> 'object'
               OR tool - ARRAY['toolId','name','inputSchema','outputSchema','annotations','globalDecision','reasonCodes','paused'] <> '{}'::jsonb
               OR NOT (tool ?& ARRAY['toolId','name','inputSchema','outputSchema','annotations','globalDecision','reasonCodes','paused'])
               OR jsonb_typeof(tool->'toolId') <> 'string'
               OR tool->>'toolId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
               OR jsonb_typeof(tool->'name') <> 'string'
               OR tool->>'name' !~ '^[a-z0-9][a-z0-9._-]{0,127}$'
               OR jsonb_typeof(tool->'inputSchema') <> 'object'
               OR jsonb_typeof(tool->'outputSchema') NOT IN ('null','object')
               OR jsonb_typeof(tool->'annotations') <> 'object'
               OR jsonb_typeof(tool->'globalDecision') <> 'string'
               OR tool->>'globalDecision' NOT IN ('allow','confirm','deny')
               OR jsonb_typeof(tool->'reasonCodes') <> 'array'
               OR jsonb_array_length(tool->'reasonCodes') > 16
               OR EXISTS(SELECT 1 FROM jsonb_array_elements(tool->'reasonCodes') reason WHERE jsonb_typeof(reason.value) <> 'string')
               OR jsonb_typeof(tool->'paused') <> 'boolean'
               OR tool->>'toolId' = ANY(local_ids)
               OR tool->>'name' = ANY(local_names) THEN
                RETURN false;
            END IF;
            local_ids := array_append(local_ids,tool->>'toolId');
            local_names := array_append(local_names,tool->>'name');
            runtime_tool_name := tool->>'name';
            IF jsonb_array_length(bundle->'servers') > 1 THEN
                runtime_tool_name := server->>'serverName' || '_' || runtime_tool_name;
            END IF;
            IF runtime_tool_name = ANY(runtime_tool_names) THEN
                RETURN false;
            END IF;
            runtime_tool_names := array_append(runtime_tool_names,runtime_tool_name);
            total_tools := total_tools + 1;
            IF total_tools > 10000 THEN
                RETURN false;
            END IF;
        END LOOP;
    END LOOP;

    IF EXISTS (
        SELECT 1
        FROM unnest(server_names) AS left_server(name)
        CROSS JOIN unnest(server_names) AS right_server(name)
        WHERE left_server.name <> right_server.name
          AND left(right_server.name,length(left_server.name)+1) = left_server.name || '_'
    ) THEN
        RETURN false;
    END IF;

    FOR profile IN SELECT value FROM jsonb_array_elements(bundle->'profiles') LOOP
        IF jsonb_typeof(profile) <> 'object'
           OR profile - ARRAY['profileId','profileRevisionId','profileRevisionHash','profileName','clientKind','servers'] <> '{}'::jsonb
           OR NOT (profile ?& ARRAY['profileId','profileRevisionId','profileRevisionHash','profileName','clientKind','servers'])
           OR jsonb_typeof(profile->'profileId') <> 'string'
           OR profile->>'profileId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
           OR jsonb_typeof(profile->'profileRevisionId') <> 'string'
           OR profile->>'profileRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
           OR jsonb_typeof(profile->'profileRevisionHash') <> 'string'
           OR profile->>'profileRevisionHash' !~ '^[a-f0-9]{64}$'
           OR jsonb_typeof(profile->'profileName') <> 'string'
           OR profile->>'profileName' !~ '^[a-z0-9][a-z0-9._-]{0,127}$'
           OR jsonb_typeof(profile->'clientKind') <> 'string'
           OR profile->>'clientKind' NOT IN ('claude','codex')
           OR jsonb_typeof(profile->'servers') <> 'array'
           OR profile->>'profileId' = ANY(profile_ids)
           OR profile->>'profileName' = ANY(profile_names) THEN
            RETURN false;
        END IF;
        profile_ids := array_append(profile_ids,profile->>'profileId');
        profile_names := array_append(profile_names,profile->>'profileName');
        local_ids := ARRAY[]::text[];
        FOR profile_server IN SELECT value FROM jsonb_array_elements(profile->'servers') LOOP
            IF jsonb_typeof(profile_server) <> 'object'
               OR profile_server - ARRAY['serverId','mcpConfigRevisionId','acceptedContractRevisionId','visibilityMode','toolOverrides','toolRules'] <> '{}'::jsonb
               OR NOT (profile_server ?& ARRAY['serverId','mcpConfigRevisionId','acceptedContractRevisionId','visibilityMode','toolOverrides','toolRules'])
               OR jsonb_typeof(profile_server->'serverId') <> 'string'
               OR profile_server->>'serverId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
               OR jsonb_typeof(profile_server->'mcpConfigRevisionId') <> 'string'
               OR profile_server->>'mcpConfigRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
               OR jsonb_typeof(profile_server->'acceptedContractRevisionId') NOT IN ('null','string')
               OR (jsonb_typeof(profile_server->'acceptedContractRevisionId') = 'string' AND profile_server->>'acceptedContractRevisionId' !~ '^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$')
               OR jsonb_typeof(profile_server->'visibilityMode') <> 'string'
               OR profile_server->>'visibilityMode' NOT IN ('all_accepted','selected','hidden')
               OR jsonb_typeof(profile_server->'toolOverrides') <> 'array'
               OR jsonb_typeof(profile_server->'toolRules') <> 'array'
               OR profile_server->>'serverId' = ANY(local_ids) THEN
                RETURN false;
            END IF;
            SELECT value INTO referenced_server FROM jsonb_array_elements(bundle->'servers') WHERE value->>'serverId'=profile_server->>'serverId';
            IF referenced_server IS NULL
               OR profile_server->>'mcpConfigRevisionId' <> referenced_server->>'mcpConfigRevisionId'
               OR profile_server->'acceptedContractRevisionId' <> referenced_server->'acceptedContractRevisionId'
               OR (bundle->>'mode' = 'enforced' AND jsonb_typeof(profile_server->'acceptedContractRevisionId') = 'null') THEN
                RETURN false;
            END IF;
            local_ids := array_append(local_ids,profile_server->>'serverId');
            override_ids := ARRAY[]::text[];
            FOR item IN SELECT value FROM jsonb_array_elements(profile_server->'toolOverrides') LOOP
                IF jsonb_typeof(item) <> 'object'
                   OR item - ARRAY['toolId','visible'] <> '{}'::jsonb
                   OR NOT (item ?& ARRAY['toolId','visible'])
                   OR jsonb_typeof(item->'toolId') <> 'string'
                   OR jsonb_typeof(item->'visible') <> 'boolean'
                   OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(referenced_server->'tools') server_tool WHERE server_tool->>'toolId'=item->>'toolId')
                   OR item->>'toolId' = ANY(override_ids) THEN
                    RETURN false;
                END IF;
                override_ids := array_append(override_ids,item->>'toolId');
            END LOOP;
            rule_ids := ARRAY[]::text[];
            FOR item IN SELECT value FROM jsonb_array_elements(profile_server->'toolRules') LOOP
                SELECT value INTO referenced_tool
                FROM jsonb_array_elements(referenced_server->'tools') server_tool
                WHERE server_tool->>'toolId'=item->>'toolId';
                IF jsonb_typeof(item) <> 'object'
                   OR item - ARRAY['toolId','decision'] <> '{}'::jsonb
                   OR NOT (item ?& ARRAY['toolId','decision'])
                   OR jsonb_typeof(item->'toolId') <> 'string'
                   OR jsonb_typeof(item->'decision') <> 'string'
                   OR item->>'decision' NOT IN ('allow','confirm','deny')
                   OR referenced_tool IS NULL
                   OR item->>'toolId' = ANY(rule_ids)
                   OR (CASE item->>'decision' WHEN 'allow' THEN 0 WHEN 'confirm' THEN 1 ELSE 2 END)
                      < (CASE referenced_tool->>'globalDecision' WHEN 'allow' THEN 0 WHEN 'confirm' THEN 1 ELSE 2 END) THEN
                    RETURN false;
                END IF;
                rule_ids := array_append(rule_ids,item->>'toolId');
                total_rules := total_rules + 1;
                IF total_rules > 20000 THEN
                    RETURN false;
                END IF;
            END LOOP;
        END LOOP;
    END LOOP;
    IF jsonb_typeof(bundle->'defaultProfileId') = 'string' AND NOT (bundle->>'defaultProfileId' = ANY(profile_ids)) THEN
        RETURN false;
    END IF;
    RETURN true;
EXCEPTION WHEN others THEN
    RETURN false;
END $$;

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
       OR NOT validate_routing_bundle_v1(governance->'routingBundle')
       OR governance->>'routingHash' !~ '^[a-f0-9]{64}$' THEN
        RETURN false;
    END IF;
    legacy_body := jsonb_set(body - 'relayGovernance', '{schemaVersion}', '1'::jsonb, true);
    RETURN validate_desired_manifest_v1(legacy_body);
EXCEPTION WHEN others THEN
    RETURN false;
END $$;

ALTER TABLE desired_snapshots ADD CONSTRAINT desired_snapshots_manifest_check CHECK (validate_desired_manifest(manifest));
ALTER TABLE desired_snapshots ADD COLUMN routing_bundle_canonical bytea;
ALTER TABLE desired_snapshots ADD CONSTRAINT desired_snapshots_routing_bundle_canonical_check CHECK (
    (manifest_schema_version = 1 AND routing_bundle_canonical IS NULL)
    OR
    (manifest_schema_version = 2
     AND routing_bundle_canonical IS NOT NULL
     AND octet_length(routing_bundle_canonical) BETWEEN 2 AND 1048576
	     AND convert_from(routing_bundle_canonical,'UTF8')::jsonb = manifest->'relayGovernance'->'routingBundle'
	     AND encode(sha256(routing_bundle_canonical),'hex') = manifest->'relayGovernance'->>'routingHash')
) NOT VALID;

DROP TRIGGER mcp_contract_tools_immutable ON mcp_contract_revision_tools;

WITH classified AS (
    SELECT current_tools.contract_revision_id,
           current_tools.tool_id,
           CASE
             WHEN previous_revision.id IS NULL THEN 'unchanged'
             WHEN previous_tools.tool_id IS NULL THEN 'new_hidden'
             WHEN current_tools.input_schema IS DISTINCT FROM previous_tools.input_schema
               OR current_tools.output_schema IS DISTINCT FROM previous_tools.output_schema
               OR current_tools.annotations IS DISTINCT FROM previous_tools.annotations THEN 'paused_incompatible'
             WHEN current_tools.presentation IS DISTINCT FROM previous_tools.presentation THEN 'changed_presentation'
             ELSE 'unchanged'
           END AS status
    FROM mcp_contract_revision_tools current_tools
    JOIN mcp_contract_revisions current_revision ON current_revision.id=current_tools.contract_revision_id
    LEFT JOIN LATERAL (
        SELECT previous.id
        FROM mcp_contract_revisions previous
        WHERE previous.server_id=current_revision.server_id
          AND previous.revision<current_revision.revision
        ORDER BY previous.revision DESC
        LIMIT 1
    ) previous_revision ON true
    LEFT JOIN mcp_contract_revision_tools previous_tools
      ON previous_tools.contract_revision_id=previous_revision.id
     AND previous_tools.tool_id=current_tools.tool_id
)
UPDATE mcp_contract_revision_tools tools
SET status=classified.status
FROM classified
WHERE tools.contract_revision_id=classified.contract_revision_id
  AND tools.tool_id=classified.tool_id;

CREATE TRIGGER mcp_contract_tools_immutable BEFORE UPDATE OR DELETE ON mcp_contract_revision_tools
FOR EACH ROW EXECUTE FUNCTION reject_governance_revision_mutation();
