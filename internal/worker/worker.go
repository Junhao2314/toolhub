package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeclient"
	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/market"
	"github.com/Junhao2314/toolhub/internal/skills"
	"github.com/Junhao2314/toolhub/internal/store"
)

type Worker struct {
	store  *store.Store
	bridge *bridgeclient.Client
	market *market.Multi
	logger *slog.Logger
}

func New(st *store.Store, bridge *bridgeclient.Client, marketClient *market.Multi, logger *slog.Logger) *Worker {
	return &Worker{store: st, bridge: bridge, market: marketClient, logger: logger}
}

// Recover requeues interrupted control work and waits for each durable Bridge
// target step to reach a terminal state before replaying its idempotent request.
// Replay is what completes PostgreSQL snapshot/backup projection after a crash.
func (w *Worker) Recover(ctx context.Context) error {
	if _, err := w.store.RequeueRunningControlOperations(ctx); err != nil {
		return fmt.Errorf("recover control operations: %w", err)
	}
	items, err := w.store.RunningOperationTargets(ctx)
	if err != nil {
		return fmt.Errorf("list running operation targets: %w", err)
	}
	for _, item := range items {
		if item.BridgeOperationID == "" {
			if err := w.store.RequeueRunningOperationTarget(ctx, item.ID); err != nil {
				return err
			}
			continue
		}
		deadline := time.Now().Add(12 * time.Minute)
		for {
			operation, operationErr := w.bridge.Operation(ctx, item.BridgeOperationID)
			if operationErr != nil {
				var apiErr *bridgeprotocol.APIError
				if errors.As(operationErr, &apiErr) && apiErr.Code == bridgeprotocol.ErrInvalidRequest {
					break
				}
				return fmt.Errorf("recover Bridge operation %s: %w", item.BridgeOperationID, operationErr)
			}
			if bridgeprotocol.IsTerminalOperationStatus(operation.Status) {
				break
			}
			if !time.Now().Before(deadline) {
				return fmt.Errorf("Bridge operation %s remained non-terminal during startup recovery", item.BridgeOperationID)
			}
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := w.store.RequeueRunningOperationTarget(ctx, item.ID); err != nil {
			return fmt.Errorf("requeue recovered operation target %s: %w", item.ID, err)
		}
	}
	return nil
}

func (w *Worker) Run(ctx context.Context, concurrency int) {
	if concurrency < 1 {
		concurrency = 1
	}
	for index := 0; index < concurrency; index++ {
		go w.loop(ctx, index)
	}
	go w.controlLoop(ctx)
}

func (w *Worker) controlLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		operation, err := w.store.ClaimControlOperation(ctx)
		if errors.Is(err, store.ErrNotFound) {
			if !waitForWork(ctx) {
				return
			}
			continue
		}
		if err != nil {
			w.logger.Error("claim control operation", "error", err)
			if !waitForWork(ctx) {
				return
			}
			continue
		}
		result, apiErr := w.executeControl(ctx, operation)
		status := bridgeprotocol.OperationSucceeded
		if apiErr != nil {
			status = bridgeprotocol.OperationFailed
		}
		if err := w.store.FinishControlOperation(ctx, operation.ID, status, result, apiErr); err != nil {
			w.logger.Error("finish control operation", "operationId", operation.ID, "error", err)
		}
	}
}

func waitForWork(ctx context.Context) bool {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (w *Worker) executeControl(ctx context.Context, operation domain.Operation) (map[string]any, *bridgeprotocol.APIError) {
	switch operation.Kind {
	case "skill_import":
		var metadata struct {
			Source store.SourceInput `json:"source"`
		}
		if err := json.Unmarshal(operation.Metadata, &metadata); err != nil {
			return nil, publicError(err)
		}
		skill, created, err := w.importSource(ctx, metadata.Source)
		if err != nil {
			return nil, publicError(err)
		}
		return map[string]any{"skillId": skill.ID, "versionId": skill.CurrentVersionID, "created": created}, nil
	case "update_check":
		sources, err := w.store.RefreshableSkillSources(ctx)
		if err != nil {
			return nil, publicError(err)
		}
		updated, unchanged := 0, 0
		failures := []map[string]string{}
		for _, source := range sources {
			before := source.Commit
			refresh := source
			refresh.Commit = ""
			skill, _, importErr := w.importSource(ctx, refresh)
			if importErr != nil {
				failures = append(failures, map[string]string{"name": source.Name, "error": "update source unavailable"})
				continue
			}
			if before != "" && before == skill.SourceCommit {
				unchanged++
			} else {
				updated++
			}
		}
		result := map[string]any{"checked": len(sources), "updated": updated, "unchanged": unchanged, "failures": failures}
		if len(failures) > 0 && len(failures) == len(sources) && len(sources) > 0 {
			return result, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "All Library update sources were unavailable", Retryable: true}
		}
		return result, nil
	case "refresh":
		result, err := w.bridge.RefreshNodes(ctx, "node-refresh-"+operation.ID)
		if err != nil {
			return nil, publicError(err)
		}
		if err := w.store.UpsertDiscoveredNodes(ctx, result.Nodes); err != nil {
			return nil, publicError(err)
		}
		online := 0
		for _, node := range result.Nodes {
			if node.Status == "online" {
				online++
			}
		}
		return map[string]any{"discovered": len(result.Nodes), "online": online}, nil
	case "backup_gc":
		result, err := w.bridge.GCBackups(ctx, "backup-gc-"+operation.ID, bridgeprotocol.BackupGCRequest{MaxAgeDays: 30, MaxPerTarget: 10})
		if err != nil {
			return nil, publicError(err)
		}
		deleted, err := w.store.DeleteBackupsByBridgeIDs(ctx, result.RemovedBackupIDs)
		if err != nil {
			return nil, publicError(err)
		}
		return map[string]any{"removed": result.Removed, "catalogRowsDeleted": deleted, "maxAgeDays": 30, "maxPerTarget": 10}, nil
	default:
		return nil, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrUnsupportedOperation, Message: "Control operation kind is not supported"}
	}
}

