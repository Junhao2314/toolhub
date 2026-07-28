package store

import (
	"context"
	"encoding/json"
)

func (s *Store) ListUsers(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT u.id::text AS id,u.username,u.email,u.display_name AS "displayName",u.disabled,u.password_change_recommended AS "passwordChangeRecommended",u.created_at AS "createdAt",
		coalesce(array_agg(r.name ORDER BY r.name) FILTER (WHERE r.name IS NOT NULL),ARRAY[]::text[]) AS roles
		FROM users u LEFT JOIN user_roles ur ON ur.user_id=u.id LEFT JOIN roles r ON r.id=ur.role_id GROUP BY u.id ORDER BY u.created_at`)
}

func (s *Store) ListNodes(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT n.id::text AS id,n.name,n.hostname,n.platform,n.architecture,host(n.tailscale_ip) AS "tailscaleIp",n.status,n.labels,
		n.connection_preference AS "connectionPreference",n.last_seen_at AS "lastSeenAt",n.created_at AS "createdAt",
		(n.labels->>'scope'='local') AS "isLocal",
		EXISTS(SELECT 1 FROM node_connections c WHERE c.node_id=n.id AND c.kind='ssh' AND c.enabled) AS "hasSsh",
		(SELECT count(*) FROM runtimes r WHERE r.node_id=n.id) AS "runtimeCount",
		(SELECT max(r.scanned_at) FROM runtimes r WHERE r.node_id=n.id) AS "scannedAt",
		(SELECT count(*) FROM mcp_runtime_bindings mb WHERE mb.node_id=n.id AND mb.desired_enabled) AS "autoManagedMcpCount",
		(SELECT count(*) FROM shared_sources ss WHERE ss.node_id=n.id AND ss.status<>'missing') AS "sharedSourceCount",
		(SELECT count(*) FROM skill_discoveries sd WHERE sd.node_id=n.id AND sd.adopted_skill_id IS NULL AND NOT sd.missing AND NOT sd.protected) AS "pendingSkillCount",
		(SELECT count(*) FROM skill_discoveries sd WHERE sd.node_id=n.id AND (sd.drift OR sd.missing)) +
		(SELECT count(*) FROM mcp_runtime_bindings mb WHERE mb.node_id=n.id AND (mb.drift OR mb.missing)) AS "discoveryAttentionCount",
		coalesce((SELECT array_agg(DISTINCT r.kind ORDER BY r.kind) FROM runtimes r WHERE r.node_id=n.id),ARRAY[]::text[]) AS "runtimeKinds",
		coalesce((SELECT jsonb_agg(jsonb_build_object('runtime',a.runtime_kind,'profileId',a.profile_id::text,'profileName',p.name,'state',a.state)
			ORDER BY a.runtime_kind) FROM toolhub_profile_activations a JOIN toolhub_profiles p ON p.id=a.profile_id WHERE a.node_id=n.id),'[]'::jsonb) AS activations
		FROM nodes n WHERE n.archived_at IS NULL ORDER BY (n.labels->>'scope'='local') DESC,n.name`)
}

func (s *Store) GetNode(ctx context.Context, id string) (json.RawMessage, error) {
	return s.JSONObject(ctx, `SELECT n.id::text AS id,n.name,n.hostname,n.platform,n.architecture,host(n.tailscale_ip) AS "tailscaleIp",n.status,n.labels,
		n.connection_preference AS "connectionPreference",n.last_seen_at AS "lastSeenAt",n.created_at AS "createdAt",
		(n.labels->>'scope'='local') AS "isLocal",
		EXISTS(SELECT 1 FROM node_connections c WHERE c.node_id=n.id AND c.kind='ssh' AND c.enabled) AS "hasSsh",
		coalesce((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT r.id::text AS id,r.kind,r.root_path AS "rootPath",r.version,r.inventory,r.scanned_at AS "scannedAt" FROM runtimes r WHERE r.node_id=n.id ORDER BY r.kind) x),'[]') AS runtimes,
		coalesce((SELECT jsonb_agg(jsonb_build_object('id',ss.id::text,'name',ss.name,'mode',ss.mode,'autoSync',ss.auto_sync,
			'skillsRoot',ss.skills_root,'mcpManifestPath',ss.mcp_manifest_path,'status',ss.status,'lastScanAt',ss.last_scan_at,
			'lastSyncAt',ss.last_sync_at,'lastError',ss.last_error) ORDER BY ss.name) FROM shared_sources ss WHERE ss.node_id=n.id),'[]'::jsonb) AS "sharedSources"
		FROM nodes n WHERE n.id=$1 AND n.archived_at IS NULL`, id)
}

