package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
)

func upsertSharedInventoryTx(ctx context.Context, tx pgx.Tx, nodeID string, sources []domain.SharedSourceInventory) error {
	if _, err := tx.Exec(ctx, `UPDATE shared_sources SET status='missing',last_error='shared source was omitted from the latest inventory',updated_at=now() WHERE node_id=$1`, nodeID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE skill_discoveries SET missing=true,updated_at=now() WHERE node_id=$1 AND runtime_kind='shared'`, nodeID); err != nil {
		return err
	}
	for _, source := range sources {
		if _, err := upsertSharedSourceTx(ctx, tx, nodeID, source); err != nil {
			return err
		}
	}
	return nil
}

func upsertSharedSourceTx(ctx context.Context, tx pgx.Tx, nodeID string, source domain.SharedSourceInventory) (string, error) {
	if err := validateSharedSourceInventory(source); err != nil {
		return "", err
	}
	var sourceID string
	err := tx.QueryRow(ctx, `INSERT INTO shared_sources(id,node_id,name,mode,auto_sync,skills_root,mcp_manifest_path,config_fingerprint,source_fingerprint,status,last_scan_at,last_error)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now(),$11)
		ON CONFLICT(node_id,name) DO UPDATE SET mode=excluded.mode,auto_sync=excluded.auto_sync,skills_root=excluded.skills_root,
		mcp_manifest_path=excluded.mcp_manifest_path,config_fingerprint=excluded.config_fingerprint,source_fingerprint=excluded.source_fingerprint,
		status=excluded.status,last_scan_at=now(),last_error=excluded.last_error,updated_at=now()
		RETURNING id::text`, uuid.NewString(), nodeID, source.Name, source.Mode, source.AutoSync, source.SkillsRoot, source.MCPManifestPath,
		source.ConfigFingerprint, source.SourceFingerprint, source.Status, truncateSharedError(source.LastError)).Scan(&sourceID)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE skill_discoveries SET missing=true,updated_at=now()
		WHERE node_id=$1 AND runtime_kind='shared' AND (canonical_path=$2 OR canonical_path LIKE $2 || '/%')`, nodeID, source.SkillsRoot); err != nil {
		return "", err
	}
	if err := upsertSharedSkillsTx(ctx, tx, nodeID, sourceID, source.Skills); err != nil {
		return "", err
	}
	if err := upsertSharedMCPServersTx(ctx, tx, nodeID, sourceID, source); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE shared_consumers SET state='missing',last_error='consumer was omitted from the latest inventory',updated_at=now() WHERE source_id=$1`, sourceID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE shared_skill_links SET state='missing',last_error='link was omitted from the latest inventory',updated_at=now() WHERE source_id=$1`, sourceID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET missing=true,drift=desired_enabled,updated_at=now() WHERE shared_source_id=$1`, sourceID); err != nil {
		return "", err
	}
	for _, consumer := range source.Consumers {
		if err := upsertSharedConsumerTx(ctx, tx, nodeID, sourceID, consumer, source.MCPServers); err != nil {
			return "", err
		}
	}
	return sourceID, nil
}

func validateSharedSourceInventory(source domain.SharedSourceInventory) error {
	if strings.TrimSpace(source.Name) == "" || len(source.Name) > 100 {
		return errors.New("shared inventory contains an invalid source name")
	}
	if source.Mode != "observed" && source.Mode != "managed" {
		return errors.New("shared inventory contains an invalid source mode")
	}
	validStatus := map[string]bool{"observed": true, "in_sync": true, "drift": true, "conflict": true, "blocked": true, "failed": true, "missing": true}
	if !validStatus[source.Status] {
		return errors.New("shared inventory contains an invalid source status")
	}
	if strings.TrimSpace(source.SkillsRoot) == "" || strings.TrimSpace(source.MCPManifestPath) == "" {
		return errors.New("shared inventory paths are required")
	}
	return nil
}

func upsertSharedSkillsTx(ctx context.Context, tx pgx.Tx, nodeID, sourceID string, skills []domain.SharedSkillInventory) error {
	for _, skill := range skills {
		if strings.TrimSpace(skill.Name) == "" || strings.TrimSpace(skill.SourcePath) == "" {
			return errors.New("shared inventory contains an invalid Skill")
		}
		_, err := tx.Exec(ctx, `INSERT INTO skill_discoveries(id,node_id,runtime_kind,canonical_path,name,directory_hash,managed,protected,disabled,missing,drift,scan_error,last_seen_at)
			VALUES($1,$2,'shared',$3,$4,$5,$6,false,false,false,$7,$8,now())
			ON CONFLICT(node_id,runtime_kind,canonical_path) DO UPDATE SET name=excluded.name,directory_hash=excluded.directory_hash,
			managed=excluded.managed,missing=false,drift=excluded.drift,scan_error=excluded.scan_error,last_seen_at=now(),updated_at=now()`, uuid.NewString(), nodeID,
			skill.SourcePath, skill.Name, skill.SHA256, skill.Managed, skill.State == "blocked" || skill.State == "failed", truncateSharedError(skill.LastError))
		if err != nil {
			return fmt.Errorf("upsert shared Skill %q for source %s: %w", skill.Name, sourceID, err)
		}
	}
	return nil
}

func upsertSharedMCPServersTx(ctx context.Context, tx pgx.Tx, nodeID, sourceID string, source domain.SharedSourceInventory) error {
	if _, err := tx.Exec(ctx, `UPDATE mcp_servers SET enabled=false,updated_at=now() WHERE shared_source_id=$1 AND authority='shared-file'`, sourceID); err != nil {
		return err
	}
	for _, server := range source.MCPServers {
		descriptor, err := protocol.NormalizeMCPDescriptor(domain.RuntimeClaude, server.Descriptor)
		if err != nil {
			return fmt.Errorf("normalize shared MCP server %q: %w", server.Descriptor.Name, err)
		}
		args := jsonStringArray(descriptor.Args)
		origin, _ := json.Marshal(map[string]any{"nodeId": nodeID, "sharedSourceId": sourceID, "sharedSourceName": source.Name})
		_, err = tx.Exec(ctx, `INSERT INTO mcp_servers(id,name,runtime_name,transport,command,args,url,env_refs,enabled,source,origin,config_fingerprint,authority,shared_source_id,header_refs,credential_mode)
			VALUES($1,$2,$2,$3,$4,$5,$6,'{}',$7,'shared-file',$8,$9,'shared-file',$10,'{}','node-local')
			ON CONFLICT(shared_source_id,runtime_name) WHERE authority='shared-file' DO UPDATE SET name=excluded.name,transport=excluded.transport,
			command=excluded.command,args=excluded.args,url=excluded.url,enabled=excluded.enabled,origin=excluded.origin,
			config_fingerprint=excluded.config_fingerprint,credential_mode='node-local',updated_at=now()`, uuid.NewString(), descriptor.Name,
			descriptor.Transport, descriptor.Command, args, descriptor.URL, server.Enabled, string(origin), descriptor.ConfigFingerprint, sourceID)
		if err != nil {
			return err
		}
	}
	return nil
}

func upsertSharedConsumerTx(ctx context.Context, tx pgx.Tx, nodeID, sourceID string, consumer domain.SharedConsumerInventory, servers []domain.SharedMCPServerInventory) error {
	if !domain.IsConsumerRuntime(consumer.Kind) {
		return errors.New("shared inventory contains an invalid consumer runtime")
	}
	var consumerID string
	err := tx.QueryRow(ctx, `INSERT INTO shared_consumers(id,source_id,consumer_kind,skills_path,mcp_path,mcp_format,inherits_from,skills_enabled,mcp_enabled,expected_fingerprint,actual_fingerprint,state,last_error)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT(source_id,consumer_kind) DO UPDATE SET skills_path=excluded.skills_path,mcp_path=excluded.mcp_path,mcp_format=excluded.mcp_format,
		inherits_from=excluded.inherits_from,skills_enabled=excluded.skills_enabled,mcp_enabled=excluded.mcp_enabled,
		expected_fingerprint=excluded.expected_fingerprint,actual_fingerprint=excluded.actual_fingerprint,state=excluded.state,last_error=excluded.last_error,updated_at=now()
		RETURNING id::text`, uuid.NewString(), sourceID, consumer.Kind, consumer.SkillsPath, consumer.MCPPath, consumer.MCPFormat, consumer.InheritsFrom,
		consumer.SkillsEnabled, consumer.MCPEnabled, consumer.ExpectedFingerprint, consumer.ActualFingerprint, consumer.State, truncateSharedError(consumer.LastError)).Scan(&consumerID)
	if err != nil {
		return err
	}
	for _, link := range consumer.SkillLinks {
		if strings.TrimSpace(link.SkillName) == "" || strings.TrimSpace(link.TargetPath) == "" {
			return errors.New("shared inventory contains an invalid Skill link")
		}
		_, err := tx.Exec(ctx, `INSERT INTO shared_skill_links(id,source_id,consumer_id,skill_name,source_path,resolved_source_path,target_path,expected_target,actual_target,managed,state,last_error,last_seen_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now())
			ON CONFLICT(source_id,consumer_id,skill_name) DO UPDATE SET source_path=excluded.source_path,resolved_source_path=excluded.resolved_source_path,
			target_path=excluded.target_path,expected_target=excluded.expected_target,actual_target=excluded.actual_target,managed=excluded.managed,
			state=excluded.state,last_error=excluded.last_error,last_seen_at=now(),updated_at=now()`, uuid.NewString(), sourceID, consumerID,
			link.SkillName, link.SourcePath, link.ResolvedSourcePath, link.TargetPath, link.ExpectedTarget, link.ActualTarget, link.Managed, link.State, truncateSharedError(link.LastError))
		if err != nil {
			return err
		}
	}
	serverDescriptors := make(map[string]domain.MCPDescriptor, len(servers))
	for _, server := range servers {
		serverDescriptors[server.Descriptor.Name] = server.Descriptor
	}
	for _, binding := range consumer.MCPBindings {
		descriptor := serverDescriptors[binding.ServerName]
		conflict, err := upsertSharedMCPBindingTx(ctx, tx, nodeID, sourceID, consumer.Kind, binding, descriptor)
		if err != nil {
			return err
		}
		if conflict {
			_, err = tx.Exec(ctx, `UPDATE shared_consumers SET state='conflict',last_error=$2,updated_at=now() WHERE id=$1`, consumerID,
				truncateSharedError("MCP server name conflicts with an existing ToolHub-authoritative runtime binding"))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func upsertSharedMCPBindingTx(ctx context.Context, tx pgx.Tx, nodeID, sourceID, runtimeKind string, binding domain.SharedMCPBindingInventory, descriptor domain.MCPDescriptor) (bool, error) {
	if strings.TrimSpace(binding.ServerName) == "" {
		return false, errors.New("shared inventory contains an invalid MCP binding")
	}
	var serverID *string
	var foundID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM mcp_servers WHERE shared_source_id=$1 AND runtime_name=$2 AND authority='shared-file'`, sourceID, binding.ServerName).Scan(&foundID); err == nil {
		serverID = &foundID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	envKeys := jsonStringArray(binding.EnvKeys)
	headerKeys := jsonStringArray(binding.HeaderKeys)
	identity := protocol.MCPIdentity(runtimeKind, binding.ServerName)
	command, err := tx.Exec(ctx, `INSERT INTO mcp_runtime_bindings(id,node_id,runtime_kind,server_name,identity,server_id,env_keys,observed_config_fingerprint,observed_secret_fingerprint,
		desired_config_fingerprint,desired_secret_fingerprint,desired_enabled,missing,drift,last_seen_at,shared_source_id,header_keys,desired_fingerprint,actual_fingerprint)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8,$9,$10,$11,$12,now(),$13,$14,$15,$16)
		ON CONFLICT(node_id,runtime_kind,server_name) DO UPDATE SET identity=excluded.identity,server_id=excluded.server_id,env_keys=excluded.env_keys,
		observed_config_fingerprint=excluded.observed_config_fingerprint,observed_secret_fingerprint=excluded.observed_secret_fingerprint,
		desired_config_fingerprint=excluded.desired_config_fingerprint,desired_secret_fingerprint=excluded.desired_secret_fingerprint,
		desired_enabled=excluded.desired_enabled,missing=excluded.missing,drift=excluded.drift,last_seen_at=now(),shared_source_id=excluded.shared_source_id,
		header_keys=excluded.header_keys,desired_fingerprint=excluded.desired_fingerprint,actual_fingerprint=excluded.actual_fingerprint,updated_at=now()
		WHERE mcp_runtime_bindings.shared_source_id=excluded.shared_source_id`, uuid.NewString(), nodeID, runtimeKind, binding.ServerName, identity, serverID,
		envKeys, descriptor.ConfigFingerprint, descriptor.SecretFingerprint, binding.Enabled, binding.Missing, binding.Drift, sourceID, headerKeys,
		binding.DesiredFingerprint, binding.ActualFingerprint)
	if err != nil {
		return false, err
	}
	return command.RowsAffected() == 0, nil
}