func (w *Worker) importSource(ctx context.Context, source store.SourceInput) (domain.Skill, bool, error) {
	source.Kind = strings.ToLower(strings.TrimSpace(source.Kind))
	provenance := map[string]any{"sourceKind": source.Kind, "sourceUrl": source.URL}
	switch source.Kind {
	case "git", "skillsmp":
		pkg, commit, err := skills.ImportGit(ctx, source.URL, source.Subdirectory, source.Commit, skills.DefaultLimits)
		if err != nil {
			return domain.Skill{}, false, err
		}
		source.Commit = commit
		provenance["sourceCommit"] = commit
		return w.store.ImportSkill(ctx, source, pkg, provenance)
	case "xiaping":
		client, ok := w.market.Xiaping()
		if !ok {
			return domain.Skill{}, false, errors.New("Xiaping marketplace is not configured")
		}
		skillID, _ := source.Metadata["skillId"].(string)
		if strings.TrimSpace(skillID) == "" {
			return domain.Skill{}, false, errors.New("Xiaping source requires metadata.skillId")
		}
		download, err := client.Download(ctx, skillID, skills.DefaultLimits.MaxArchiveBytes)
		if err != nil {
			return domain.Skill{}, false, err
		}
		pkg, err := skills.ScanZIP(download.Archive, skills.DefaultLimits)
		if err != nil {
			return domain.Skill{}, false, err
		}
		source.Commit = download.Version
		provenance["sourceCommit"], provenance["skillPage"] = download.Version, download.SkillPage
		return w.store.ImportSkill(ctx, source, pkg, provenance)
	case "skillhub":
		client, ok := w.market.SkillHub()
		if !ok {
			return domain.Skill{}, false, errors.New("SkillHub marketplace is not configured")
		}
		skillID, _ := source.Metadata["skillId"].(string)
		if strings.TrimSpace(skillID) == "" {
			return domain.Skill{}, false, errors.New("SkillHub source requires metadata.skillId")
		}
		version, _ := source.Metadata["version"].(string)
		download, err := client.Download(ctx, skillID, version, skills.DefaultLimits.MaxArchiveBytes)
		if err != nil {
			return domain.Skill{}, false, err
		}
		pkg, err := skills.ScanZIP(download.Archive, skills.DefaultLimits)
		if err != nil {
			return domain.Skill{}, false, err
		}
		source.Commit = download.Version
		provenance["sourceCommit"], provenance["skillPage"] = download.Version, download.SkillPage
		return w.store.ImportSkill(ctx, source, pkg, provenance)
	default:
		return domain.Skill{}, false, errors.New("queued Skill import source is unsupported")
	}
}

func (w *Worker) loop(ctx context.Context, index int) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		item, err := w.store.ClaimOperationTarget(ctx)
		if errors.Is(err, store.ErrNotFound) {
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		if err != nil {
			w.logger.Error("claim operation target", "worker", index, "error", err)
			continue
		}
		if err := w.store.SetBridgeOperationMetadata(ctx, item.OperationTarget.ID, item.OperationTarget.ID, ""); err != nil {
			apiErr := publicError(err)
			if finishErr := w.store.FinishOperationTarget(ctx, item.OperationTarget.ID, bridgeprotocol.OperationFailed, bridgeprotocol.TargetResult{}, apiErr); finishErr != nil {
				w.logger.Error("finish operation target after Bridge metadata failure", "operationTargetId", item.OperationTarget.ID, "error", finishErr)
			}
			if _, rerunErr := w.store.EnqueuePendingRerun(ctx, item.OperationTarget.ID); rerunErr != nil {
				w.logger.Error("enqueue coalesced reconcile", "operationTargetId", item.OperationTarget.ID, "error", rerunErr)
			}
			continue
		}
		result, apiErr := w.execute(ctx, item)
		status := bridgeprotocol.OperationSucceeded
		if apiErr != nil {
			status = bridgeprotocol.OperationFailed
		} else if result.Status == bridgeprotocol.OperationPartial {
			status = bridgeprotocol.OperationPartial
		}
		if err := w.store.FinishOperationTarget(ctx, item.OperationTarget.ID, status, result, apiErr); err != nil {
			w.logger.Error("finish operation target", "operationTargetId", item.OperationTarget.ID, "error", err)
			continue
		}
		if _, err := w.store.EnqueuePendingRerun(ctx, item.OperationTarget.ID); err != nil {
			w.logger.Error("enqueue coalesced reconcile", "operationTargetId", item.OperationTarget.ID, "error", err)
		}
	}
}

