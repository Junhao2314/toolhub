package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/skills"
)

type SourceChangedError struct {
	ExpectedSHA256     string
	ObservedSHA256     string
	ExpectedGeneration int64
	ObservedGeneration int64
}

func (e *SourceChangedError) Error() string { return ErrSourceChanged.Error() }
func (e *SourceChangedError) Unwrap() error { return ErrSourceChanged }

type SecretConfirmationRequiredError struct {
	EnvKeys    []string
	HeaderKeys []string
	Targets    []MCPAffectedTarget
}

func (e *SecretConfirmationRequiredError) Error() string { return ErrSecretConfirmation.Error() }
func (e *SecretConfirmationRequiredError) Unwrap() error { return ErrSecretConfirmation }

type ImportInProgressError struct {
	Status string
}

func (e *ImportInProgressError) Error() string { return ErrImportInProgress.Error() }
func (e *ImportInProgressError) Unwrap() error { return ErrImportInProgress }

type MCPAffectedTarget struct {
	NodeID   string `json:"nodeId"`
	NodeName string `json:"nodeName"`
	Runtime  string `json:"runtime"`
}

type hermesMCPImportCandidate struct {
	BindingID          string
	NodeID             string
	ObservedGeneration int64
	Descriptor         domain.MCPDescriptor
}

func (s *Store) QueueHermesSkillImport(ctx context.Context, discoveryID, expectedSHA256, actor string) (domain.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var nodeID, runtimeKind, controlMode, observedSHA256, status string
	var missing, protected, capable bool
	err = tx.QueryRow(ctx, `SELECT discovery.node_id::text,discovery.runtime_kind,discovery.control_mode,
		discovery.directory_hash,discovery.import_status,discovery.missing,discovery.protected,
		coalesce(node.agent_capabilities ? $2,false)
		FROM skill_discoveries discovery JOIN nodes node ON node.id=discovery.node_id
		WHERE discovery.id=$1 FOR UPDATE OF discovery`, discoveryID, domain.CapabilityHermesReadOnlyImportV1).
		Scan(&nodeID, &runtimeKind, &controlMode, &observedSHA256, &status, &missing, &protected, &capable)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Job{}, ErrNotFound
	}
	if err != nil {
		return domain.Job{}, err
	}
	if runtimeKind != domain.RuntimeHermes || controlMode != "read_only_source" {
		return domain.Job{}, ErrHermesReadOnly
	}
	if status == "not_applicable" {
		return domain.Job{}, ErrStateConflict
	}
	if !capable {
		return domain.Job{}, ErrAgentUpgradeRequired
	}
	if missing || protected || observedSHA256 == "" {
		return domain.Job{}, ErrStateConflict
	}
	if expectedSHA256 == "" || expectedSHA256 != observedSHA256 {
		return domain.Job{}, &SourceChangedError{ExpectedSHA256: expectedSHA256, ObservedSHA256: observedSHA256}
	}
	if status == "queued" || status == "importing" {
		return domain.Job{}, &ImportInProgressError{Status: status}
	}
	job, err := s.enqueueJobTxWithOptions(ctx, tx, "skill_snapshot_import", map[string]any{
		"discoveryId": discoveryID, "expectedSha256": expectedSHA256,
	}, false, actor, JobOptions{MaxAttempts: 1, DeduplicateActive: true})
	if err != nil {
		return domain.Job{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE skill_discoveries SET import_status='queued',import_error='',
		import_job_id=$2,source_changed=false,updated_at=now() WHERE id=$1`, discoveryID, job.ID); err != nil {
		return domain.Job{}, err
	}
	return job, tx.Commit(ctx)
}

func (s *Store) HermesSkillSnapshotForImport(ctx context.Context, discoveryID, expectedSHA256, jobID string) (SkillAdoptionTarget, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SkillAdoptionTarget{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var target SkillAdoptionTarget
	var controlMode, status, importJobID string
	var missing, protected, capable bool
	err = tx.QueryRow(ctx, `SELECT discovery.id::text,discovery.node_id::text,discovery.runtime_kind,
		discovery.canonical_path,discovery.directory_hash,discovery.control_mode,discovery.import_status,
		coalesce(discovery.import_job_id::text,''),discovery.missing,discovery.protected,
		coalesce(node.agent_capabilities ? $2,false)
		FROM skill_discoveries discovery JOIN nodes node ON node.id=discovery.node_id
		WHERE discovery.id=$1 FOR UPDATE OF discovery`, discoveryID, domain.CapabilityHermesReadOnlyImportV1).
		Scan(&target.DiscoveryID, &target.NodeID, &target.Runtime, &target.Path, &target.SHA256,
			&controlMode, &status, &importJobID, &missing, &protected, &capable)
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillAdoptionTarget{}, ErrNotFound
	}
	if err != nil {
		return SkillAdoptionTarget{}, err
	}
	if target.Runtime != domain.RuntimeHermes || controlMode != "read_only_source" {
		return SkillAdoptionTarget{}, ErrHermesReadOnly
	}
	if !capable {
		return SkillAdoptionTarget{}, ErrAgentUpgradeRequired
	}
	if missing || protected || target.SHA256 == "" {
		return SkillAdoptionTarget{}, ErrStateConflict
	}
	if target.SHA256 != expectedSHA256 {
		return SkillAdoptionTarget{}, &SourceChangedError{ExpectedSHA256: expectedSHA256, ObservedSHA256: target.SHA256}
	}
	if importJobID != jobID || (status != "queued" && status != "importing") {
		return SkillAdoptionTarget{}, ErrStateConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE skill_discoveries SET import_status='importing',import_error='',updated_at=now()
		WHERE id=$1`, discoveryID); err != nil {
		return SkillAdoptionTarget{}, err
	}
	return target, tx.Commit(ctx)
}