func (s *Store) ListSkills(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT s.id::text AS id,s.slug,s.name,s.description,s.review_status AS "reviewStatus",s.protected,s.created_at AS "createdAt",
		v.id::text AS "versionId",v.source_commit AS "sourceCommit",v.content_sha256 AS sha256,v.risk_level AS "riskLevel",v.manifest,
		ss.kind AS "sourceKind",ss.url AS "sourceUrl",(SELECT count(*) FROM deployments d WHERE d.skill_id=s.id) AS "deploymentCount"
		FROM skills s LEFT JOIN LATERAL (SELECT candidate.* FROM skill_versions candidate WHERE candidate.skill_id=s.id ORDER BY (candidate.id=s.current_version_id) DESC,candidate.created_at DESC LIMIT 1) v ON true LEFT JOIN skill_sources ss ON ss.id=s.source_id
		WHERE s.archived_at IS NULL ORDER BY s.name`)
}

func (s *Store) GetSkill(ctx context.Context, id string) (json.RawMessage, error) {
	return s.JSONObject(ctx, `SELECT s.id::text AS id,s.slug,s.name,s.description,s.review_status AS "reviewStatus",s.protected,
		ss.kind AS "sourceKind",ss.url AS "sourceUrl",ss.subdirectory,
		coalesce((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT v.id::text AS id,v.source_commit AS "sourceCommit",v.content_sha256 AS sha256,v.provenance,v.manifest,v.risk_level AS "riskLevel",v.approved_at AS "approvedAt",a.scan_report AS "scanReport",v.created_at AS "createdAt" FROM skill_versions v JOIN skill_artifacts a ON a.id=v.artifact_id WHERE v.skill_id=s.id ORDER BY v.created_at DESC) x),'[]') AS versions
		FROM skills s LEFT JOIN skill_sources ss ON ss.id=s.source_id WHERE s.id=$1 AND s.archived_at IS NULL`, id)
}

func (s *Store) ListSources(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT id::text AS id,kind,name,url,subdirectory,default_branch AS "defaultBranch",update_policy AS "updatePolicy",created_at AS "createdAt" FROM skill_sources ORDER BY created_at DESC`)
}

func (s *Store) ListDeployments(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT d.id::text AS id,d.node_id::text AS "nodeId",n.name AS "nodeName",d.runtime_kind AS runtime,d.skill_id::text AS "skillId",s.name AS "skillName",
		d.desired_version_id::text AS "desiredVersionId",d.actual_version_id::text AS "actualVersionId",d.previous_version_id::text AS "previousVersionId",
		d.desired_enabled AS "desiredEnabled",d.actual_enabled AS "actualEnabled",d.state,d.last_error AS "lastError",d.reconciled_at AS "reconciledAt"
		FROM deployments d JOIN nodes n ON n.id=d.node_id JOIN skills s ON s.id=d.skill_id ORDER BY s.name,n.name,d.runtime_kind`)
}

func (s *Store) ListUpdates(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT u.id::text AS id,u.skill_id::text AS "skillId",s.name AS "skillName",u.candidate_commit AS "candidateCommit",u.candidate_sha256 AS "candidateSha256",u.diff,u.risk_change AS "riskChange",u.license_change AS "licenseChange",u.status,u.created_at AS "createdAt" FROM updates u JOIN skills s ON s.id=u.skill_id ORDER BY u.created_at DESC`)
}

func (s *Store) ListJobs(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT id::text AS id,kind,status,payload,result,dry_run AS "dryRun",attempts,max_attempts AS "maxAttempts",run_after AS "runAfter",started_at AS "startedAt",finished_at AS "finishedAt",created_at AS "createdAt" FROM jobs ORDER BY created_at DESC LIMIT 500`)
}

func (s *Store) ListAudit(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT a.id::text AS id,a.action,a.resource_type AS "resourceType",a.resource_id AS "resourceId",a.outcome,a.metadata,a.created_at AS "createdAt",u.email AS actor FROM audit_events a LEFT JOIN users u ON u.id=a.actor_user_id ORDER BY a.created_at DESC LIMIT 1000`)
}

func (s *Store) ListMCPServers(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT id::text AS id,name,runtime_name AS "runtimeName",transport,command,args,url,env_refs AS "envRefs",header_refs AS "headerRefs",enabled,source,origin,
		authority,coalesce(shared_source_id::text,'') AS "sharedSourceId",credential_mode AS "credentialMode",health_status AS "healthStatus",usage,update_policy AS "updatePolicy",archived_at AS "archivedAt",created_at AS "createdAt",
		(SELECT count(*) FROM mcp_runtime_bindings b WHERE b.server_id=mcp_servers.id) AS "bindingCount",
		EXISTS(SELECT 1 FROM mcp_runtime_bindings b WHERE b.server_id=mcp_servers.id AND (b.drift OR b.missing)) AS "hasDrift"
		FROM mcp_servers ORDER BY name`)
}

func (s *Store) ListMCPProfiles(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT p.id::text AS id,p.name,p.description,p.enabled,p.source,p.origin,p.created_at AS "createdAt",coalesce(array_agg(ps.server_id::text ORDER BY ps.server_id::text) FILTER (WHERE ps.server_id IS NOT NULL),ARRAY[]::text[]) AS "serverIds" FROM mcp_profiles p LEFT JOIN mcp_profile_servers ps ON ps.profile_id=p.id GROUP BY p.id ORDER BY p.name`)
}

func (s *Store) ListMCPDeployments(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT d.id::text AS id,d.profile_id::text AS "profileId",p.name AS "profileName",p.source,d.node_id::text AS "nodeId",n.name AS "nodeName",d.runtime_kind AS runtime,d.desired_enabled AS "desiredEnabled",d.actual_hash AS "actualHash",d.desired_hash AS "desiredHash",d.state,d.last_error AS "lastError",d.updated_at AS "updatedAt",
		coalesce((SELECT jsonb_agg(jsonb_build_object('id',b.id::text,'serverName',b.server_name,'missing',b.missing,'drift',b.drift) ORDER BY b.server_name) FROM mcp_runtime_bindings b WHERE b.deployment_id=d.id),'[]'::jsonb) AS bindings
		FROM mcp_deployments d JOIN mcp_profiles p ON p.id=d.profile_id JOIN nodes n ON n.id=d.node_id ORDER BY p.name,n.name`)
}

func (s *Store) ListAIProviders(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT id::text AS id,name,base_url AS "baseUrl",model,is_default AS "isDefault",enabled,created_at AS "createdAt" FROM ai_providers ORDER BY name`)
}