func (w *Worker) execute(ctx context.Context, item store.WorkItem) (bridgeprotocol.TargetResult, *bridgeprotocol.APIError) {
	target := toBridgeTarget(item.Target)
	switch item.Operation.Kind {
	case "scan":
		response, err := w.bridge.Scan(ctx, item.OperationTarget.ID, bridgeprotocol.ScanRequest{Target: target})
		if err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
		var activeManifest *bridgeprotocol.DesiredManifest
		if _, manifest, manifestErr := w.store.ActiveDesiredManifest(ctx, item.Target.ID); manifestErr == nil {
			activeManifest = &manifest
		} else if !errors.Is(manifestErr, store.ErrNotFound) {
			return bridgeprotocol.TargetResult{}, publicError(manifestErr)
		}
		if target.Runtime == bridgeprotocol.RuntimeSharedRelay {
			settings, settingsErr := w.store.Settings(ctx)
			if settingsErr != nil {
				return bridgeprotocol.TargetResult{}, publicError(settingsErr)
			}
			status, statusErr := w.bridge.Relay(ctx, "health", item.OperationTarget.ID+":relay-health", bridgeprotocol.RelayActionRequest{Target: target, Port: settings.RelayPort, IntentionalPaused: settings.RelayIntentionalPaused, Manifest: activeManifest})
			if statusErr != nil {
				return bridgeprotocol.TargetResult{}, publicError(statusErr)
			}
			response.Relay = &status
		}
		if err := w.store.ReplaceRuntimeSnapshot(ctx, item.Target.ID, response.TargetRevision, map[string]any{"members": response.Members, "relay": response.Relay}); err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
		health := item.Target.Health
		if activeManifest != nil {
			var drift bridgeprotocol.Diff
			health, drift = projectScanHealth(*activeManifest, response.Members)
			if response.Relay != nil && !response.Relay.Healthy {
				health = bridgeprotocol.HealthBlocked
			}
			if response.Relay != nil {
				if _, err := w.store.UpdateRelayProjection(ctx, item.Target.ID, *response.Relay, health, response.Relay.ErrorCode, response.Relay.ErrorReason, drift, false, store.RelayProjectionObserve); err != nil {
					return bridgeprotocol.TargetResult{}, publicError(err)
				}
			} else if _, err := w.store.UpdateTargetHealth(ctx, item.Target.ID, health, "", "", drift, false); err != nil {
				return bridgeprotocol.TargetResult{}, publicError(err)
			}
		}
		return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: health, TargetRevision: response.TargetRevision, Relay: response.Relay}, nil
	case "skill_import":
		var batch struct {
			Items []bridgeprotocol.LocalSkillBatchItem `json:"items"`
		}
		if err := json.Unmarshal(item.OperationTarget.Request, &batch); err == nil && len(batch.Items) > 0 {
			return w.executeLocalSkillBatchImport(ctx, item, target, batch.Items)
		}
		return w.executeLocalSkillImport(ctx, item, target)
	case "mcp_import":
		return w.executeLocalMCPImport(ctx, item, target)
	case "apply", "edit", "restore":
		return w.executeCommit(ctx, item, target)
	case "reconcile":
		return w.executeReconcile(ctx, item, target)
	case "relay_start", "relay_stop", "relay_restart", "relay_health":
		action := map[string]string{"relay_start": "start", "relay_stop": "stop", "relay_restart": "restart", "relay_health": "health"}[item.Operation.Kind]
		settings, err := w.store.Settings(ctx)
		if err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
		var manifest *bridgeprotocol.DesiredManifest
		if _, desired, manifestErr := w.store.ActiveDesiredManifest(ctx, item.Target.ID); manifestErr == nil {
			manifest = &desired
		} else if !errors.Is(manifestErr, store.ErrNotFound) {
			return bridgeprotocol.TargetResult{}, publicError(manifestErr)
		}
		status, err := w.bridge.Relay(ctx, action, item.OperationTarget.ID, bridgeprotocol.RelayActionRequest{Target: target, Port: settings.RelayPort, IntentionalPaused: settings.RelayIntentionalPaused, Manifest: manifest})
		if err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
		if action == "stop" {
			if err := w.store.SetRelayIntentionalPaused(ctx, true); err != nil {
				return bridgeprotocol.TargetResult{}, publicError(err)
			}
			status.IntentionalPaused = true
		} else if action == "start" || action == "restart" {
			if err := w.store.SetRelayIntentionalPaused(ctx, false); err != nil {
				return bridgeprotocol.TargetResult{}, publicError(err)
			}
			status.IntentionalPaused = false
		}
		health := relayProjectedHealth(status)
		result := bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: health, Relay: &status, Error: relayProjectionError(status)}
		if manifest != nil {
			policy := store.RelayProjectionObserve
			if action == "start" || action == "restart" {
				policy = store.RelayProjectionReset
			}
			if _, err := w.store.UpdateRelayProjection(ctx, item.Target.ID, status, health, status.ErrorCode, status.ErrorReason, nil, false, policy); err != nil {
				return bridgeprotocol.TargetResult{}, publicError(err)
			}
			if err := w.store.UpdateRuntimeRelayStatus(ctx, item.Target.ID, item.Target.TargetRevision, status); err != nil {
				return bridgeprotocol.TargetResult{}, publicError(err)
			}
		}
		return result, nil
	default:
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrUnsupportedOperation, Message: "Operation kind is not executable on a target"}
	}
}