func (s *Store) FailHermesSkillImport(ctx context.Context, discoveryID, jobID string, cause error) {
	message := truncateImportError(cause)
	_, _ = s.pool.Exec(ctx, `UPDATE skill_discoveries SET import_status='failed',import_error=$3,updated_at=now()
		WHERE id=$1 AND import_job_id=$2 AND import_status IN ('queued','importing')`, discoveryID, jobID, message)
}

func (s *Store) QueueHermesMCPImport(ctx context.Context, bindingID string, observedGeneration int64, confirmSecrets bool, actor string) (domain.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var nodeID, runtimeKind, controlMode, status string
	var currentGeneration int64
	var envJSON, headerJSON []byte
	var missing, capable bool
	err = tx.QueryRow(ctx, `SELECT binding.node_id::text,binding.runtime_kind,binding.control_mode,
		binding.observed_generation,binding.import_status,binding.env_keys,binding.header_keys,binding.missing,
		coalesce(node.agent_capabilities ? $2,false)
		FROM mcp_runtime_bindings binding JOIN nodes node ON node.id=binding.node_id
		WHERE binding.id=$1 FOR UPDATE OF binding`, bindingID, domain.CapabilityHermesReadOnlyImportV1).
		Scan(&nodeID, &runtimeKind, &controlMode, &currentGeneration, &status, &envJSON, &headerJSON, &missing, &capable)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Job{}, ErrNotFound
	}
	if err != nil {
		return domain.Job{}, err
	}
	if runtimeKind != domain.RuntimeHermes || controlMode != "read_only_source" {
		return domain.Job{}, ErrHermesReadOnly
	}
	if status == "not_applicable" {
		return domain.Job{}, ErrStateConflict
	}
	if !capable {
		return domain.Job{}, ErrAgentUpgradeRequired
	}
	if missing {
		return domain.Job{}, ErrStateConflict
	}
	if observedGeneration <= 0 || observedGeneration != currentGeneration {
		return domain.Job{}, &SourceChangedError{ExpectedGeneration: observedGeneration, ObservedGeneration: currentGeneration}
	}
	if status == "queued" || status == "importing" {
		return domain.Job{}, &ImportInProgressError{Status: status}
	}
	var envKeys, headerKeys []string
	if json.Unmarshal(envJSON, &envKeys) != nil || json.Unmarshal(headerJSON, &headerKeys) != nil {
		return domain.Job{}, errors.New("Hermes MCP candidate key names are invalid")
	}
	if !confirmSecrets && (len(envKeys) > 0 || len(headerKeys) > 0) {
		return domain.Job{}, &SecretConfirmationRequiredError{EnvKeys: envKeys, HeaderKeys: headerKeys}
	}
	job, err := s.enqueueJobTxWithOptions(ctx, tx, "inventory_scan", map[string]any{
		"nodeId":          nodeID,
		"hermesMcpImport": map[string]any{"discoveryId": bindingID, "observedGeneration": observedGeneration},
	}, false, actor, JobOptions{MaxAttempts: 1, DeduplicateActive: true})
	if err != nil {
		return domain.Job{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET import_status='queued',import_error='',
		import_job_id=$2,pinned_generation=$3,updated_at=now() WHERE id=$1`, bindingID, job.ID, observedGeneration); err != nil {
		return domain.Job{}, err
	}
	return job, tx.Commit(ctx)
}

func (s *Store) HermesMCPImportForScan(ctx context.Context, bindingID string, observedGeneration int64, jobID string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var nodeID, runtimeKind, controlMode, status, importJobID string
	var currentGeneration int64
	var pinnedGeneration *int64
	var capable bool
	err = tx.QueryRow(ctx, `SELECT binding.node_id::text,binding.runtime_kind,binding.control_mode,
		binding.observed_generation,binding.pinned_generation,binding.import_status,
		coalesce(binding.import_job_id::text,''),coalesce(node.agent_capabilities ? $2,false)
		FROM mcp_runtime_bindings binding JOIN nodes node ON node.id=binding.node_id
		WHERE binding.id=$1 FOR UPDATE OF binding`, bindingID, domain.CapabilityHermesReadOnlyImportV1).
		Scan(&nodeID, &runtimeKind, &controlMode, &currentGeneration, &pinnedGeneration, &status, &importJobID, &capable)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if runtimeKind != domain.RuntimeHermes || controlMode != "read_only_source" {
		return "", ErrHermesReadOnly
	}
	if !capable {
		return "", ErrAgentUpgradeRequired
	}
	if currentGeneration != observedGeneration || pinnedGeneration == nil || *pinnedGeneration != observedGeneration {
		return "", &SourceChangedError{ExpectedGeneration: observedGeneration, ObservedGeneration: currentGeneration}
	}
	if importJobID != jobID || (status != "queued" && status != "importing") {
		return "", ErrStateConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET import_status='importing',import_error='',updated_at=now()
		WHERE id=$1`, bindingID); err != nil {
		return "", err
	}
	return nodeID, tx.Commit(ctx)
}