func (s *Store) ListSharedSources(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, sharedSourceProjectionQuery()+` ORDER BY n.name,ss.name`)
}

func (s *Store) GetSharedSource(ctx context.Context, id string) (json.RawMessage, error) {
	return s.JSONObject(ctx, sharedSourceProjectionQuery()+` WHERE ss.id=$1`, id)
}

func sharedSourceProjectionQuery() string {
	return `SELECT ss.id::text AS id,ss.node_id::text AS "nodeId",n.name AS "nodeName",ss.name,ss.mode,ss.auto_sync AS "autoSync",
		ss.skills_root AS "skillsRoot",ss.mcp_manifest_path AS "mcpManifestPath",ss.config_fingerprint AS "configFingerprint",
		ss.source_fingerprint AS "sourceFingerprint",ss.status,ss.last_scan_at AS "lastScanAt",ss.last_sync_at AS "lastSyncAt",ss.last_error AS "lastError",
		coalesce((SELECT jsonb_agg(jsonb_build_object('kind',c.consumer_kind,'skillsPath',c.skills_path,'mcpPath',c.mcp_path,
			'mcpFormat',c.mcp_format,'inheritsFrom',c.inherits_from,'skillsEnabled',c.skills_enabled,'mcpEnabled',c.mcp_enabled,
			'expectedFingerprint',c.expected_fingerprint,'actualFingerprint',c.actual_fingerprint,'state',c.state,'lastError',c.last_error,
			'skillLinks',coalesce((SELECT jsonb_agg(jsonb_build_object('skillName',l.skill_name,'sourcePath',l.source_path,
				'resolvedSourcePath',l.resolved_source_path,'targetPath',l.target_path,'expectedTarget',l.expected_target,
				'actualTarget',l.actual_target,'managed',l.managed,'state',l.state,'lastError',l.last_error) ORDER BY l.skill_name)
				FROM shared_skill_links l WHERE l.consumer_id=c.id),'[]'::jsonb),
			'mcpBindings',coalesce((SELECT jsonb_agg(jsonb_build_object('serverName',b.server_name,'desiredFingerprint',b.desired_fingerprint,
				'actualFingerprint',b.actual_fingerprint,'envKeys',b.env_keys,'headerKeys',b.header_keys,'enabled',b.desired_enabled,
				'missing',b.missing,'drift',b.drift) ORDER BY b.server_name) FROM mcp_runtime_bindings b
				WHERE b.shared_source_id=ss.id AND b.runtime_kind=c.consumer_kind),'[]'::jsonb)) ORDER BY c.consumer_kind)
			FROM shared_consumers c WHERE c.source_id=ss.id),'[]'::jsonb) AS consumers,
		coalesce((SELECT jsonb_agg(jsonb_build_object('id',ms.id::text,'name',ms.runtime_name,'transport',ms.transport,'command',ms.command,
			'args',ms.args,'url',ms.url,'enabled',ms.enabled,'authority',ms.authority,'credentialMode',ms.credential_mode,
			'envKeys',coalesce((SELECT jsonb_agg(DISTINCT value) FROM mcp_runtime_bindings b
				CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(b.env_keys)='array' THEN b.env_keys ELSE '[]'::jsonb END)
				WHERE b.shared_source_id=ss.id AND b.server_name=ms.runtime_name),'[]'::jsonb),
			'headerKeys',coalesce((SELECT jsonb_agg(DISTINCT value) FROM mcp_runtime_bindings b
				CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(b.header_keys)='array' THEN b.header_keys ELSE '[]'::jsonb END)
				WHERE b.shared_source_id=ss.id AND b.server_name=ms.runtime_name),'[]'::jsonb)) ORDER BY ms.runtime_name)
			FROM mcp_servers ms WHERE ms.shared_source_id=ss.id AND ms.authority='shared-file'),'[]'::jsonb) AS "mcpServers",
		coalesce((SELECT jsonb_agg(jsonb_build_object('name',sd.name,'path',sd.canonical_path,'managed',sd.managed,'error',sd.scan_error) ORDER BY sd.name)
			FROM skill_discoveries sd WHERE sd.node_id=ss.node_id AND sd.runtime_kind='shared' AND NOT sd.missing
			AND (sd.canonical_path=ss.skills_root OR sd.canonical_path LIKE ss.skills_root || '/%')),'[]'::jsonb) AS skills
	FROM shared_sources ss JOIN nodes n ON n.id=ss.node_id`
}

func truncateSharedError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 2000 {
		return value[:2000]
	}
	return value
}

// jsonStringArray encodes values as a jsonb array literal. A nil slice must not reach
// PostgreSQL as `null`: the shared-source projection expands these columns with
// jsonb_array_elements_text, which rejects scalars.
func jsonStringArray(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}