func projectScanHealth(manifest bridgeprotocol.DesiredManifest, members []bridgeprotocol.InventoryMember) (string, bridgeprotocol.Diff) {
	drift := bridgeprotocol.Diff{
		Add:      []bridgeprotocol.DiffItem{},
		Replace:  []bridgeprotocol.DiffItem{},
		Delete:   []bridgeprotocol.DiffItem{},
		Excluded: []bridgeprotocol.DiffItem{},
	}
	current := map[string]bridgeprotocol.InventoryMember{}
	protected := map[string]bool{}
	for _, member := range members {
		if member.Kind != "skill" && member.Kind != "mcp" {
			continue
		}
		key := member.Kind + "\x00" + member.Name
		if member.Protected {
			protected[key] = true
			continue
		}
		current[key] = member
	}
	compare := func(kind, memberID, name, contentHash string) {
		key := kind + "\x00" + name
		member, ok := current[key]
		if !ok {
			reason := "missing"
			if protected[key] {
				reason = "protected"
			}
			drift.Add = append(drift.Add, bridgeprotocol.DiffItem{Kind: kind, MemberID: memberID, Name: name, Reason: reason})
			return
		}
		if member.ContentHash != contentHash {
			drift.Replace = append(drift.Replace, bridgeprotocol.DiffItem{Kind: kind, MemberID: memberID, Name: name, Reason: "content_hash_mismatch"})
		}
	}
	for _, skill := range manifest.Skills {
		compare("skill", skill.MemberID, skill.Slug, skill.ContentHash)
	}
	for _, server := range manifest.MCPServers {
		compare("mcp", server.MemberID, server.Name, server.ContentHash)
	}
	sortItems := func(items []bridgeprotocol.DiffItem) {
		sort.Slice(items, func(i, j int) bool {
			if items[i].Kind != items[j].Kind {
				return items[i].Kind < items[j].Kind
			}
			return items[i].Name < items[j].Name
		})
	}
	sortItems(drift.Add)
	sortItems(drift.Replace)
	if len(drift.Add) != 0 || len(drift.Replace) != 0 {
		return bridgeprotocol.HealthDrifted, drift
	}
	return bridgeprotocol.HealthHealthy, drift
}