func (s *Store) FailHermesMCPImport(ctx context.Context, bindingID, jobID string, cause error) {
	message := truncateImportError(cause)
	_, _ = s.pool.Exec(ctx, `UPDATE mcp_runtime_bindings SET import_status='failed',import_error=$3,
		pinned_generation=NULL,updated_at=now()
		WHERE id=$1 AND ($2='' OR import_job_id=$2) AND import_status IN ('queued','importing')`, bindingID, jobID, message)
}

func normalizeAgentCapabilities(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && len(value) <= 100 {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func inventoryAgentCapabilities(inventory domain.AgentInventory) []string {
	values := append([]string(nil), inventory.Capabilities...)
	for _, runtime := range inventory.Runtimes {
		if runtime.Kind != domain.RuntimeHermes {
			continue
		}
		switch configured := runtime.Config["capabilities"].(type) {
		case []string:
			values = append(values, configured...)
		case []any:
			for _, value := range configured {
				if capability, ok := value.(string); ok {
					values = append(values, capability)
				}
			}
		}
	}
	return normalizeAgentCapabilities(values)
}

func observeHermesMCPDescriptorTx(ctx context.Context, tx pgx.Tx, nodeID string, descriptor domain.MCPDescriptor) (hermesMCPImportCandidate, error) {
	body, _ := json.Marshal(descriptor)
	var bindingID, observedConfig, observedSecret, status, lastConfig, lastSecret string
	var generation int64
	var pinnedGeneration *int64
	err := tx.QueryRow(ctx, `SELECT id::text,observed_config_fingerprint,observed_secret_fingerprint,
		observed_generation,pinned_generation,import_status,last_imported_config_fingerprint,last_imported_secret_fingerprint
		FROM mcp_runtime_bindings WHERE node_id=$1 AND runtime_kind='hermes' AND server_name=$2 FOR UPDATE`, nodeID, descriptor.Name).
		Scan(&bindingID, &observedConfig, &observedSecret, &generation, &pinnedGeneration, &status, &lastConfig, &lastSecret)
	if errors.Is(err, pgx.ErrNoRows) {
		bindingID = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO mcp_runtime_bindings(
			id,node_id,runtime_kind,server_name,identity,env_keys,header_keys,observed_config_fingerprint,
			observed_secret_fingerprint,desired_enabled,missing,drift,last_seen_at,control_mode,descriptor,
			observed_generation,import_status)
			VALUES($1,$2,'hermes',$3,$4,$5,$6,$7,$8,false,false,false,now(),'read_only_source',$9,1,'available')`,
			bindingID, nodeID, descriptor.Name, descriptor.Identity, jsonStringArray(descriptor.EnvKeys),
			jsonStringArray(descriptor.HeaderKeys), descriptor.ConfigFingerprint, descriptor.SecretFingerprint, string(body))
		return hermesMCPImportCandidate{}, err
	}
	if err != nil {
		return hermesMCPImportCandidate{}, err
	}
	changed := observedConfig != descriptor.ConfigFingerprint || observedSecret != descriptor.SecretFingerprint
	if changed {
		generation++
	}
	if status == "not_applicable" {
		status = "available"
	}
	requested := (status == "queued" || status == "importing") && pinnedGeneration != nil && *pinnedGeneration == generation
	importError := ""
	if (status == "queued" || status == "importing") && !requested {
		status = "failed"
		importError = "source_changed: Hermes MCP changed after the import was requested"
		pinnedGeneration = nil
	}
	sourceChanged := lastConfig != "" && (lastConfig != descriptor.ConfigFingerprint || lastSecret != descriptor.SecretFingerprint)
	_, err = tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET identity=$2,env_keys=$3,header_keys=$4,
		observed_config_fingerprint=$5,observed_secret_fingerprint=$6,observed_generation=$7,
		pinned_generation=$8,descriptor=$9,source_changed=$10,import_status=$11,import_error=$12,
		desired_enabled=false,missing=false,drift=false,last_seen_at=now(),updated_at=now()
		WHERE id=$1`, bindingID, descriptor.Identity, jsonStringArray(descriptor.EnvKeys), jsonStringArray(descriptor.HeaderKeys),
		descriptor.ConfigFingerprint, descriptor.SecretFingerprint, generation, pinnedGeneration, string(body), sourceChanged, status, importError)
	if err != nil || !requested {
		return hermesMCPImportCandidate{}, err
	}
	return hermesMCPImportCandidate{BindingID: bindingID, NodeID: nodeID, ObservedGeneration: generation, Descriptor: descriptor}, nil
}

func (s *Store) importHermesMCPDescriptor(ctx context.Context, bindingID string, generation int64, environment, headers map[string]string) (MCPAdoptionResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := s.importHermesMCPDescriptorTx(ctx, tx, bindingID, generation, environment, headers)
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) importHermesMCPDescriptorTx(ctx context.Context, tx pgx.Tx, bindingID string, generation int64, environment, headers map[string]string) (MCPAdoptionResult, error) {
	var nodeID, runtimeKind, controlMode, status, observedConfig, observedSecret, lastServerID string
	var descriptorJSON, envJSON, headerJSON []byte
	var currentGeneration int64
	var pinnedGeneration, lastImportedGeneration *int64
	err := tx.QueryRow(ctx, `SELECT node_id::text,runtime_kind,control_mode,descriptor,env_keys,header_keys,
		observed_config_fingerprint,observed_secret_fingerprint,observed_generation,pinned_generation,
		import_status,last_imported_generation,coalesce(last_imported_server_id::text,'')
		FROM mcp_runtime_bindings WHERE id=$1 FOR UPDATE`, bindingID).
		Scan(&nodeID, &runtimeKind, &controlMode, &descriptorJSON, &envJSON, &headerJSON, &observedConfig,
			&observedSecret, &currentGeneration, &pinnedGeneration, &status, &lastImportedGeneration, &lastServerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPAdoptionResult{}, ErrNotFound
	}
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	if runtimeKind != domain.RuntimeHermes || controlMode != "read_only_source" {
		return MCPAdoptionResult{}, ErrHermesReadOnly
	}
	if status == "imported" && lastImportedGeneration != nil && *lastImportedGeneration == generation && lastServerID != "" {
		return MCPAdoptionResult{BindingID: bindingID, ServerID: lastServerID, Reused: true}, nil
	}
	if status != "importing" || pinnedGeneration == nil || *pinnedGeneration != generation || currentGeneration != generation {
		return MCPAdoptionResult{}, &SourceChangedError{ExpectedGeneration: generation, ObservedGeneration: currentGeneration}
	}
	var descriptor domain.MCPDescriptor
	var envKeys, headerKeys []string
	if json.Unmarshal(descriptorJSON, &descriptor) != nil || json.Unmarshal(envJSON, &envKeys) != nil || json.Unmarshal(headerJSON, &headerKeys) != nil {
		return MCPAdoptionResult{}, errors.New("Hermes MCP candidate descriptor is invalid")
	}
	if !sameStringSet(envKeys, mapKeys(environment)) || !sameStringSet(headerKeys, mapKeys(headers)) {
		return MCPAdoptionResult{}, errors.New("captured environment or header names do not match the Hermes candidate")
	}
	serverID := uuid.NewString()
	name, err := uniqueMCPDisplayNameTx(ctx, tx, strings.TrimSpace(descriptor.Name))
	if err != nil {
		return MCPAdoptionResult{}, err
	}
	envRefs := map[string]string{}
	for _, key := range envKeys {
		secretID, err := s.createSecret(ctx, tx, "mcp:"+serverID+":env:"+key+":"+uuid.NewString(), "mcp-env", []byte(environment[key]),
			map[string]any{"mcpServerId": serverID, "envName": key, "source": "hermes-import"}, "")
		if err != nil {
			return MCPAdoptionResult{}, err
		}
		envRefs[key] = secretID
	}
	headerRefs := map[string]string{}
	for _, key := range headerKeys {
		secretID, err := s.createSecret(ctx, tx, "mcp:"+serverID+":header:"+key+":"+uuid.NewString(), "mcp-header", []byte(headers[key]),
			map[string]any{"mcpServerId": serverID, "headerName": key, "source": "hermes-import"}, "")
		if err != nil {
			return MCPAdoptionResult{}, err
		}
		headerRefs[key] = secretID
	}
	argsJSON, _ := json.Marshal(descriptor.Args)
	envRefsJSON, _ := json.Marshal(envRefs)
	headerRefsJSON, _ := json.Marshal(headerRefs)
	origin, _ := json.Marshal(map[string]any{
		"nodeId": nodeID, "importSource": "hermes", "serverName": descriptor.Name,
		"discoveryBindingId": bindingID, "observedGeneration": generation,
	})
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_servers(
		id,name,runtime_name,transport,command,args,url,env_refs,header_refs,enabled,source,origin,config_fingerprint)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,true,'hermes-import',$10,$11)`, serverID, name, descriptor.Name,
		descriptor.Transport, descriptor.Command, string(argsJSON), descriptor.URL, string(envRefsJSON), string(headerRefsJSON),
		string(origin), descriptor.ConfigFingerprint); err != nil {
		return MCPAdoptionResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_runtime_bindings SET import_status='imported',import_error='',
		pinned_generation=NULL,source_changed=false,last_imported_config_fingerprint=$2,
		last_imported_secret_fingerprint=$3,last_imported_generation=$4,last_imported_server_id=$5,
		last_imported_at=now(),updated_at=now() WHERE id=$1`, bindingID, observedConfig, observedSecret, generation, serverID); err != nil {
		return MCPAdoptionResult{}, err
	}
	return MCPAdoptionResult{BindingID: bindingID, ServerID: serverID}, nil
}