func (w *Worker) executeCommit(ctx context.Context, item store.WorkItem, target bridgeprotocol.Target) (bridgeprotocol.TargetResult, *bridgeprotocol.APIError) {
	var metadata struct {
		Manifest       bridgeprotocol.DesiredManifest `json:"manifest"`
		TargetRevision string                         `json:"targetRevision"`
		BackupID       string                         `json:"backupId"`
		SourceKind     string                         `json:"sourceKind"`
		SourceID       string                         `json:"sourceId"`
	}
	requestBody := item.OperationTarget.Request
	if len(requestBody) == 0 || string(requestBody) == "{}" {
		requestBody = item.Operation.Metadata
	}
	if err := json.Unmarshal(requestBody, &metadata); err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	if metadata.Manifest.SchemaVersion == 0 && item.Operation.Kind != "restore" {
		return bridgeprotocol.TargetResult{}, publicError(errors.New("operation manifest is missing"))
	}
	beforeManifest, err := w.store.DesiredManifestOrEmpty(ctx, target.ID)
	if err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	var restoredManifest *bridgeprotocol.DesiredManifest
	if item.Operation.Kind == "restore" {
		backup, err := w.store.Backup(ctx, metadata.BackupID)
		if err != nil || backup.TargetID != target.ID {
			return bridgeprotocol.TargetResult{}, publicError(store.ErrNotFound)
		}
		manifest, err := w.store.BackupDesiredManifest(ctx, backup)
		if err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
		metadata.BackupID = backup.BridgeBackupID
		metadata.Manifest = manifest
		restoredManifest = &manifest
	}
	if err := w.validateTargetBinding(ctx, target, metadata.Manifest); err != nil {
		apiErr := publicError(err)
		_, _ = w.store.UpdateTargetHealth(ctx, target.ID, healthForError(apiErr), apiErr.Code, apiErr.Message, map[string]any{}, false)
		return bridgeprotocol.TargetResult{}, apiErr
	}
	artifacts := []bridgeprotocol.Artifact{}
	if item.Operation.Kind != "restore" {
		artifacts, err = w.artifacts(ctx, metadata.Manifest)
		if err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
	}
	secrets, err := w.store.SecretValues(ctx, store.ManifestSecretIDs(metadata.Manifest))
	if err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	request := bridgeprotocol.CommitRequest{OperationID: item.OperationTarget.ID, OperationKind: item.Operation.Kind, Target: target, ExpectedRevision: metadata.TargetRevision, Manifest: metadata.Manifest, Artifacts: artifacts, SecretValues: secrets, BackupID: metadata.BackupID}
	if item.Operation.Kind == "restore" && target.Runtime == bridgeprotocol.RuntimeSharedRelay {
		settings, settingsErr := w.store.Settings(ctx)
		if settingsErr != nil {
			clear(secrets)
			return bridgeprotocol.TargetResult{}, publicError(settingsErr)
		}
		request.IntentionalPaused = settings.RelayIntentionalPaused
	}
	result, err := w.bridge.Commit(ctx, item.Operation.Kind, item.OperationTarget.ID, request)
	clear(secrets)
	w.captureBridgeSaltJID(ctx, item.OperationTarget.ID)
	if err != nil {
		apiErr := publicError(err)
		_, _ = w.store.UpdateTargetHealth(ctx, target.ID, healthForError(apiErr), apiErr.Code, apiErr.Message, map[string]any{}, false)
		return bridgeprotocol.TargetResult{}, apiErr
	}
	if target.Runtime == bridgeprotocol.RuntimeSharedRelay && (item.Operation.Kind == "apply" || item.Operation.Kind == "edit") {
		if err := w.store.SetRelayIntentionalPaused(ctx, false); err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
		if result.Relay != nil {
			result.Relay.IntentionalPaused = false
		}
	}
	if err := w.refreshRuntimeSnapshot(ctx, item.OperationTarget.ID, target, result); err != nil && w.logger != nil {
		w.logger.Warn("refresh runtime inventory after commit", "targetId", target.ID, "error", err)
	}
	if result.BackupID != "" {
		_, _ = w.store.RecordBackup(ctx, bridgeprotocol.Backup{ID: result.BackupID, TargetID: target.ID, NodeKind: target.NodeKind, SaltMinionID: target.SaltMinionID, Runtime: target.Runtime, Revision: metadata.TargetRevision, CreatedAt: time.Now().UTC()}, item.Operation.ID, &beforeManifest)
	}
	if restoredManifest != nil {
		result.Manifest = restoredManifest
	} else if result.Manifest == nil && metadata.Manifest.SchemaVersion != 0 {
		result.Manifest = &metadata.Manifest
	}
	if result.Manifest != nil {
		sourceKind := metadata.SourceKind
		if sourceKind == "" {
			sourceKind = "profile_apply"
		}
		if _, err := w.store.PinDesiredSnapshot(ctx, target.ID, sourceKind, metadata.SourceID, item.OperationTarget.ID, *result.Manifest); err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
	}
	result.Health = normalizedResultHealth(result.Health)
	if target.Runtime == bridgeprotocol.RuntimeSharedRelay {
		status := relayStatusFromResult(result)
		code, reason := resultProjectionError(result)
		if _, err := w.store.UpdateRelayProjection(ctx, target.ID, status, result.Health, code, reason, map[string]any{}, result.Repaired, store.RelayProjectionReset); err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
		if result.Relay != nil {
			if err := w.store.UpdateRuntimeRelayStatus(ctx, target.ID, result.TargetRevision, *result.Relay); err != nil {
				return bridgeprotocol.TargetResult{}, publicError(err)
			}
		}
	} else {
		code, reason := resultProjectionError(result)
		if _, err := w.store.UpdateTargetHealth(ctx, target.ID, result.Health, code, reason, map[string]any{}, result.Repaired); err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
	}
	return result, nil
}

func (w *Worker) executeReconcile(ctx context.Context, item store.WorkItem, target bridgeprotocol.Target) (bridgeprotocol.TargetResult, *bridgeprotocol.APIError) {
	var options struct {
		FullRelayProbe bool `json:"fullRelayProbe"`
	}
	if len(item.OperationTarget.Request) > 0 {
		if err := json.Unmarshal(item.OperationTarget.Request, &options); err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
	}
	_, manifest, err := w.store.ActiveDesiredManifest(ctx, target.ID)
	if err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	if err := w.validateTargetBinding(ctx, target, manifest); err != nil {
		apiErr := publicError(err)
		_, _ = w.store.UpdateTargetHealth(ctx, target.ID, healthForError(apiErr), apiErr.Code, apiErr.Message, map[string]any{}, false)
		return bridgeprotocol.TargetResult{}, apiErr
	}
	artifacts, err := w.artifacts(ctx, manifest)
	if err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	secrets, err := w.store.SecretValues(ctx, store.ManifestSecretIDs(manifest))
	if err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	settings, settingsErr := w.store.Settings(ctx)
	if settingsErr != nil {
		clear(secrets)
		return bridgeprotocol.TargetResult{}, publicError(settingsErr)
	}
	result, err := w.bridge.Reconcile(ctx, item.OperationTarget.ID, bridgeprotocol.ReconcileRequest{OperationID: item.OperationTarget.ID, Target: target, Manifest: manifest, Artifacts: artifacts, SecretValues: secrets, IntentionalPaused: target.Runtime == bridgeprotocol.RuntimeSharedRelay && settings.RelayIntentionalPaused, FullRelayProbe: options.FullRelayProbe})
	clear(secrets)
	w.captureBridgeSaltJID(ctx, item.OperationTarget.ID)
	if err != nil {
		apiErr := publicError(err)
		_, _ = w.store.UpdateTargetHealth(ctx, target.ID, healthForError(apiErr), apiErr.Code, apiErr.Message, map[string]any{}, false)
		return bridgeprotocol.TargetResult{}, apiErr
	}
	if err := w.refreshRuntimeSnapshot(ctx, item.OperationTarget.ID, target, result); err != nil && w.logger != nil {
		w.logger.Warn("refresh runtime inventory after reconcile", "targetId", target.ID, "error", err)
	}
	if result.BackupID != "" {
		_, _ = w.store.RecordBackup(ctx, bridgeprotocol.Backup{ID: result.BackupID, TargetID: target.ID, NodeKind: target.NodeKind, SaltMinionID: target.SaltMinionID, Runtime: target.Runtime, Revision: result.TargetRevision, CreatedAt: time.Now().UTC()}, item.Operation.ID, &manifest)
	}
	result.Health = normalizedResultHealth(result.Health)
	if target.Runtime == bridgeprotocol.RuntimeSharedRelay {
		status := relayStatusFromResult(result)
		policy := store.RelayProjectionObserve
		if item.Target.RelayNextRetryAt != nil {
			policy = store.RelayProjectionRetry
		}
		code, reason := resultProjectionError(result)
		if _, err := w.store.UpdateRelayProjection(ctx, target.ID, status, result.Health, code, reason, map[string]any{}, result.Repaired, policy); err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
		if result.Relay != nil {
			if err := w.store.UpdateRuntimeRelayStatus(ctx, target.ID, result.TargetRevision, *result.Relay); err != nil {
				return bridgeprotocol.TargetResult{}, publicError(err)
			}
		}
	} else {
		code, reason := resultProjectionError(result)
		if _, err := w.store.UpdateTargetHealth(ctx, target.ID, result.Health, code, reason, map[string]any{}, result.Repaired); err != nil {
			return bridgeprotocol.TargetResult{}, publicError(err)
		}
	}
	return result, nil
}

func normalizedResultHealth(health string) string {
	if health == "" {
		return bridgeprotocol.HealthHealthy
	}
	return health
}

func (w *Worker) refreshRuntimeSnapshot(ctx context.Context, operationID string, target bridgeprotocol.Target, result bridgeprotocol.TargetResult) error {
	response, err := w.bridge.Scan(ctx, operationID+":post-commit-scan", bridgeprotocol.ScanRequest{Target: target})
	if err != nil {
		return err
	}
	return w.store.ReplaceRuntimeSnapshot(ctx, target.ID, response.TargetRevision, runtimeInventoryPayload(response, result))
}

func runtimeInventoryPayload(response bridgeprotocol.ScanResponse, result bridgeprotocol.TargetResult) map[string]any {
	relay := response.Relay
	if relay == nil {
		relay = result.Relay
	}
	return map[string]any{"members": response.Members, "relay": relay}
}

func relayStatusFromResult(result bridgeprotocol.TargetResult) bridgeprotocol.RelayStatus {
	if result.Relay != nil {
		return *result.Relay
	}
	return bridgeprotocol.RelayStatus{Healthy: result.Health == bridgeprotocol.HealthHealthy}
}

func relayProjectedHealth(status bridgeprotocol.RelayStatus) string {
	if status.Healthy {
		return bridgeprotocol.HealthHealthy
	}
	return bridgeprotocol.HealthBlocked
}

func resultProjectionError(result bridgeprotocol.TargetResult) (string, string) {
	if result.Error != nil {
		return result.Error.Code, result.Error.Message
	}
	if result.Relay != nil {
		return result.Relay.ErrorCode, result.Relay.ErrorReason
	}
	return "", ""
}

func relayProjectionError(status bridgeprotocol.RelayStatus) *bridgeprotocol.APIError {
	if status.Healthy || status.ErrorCode == "" {
		return nil
	}
	message := status.ErrorReason
	if message == "" {
		message = "Relay runtime is blocked"
	}
	return &bridgeprotocol.APIError{Code: status.ErrorCode, Message: message, Retryable: true}
}

func (w *Worker) captureBridgeSaltJID(ctx context.Context, operationTargetID string) {
	operation, err := w.bridge.Operation(ctx, operationTargetID)
	if err != nil || len(operation.Targets) != 1 || operation.Targets[0].SaltJID == "" {
		return
	}
	if err := w.store.SetBridgeOperationMetadata(ctx, operationTargetID, operationTargetID, operation.Targets[0].SaltJID); err != nil {
		w.logger.Error("persist Salt JID", "operationTargetId", operationTargetID, "error", err)
	}
}