func (s *Store) ImportHermesSkillSnapshot(ctx context.Context, nodeID, discoveryID, taskID string, pkg skills.Package) (ImportedSkill, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ImportedSkill{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var runtimeKind, controlMode, pathValue, expectedHash, status, importedSkillID, importedVersionID, lastHash string
	err = tx.QueryRow(ctx, `SELECT runtime_kind,control_mode,canonical_path,directory_hash,import_status,
		coalesce(imported_skill_id::text,''),coalesce(imported_version_id::text,''),last_imported_sha256
		FROM skill_discoveries WHERE id=$1 AND node_id=$2 FOR UPDATE`, discoveryID, nodeID).
		Scan(&runtimeKind, &controlMode, &pathValue, &expectedHash, &status, &importedSkillID, &importedVersionID, &lastHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ImportedSkill{}, ErrNotFound
	}
	if err != nil {
		return ImportedSkill{}, err
	}
	if runtimeKind != domain.RuntimeHermes || controlMode != "read_only_source" {
		return ImportedSkill{}, ErrHermesReadOnly
	}
	if pkg.SHA256 != expectedHash {
		return ImportedSkill{}, &SourceChangedError{ExpectedSHA256: expectedHash, ObservedSHA256: pkg.SHA256}
	}
	var taskPayload []byte
	if err := tx.QueryRow(ctx, `SELECT payload FROM node_tasks WHERE id=$1 AND node_id=$2
		AND kind='import_skill_snapshot' AND status IN ('pending','delivered','running')`, taskID, nodeID).Scan(&taskPayload); err != nil {
		return ImportedSkill{}, errors.New("Hermes Skill snapshot upload is not authorized")
	}
	var task protocol.ImportSkillSnapshotPayload
	if json.Unmarshal(taskPayload, &task) != nil || task.DiscoveryID != discoveryID || task.Runtime != domain.RuntimeHermes ||
		task.Path != pathValue || task.SHA256 != expectedHash {
		return ImportedSkill{}, errors.New("Hermes Skill snapshot task identity mismatch")
	}
	if status == "imported" && lastHash == pkg.SHA256 && importedSkillID != "" && importedVersionID != "" {
		return ImportedSkill{SkillID: importedSkillID, VersionID: importedVersionID, SHA256: pkg.SHA256, Status: "pending"}, tx.Commit(ctx)
	}
	if status != "importing" {
		return ImportedSkill{}, ErrStateConflict
	}
	result, err := s.importHermesSkillSnapshotTx(ctx, tx, nodeID, pathValue, importedSkillID, pkg)
	if err != nil {
		return ImportedSkill{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE skill_discoveries SET imported_skill_id=$2,imported_version_id=$3,
		last_imported_sha256=$4,last_imported_at=now(),import_status='imported',import_error='',source_changed=false,updated_at=now()
		WHERE id=$1`, discoveryID, result.SkillID, result.VersionID, pkg.SHA256); err != nil {
		return ImportedSkill{}, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) importHermesSkillSnapshotTx(ctx context.Context, tx pgx.Tx, nodeID, pathValue, skillID string, pkg skills.Package) (ImportedSkill, error) {
	artifactID, err := ensureSkillArtifactTx(ctx, tx, pkg)
	if err != nil {
		return ImportedSkill{}, err
	}
	manifest, _ := json.Marshal(pkg.Manifest)
	provenance, _ := json.Marshal(map[string]any{
		"sourceKind": "hermes-import", "nodeId": nodeID, "runtime": domain.RuntimeHermes,
		"path": pathValue, "contentSHA256": pkg.SHA256, "readOnlySnapshot": true,
	})
	versionID := uuid.NewString()
	if skillID == "" {
		sourceID := uuid.NewString()
		skillID = uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO skill_sources(id,kind,name,subdirectory)
			VALUES($1,'hermes-import',$2,$3)`, sourceID, "Hermes snapshot · "+filepathBase(pathValue), pathValue); err != nil {
			return ImportedSkill{}, err
		}
		slug, err := uniqueDiscoveredSkillSlug(ctx, tx, pkg.Slug, nodeID, domain.RuntimeHermes)
		if err != nil {
			return ImportedSkill{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO skills(id,slug,name,description,source_id)
			VALUES($1,$2,$3,$4,$5)`, skillID, slug, pkg.Name, pkg.Description, sourceID); err != nil {
			return ImportedSkill{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO skill_versions(id,skill_id,source_commit,content_sha256,artifact_id,provenance,manifest,risk_level)
			VALUES($1,$2,$3,$3,$4,$5,$6,$7)`, versionID, skillID, pkg.SHA256, artifactID, string(provenance), string(manifest), pkg.Report.RiskLevel); err != nil {
			return ImportedSkill{}, err
		}
		return ImportedSkill{SkillID: skillID, VersionID: versionID, SourceID: sourceID, SHA256: pkg.SHA256, RiskLevel: pkg.Report.RiskLevel, Status: "pending"}, nil
	}
	var reviewStatus, sourceID, currentVersionID, currentSHA, currentRisk string
	var currentReport []byte
	err = tx.QueryRow(ctx, `SELECT skill.review_status,skill.source_id::text,coalesce(skill.current_version_id::text,''),
		coalesce(version.content_sha256,''),coalesce(version.risk_level,'low'),coalesce(artifact.scan_report,'{}'::jsonb)
		FROM skills skill LEFT JOIN skill_versions version ON version.id=skill.current_version_id
		LEFT JOIN skill_artifacts artifact ON artifact.id=version.artifact_id WHERE skill.id=$1 FOR UPDATE OF skill`, skillID).
		Scan(&reviewStatus, &sourceID, &currentVersionID, &currentSHA, &currentRisk, &currentReport)
	if err != nil {
		return ImportedSkill{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO skill_versions(id,skill_id,source_commit,content_sha256,artifact_id,provenance,manifest,risk_level)
		VALUES($1,$2,$3,$3,$4,$5,$6,$7)
		ON CONFLICT(skill_id,source_commit,content_sha256) DO UPDATE SET skill_id=excluded.skill_id
		RETURNING id::text`, versionID, skillID, pkg.SHA256, artifactID, string(provenance), string(manifest), pkg.Report.RiskLevel).Scan(&versionID)
	if err != nil {
		return ImportedSkill{}, err
	}
	if reviewStatus != "approved" || currentVersionID == "" {
		if _, err := tx.Exec(ctx, `UPDATE skills SET name=$2,description=$3,review_status='pending',updated_at=now() WHERE id=$1`,
			skillID, pkg.Name, pkg.Description); err != nil {
			return ImportedSkill{}, err
		}
		return ImportedSkill{SkillID: skillID, VersionID: versionID, SourceID: sourceID, SHA256: pkg.SHA256, RiskLevel: pkg.Report.RiskLevel, Status: "pending"}, nil
	}
	if currentSHA == pkg.SHA256 {
		return ImportedSkill{SkillID: skillID, VersionID: currentVersionID, SourceID: sourceID, SHA256: pkg.SHA256, RiskLevel: pkg.Report.RiskLevel, Status: "approved"}, nil
	}
	var previousReport map[string]any
	_ = json.Unmarshal(currentReport, &previousReport)
	diff, _ := json.Marshal(map[string]any{"fromSHA256": currentSHA, "toSHA256": pkg.SHA256, "fromFiles": previousReport["fileCount"], "toFiles": pkg.Report.FileCount, "fromBytes": previousReport["sizeBytes"], "toBytes": pkg.Report.SizeBytes})
	riskChange, _ := json.Marshal(map[string]any{"from": currentRisk, "to": pkg.Report.RiskLevel, "findings": pkg.Report.Findings})
	licenseChange, _ := json.Marshal(map[string]any{"from": previousReport["license"], "to": pkg.Report.License})
	updateID := uuid.NewString()
	if _, err := tx.Exec(ctx, `UPDATE updates SET status='superseded' WHERE skill_id=$1 AND status='available';
		INSERT INTO updates(id,skill_id,from_version_id,candidate_commit,candidate_sha256,diff,risk_change,license_change)
		VALUES($2,$1,$3,$4,$4,$5,$6,$7)`, skillID, updateID, currentVersionID, pkg.SHA256, string(diff), string(riskChange), string(licenseChange)); err != nil {
		return ImportedSkill{}, err
	}
	return ImportedSkill{SkillID: skillID, VersionID: versionID, SourceID: sourceID, SHA256: pkg.SHA256, RiskLevel: pkg.Report.RiskLevel, Status: "update_available"}, nil
}

func ensureSkillArtifactTx(ctx context.Context, tx pgx.Tx, pkg skills.Package) (string, error) {
	var artifactID string
	err := tx.QueryRow(ctx, "SELECT id::text FROM skill_artifacts WHERE sha256=$1", pkg.SHA256).Scan(&artifactID)
	if err == nil {
		return artifactID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	artifactID = uuid.NewString()
	report, _ := json.Marshal(pkg.Report)
	_, err = tx.Exec(ctx, `INSERT INTO skill_artifacts(id,sha256,size_bytes,content,scan_report)
		VALUES($1,$2,$3,$4,$5)`, artifactID, pkg.SHA256, len(pkg.CanonicalZIP), pkg.CanonicalZIP, string(report))
	return artifactID, err
}

func truncateImportError(cause error) string {
	message := "import failed"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	return message
}