func (w *Worker) executeLocalSkillImport(ctx context.Context, item store.WorkItem, target bridgeprotocol.Target) (bridgeprotocol.TargetResult, *bridgeprotocol.APIError) {
	var request struct {
		TargetRevision string `json:"targetRevision"`
		Name           string `json:"name"`
		ContentHash    string `json:"contentHash"`
	}
	if err := json.Unmarshal(item.OperationTarget.Request, &request); err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	exported, err := w.bridge.ExportLocalSkill(ctx, bridgeprotocol.LocalSkillExportRequest{Target: target, ExpectedRevision: request.TargetRevision, Name: request.Name, ContentHash: request.ContentHash})
	if err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	defer clear(exported.Archive)
	pkg, err := skills.ScanZIP(exported.Archive, skills.DefaultLimits)
	if err != nil || pkg.SHA256 != exported.SHA256 || pkg.ContentHash != request.ContentHash {
		return bridgeprotocol.TargetResult{}, publicError(errors.New("exported local Skill package did not match its scanned identity"))
	}
	source := store.SourceInput{Kind: "local", Name: target.Runtime + "/" + request.Name, Commit: request.ContentHash, Metadata: map[string]any{"targetId": target.ID, "runtime": target.Runtime}}
	skill, created, err := w.store.ImportSkill(ctx, source, pkg, map[string]any{"sourceKind": "local", "targetId": target.ID, "targetRevision": request.TargetRevision, "contentHash": request.ContentHash})
	if err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	_ = w.store.Audit(ctx, domain.AuditEvent{Action: "skill_import", ResourceType: "skill", ResourceID: skill.ID, Outcome: "success", Metadata: map[string]any{"source": "local", "targetId": target.ID, "contentHash": request.ContentHash}})
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: item.Target.Health, Details: map[string]any{"skillId": skill.ID, "versionId": skill.CurrentVersionID, "created": created}}, nil
}

func (w *Worker) executeLocalSkillBatchImport(ctx context.Context, item store.WorkItem, target bridgeprotocol.Target, requested []bridgeprotocol.LocalSkillBatchItem) (bridgeprotocol.TargetResult, *bridgeprotocol.APIError) {
	if target.NodeKind != bridgeprotocol.NodeKindLocal || target.Runtime != bridgeprotocol.RuntimeHermes {
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrProtectedScope, Message: "Hermes Skill intake is available only from local Hermes"}
	}
	var request struct {
		TargetRevision string `json:"targetRevision"`
	}
	if err := json.Unmarshal(item.OperationTarget.Request, &request); err != nil || !bridgeprotocol.IsSHA256(request.TargetRevision) {
		return bridgeprotocol.TargetResult{}, publicError(errors.New("batch Skill import request is invalid"))
	}
	if len(requested) == 0 || len(requested) > 50 {
		return bridgeprotocol.TargetResult{}, publicError(errors.New("batch Skill import item count is invalid"))
	}
	results := make([]map[string]any, 0, len(requested))
	failed := make([]bridgeprotocol.LocalSkillBatchItem, 0)
	succeeded := 0
	// Keep each ephemeral response well below the Bridge ceiling. The Bridge
	// rescans for every chunk, so a stale revision fails the whole chunk while
	// individual archive/Library failures remain item-scoped below.
	for start := 0; start < len(requested); start += 3 {
		end := start + 3
		if end > len(requested) {
			end = len(requested)
		}
		response, err := w.bridge.ExportLocalSkillBatch(ctx, bridgeprotocol.LocalSkillBatchExportRequest{Target: target, ExpectedRevision: request.TargetRevision, Items: requested[start:end]})
		if err != nil {
			apiErr := publicError(err)
			for _, candidate := range requested[start:end] {
				failed = append(failed, candidate)
				results = append(results, map[string]any{"id": candidate.ID, "status": "Failed", "error": map[string]any{"code": apiErr.Code, "message": apiErr.Message}})
			}
			continue
		}
		for _, exported := range response.Items {
			itemResult := map[string]any{"id": exported.ID, "status": exported.Status}
			if exported.Error != nil || exported.Status != "Exported" {
				if exported.Error != nil {
					itemResult["error"] = map[string]any{"code": exported.Error.Code, "message": exported.Error.Message}
				}
				for _, candidate := range requested[start:end] {
					if candidate.ID == exported.ID {
						failed = append(failed, candidate)
						break
					}
				}
				results = append(results, itemResult)
				continue
			}
			pkg, scanErr := skills.ScanZIP(exported.Archive, skills.DefaultLimits)
			clear(exported.Archive)
			if scanErr != nil || pkg.SHA256 != exported.SHA256 || pkg.ContentHash != exported.ContentHash || pkg.Slug != exported.Slug {
				itemResult["status"] = "Failed"
				itemResult["error"] = map[string]any{"code": bridgeprotocol.ErrRevisionConflict, "message": "exported Skill package failed identity verification"}
				for _, candidate := range requested[start:end] {
					if candidate.ID == exported.ID {
						failed = append(failed, candidate)
						break
					}
				}
				results = append(results, itemResult)
				continue
			}
			source := store.SourceInput{Kind: "local", Name: "hermes/" + exported.Name, Commit: exported.ContentHash, Metadata: map[string]any{"source": "hermes", "targetId": target.ID, "provider": exported.Provider, "category": exported.Category}}
			imported, created, importErr := w.store.ImportSkill(ctx, source, pkg, map[string]any{"source": "hermes", "targetId": target.ID, "provider": exported.Provider, "category": exported.Category, "contentHash": exported.ContentHash})
			if importErr != nil {
				itemResult["status"] = "Failed"
				itemResult["error"] = map[string]any{"code": bridgeprotocol.ErrInvalidRequest, "message": "Skill could not be committed"}
				for _, candidate := range requested[start:end] {
					if candidate.ID == exported.ID {
						failed = append(failed, candidate)
						break
					}
				}
			} else {
				if created {
					itemResult["status"] = "Imported"
				} else {
					itemResult["status"] = "Reused"
				}
				itemResult["skillId"], itemResult["versionId"] = imported.ID, imported.CurrentVersionID
				succeeded++
			}
			results = append(results, itemResult)
		}
	}
	if len(failed) > 0 {
		_ = w.store.UpdateOperationTargetRequest(ctx, item.OperationTarget.ID, map[string]any{"targetRevision": request.TargetRevision, "items": failed})
	}
	details := map[string]any{"items": results, "failedItems": failed, "targetRevision": request.TargetRevision}
	if succeeded == 0 {
		return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationFailed, Health: item.Target.Health, Details: details}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "No Hermes Skills could be imported", Retryable: true}
	}
	if len(failed) > 0 {
		return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationPartial, Health: item.Target.Health, Details: details}, nil
	}
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: item.Target.Health, Details: details}, nil
}

func (w *Worker) executeLocalMCPImport(ctx context.Context, item store.WorkItem, target bridgeprotocol.Target) (bridgeprotocol.TargetResult, *bridgeprotocol.APIError) {
	if existing, err := w.store.MCPServer(ctx, item.OperationTarget.ID); err == nil {
		return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: item.Target.Health, Details: map[string]any{"serverId": existing.ID, "revision": existing.Revision, "recovered": true}}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	var request struct {
		TargetRevision string                               `json:"targetRevision"`
		ServerName     string                               `json:"serverName"`
		ContentHash    string                               `json:"contentHash"`
		Preview        bridgeprotocol.LocalMCPServerPreview `json:"preview"`
	}
	if err := json.Unmarshal(item.OperationTarget.Request, &request); err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	captured, err := w.bridge.CaptureLocalMCP(ctx, bridgeprotocol.LocalMCPCaptureRequest{Target: target, ExpectedRevision: request.TargetRevision, Name: request.ServerName, ContentHash: request.ContentHash})
	if err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	if !reflect.DeepEqual(captured.Preview, request.Preview) {
		clear(captured.Env)
		clear(captured.Headers)
		return bridgeprotocol.TargetResult{}, publicError(errors.New("captured local MCP server did not match its confirmed preview"))
	}
	input := store.MCPInput{Name: captured.Preview.Name, Description: "Imported from local " + target.Runtime, Transport: captured.Preview.Transport, Command: captured.Preview.Command, Args: captured.Preview.Args, URL: captured.Preview.URL, Env: captured.Env, Headers: captured.Headers}
	server, err := w.store.SaveMCPServer(ctx, item.OperationTarget.ID, input)
	clear(captured.Env)
	clear(captured.Headers)
	if err != nil {
		return bridgeprotocol.TargetResult{}, publicError(err)
	}
	_ = w.store.Audit(ctx, domain.AuditEvent{Action: "mcp_import", ResourceType: "mcp_server", ResourceID: server.ID, Outcome: "success", Metadata: map[string]any{"source": "local", "targetId": target.ID, "serverName": server.Name, "secretKeys": append(server.EnvKeys, server.HeaderKeys...)}})
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: item.Target.Health, Details: map[string]any{"serverId": server.ID, "revision": server.Revision}}, nil
}

func (w *Worker) artifacts(ctx context.Context, manifest bridgeprotocol.DesiredManifest) ([]bridgeprotocol.Artifact, error) {
	result := make([]bridgeprotocol.Artifact, 0, len(manifest.Skills))
	for _, skill := range manifest.Skills {
		archive, sha, err := w.store.SkillArtifact(ctx, skill.VersionID)
		if err != nil {
			return nil, err
		}
		if sha != skill.SHA256 {
			return nil, fmt.Errorf("snapshot Skill artifact hash changed")
		}
		result = append(result, bridgeprotocol.Artifact{VersionID: skill.VersionID, SHA256: sha, Archive: archive})
	}
	return result, nil
}

func toBridgeTarget(target domain.Target) bridgeprotocol.Target {
	return bridgeprotocol.Target{ID: target.ID, NodeID: target.NodeID, NodeKind: target.NodeKind, SaltMinionID: target.SaltMinionID, Runtime: target.Runtime, ManagedUsername: target.ManagedUsername}
}

func (w *Worker) validateTargetBinding(ctx context.Context, target bridgeprotocol.Target, manifest bridgeprotocol.DesiredManifest) error {
	relayPort := manifest.RelayPort
	if target.Runtime == bridgeprotocol.RuntimeSharedRelay {
		settings, err := w.store.Settings(ctx)
		if err != nil {
			return err
		}
		relayPort = settings.RelayPort
	}
	return validateTargetBinding(target, manifest, relayPort)
}

func validateTargetBinding(target bridgeprotocol.Target, manifest bridgeprotocol.DesiredManifest, relayPort int) error {
	if manifest.Target != target {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Desired snapshot target binding changed; run an explicit Apply or target edit"}
	}
	if target.Runtime == bridgeprotocol.RuntimeSharedRelay && manifest.RelayPort != relayPort {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Desired snapshot relay port changed; run an explicit Apply or target edit"}
	}
	return nil
}

func publicError(err error) *bridgeprotocol.APIError {
	var apiErr *bridgeprotocol.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "Operation failed"}
}

func healthForError(err *bridgeprotocol.APIError) string {
	if err.Code == bridgeprotocol.ErrTargetUnavailable {
		return bridgeprotocol.HealthUnavailable
	}
	return bridgeprotocol.HealthBlocked
}
