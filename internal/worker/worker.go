package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/toolhub-dev/toolhub/internal/agenthub"
	"github.com/toolhub-dev/toolhub/internal/domain"
	"github.com/toolhub-dev/toolhub/internal/remote"
	"github.com/toolhub-dev/toolhub/internal/skills"
	"github.com/toolhub-dev/toolhub/internal/store"
)

type Worker struct {
	store  *store.Store
	hub    *agenthub.Hub
	logger *slog.Logger
	ssh    *remote.Executor
}

func New(st *store.Store, hub *agenthub.Hub, ssh *remote.Executor, logger *slog.Logger) *Worker {
	return &Worker{store: st, hub: hub, ssh: ssh, logger: logger}
}

func (w *Worker) Run(ctx context.Context, concurrency int) {
	if concurrency < 1 {
		concurrency = 1
	}
	for index := 0; index < concurrency; index++ {
		go w.loop(ctx, index)
	}
}

func (w *Worker) loop(ctx context.Context, index int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		job, err := w.store.ClaimJob(ctx)
		if errors.Is(err, store.ErrNotFound) {
			timer := time.NewTimer(750 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		if err != nil {
			w.logger.Error("claim job", "worker", index, "error", err)
			time.Sleep(time.Second)
			continue
		}
		result, err := w.execute(ctx, job)
		if err != nil {
			w.logger.Warn("job failed", "jobId", job.ID, "kind", job.Kind, "attempt", job.Attempts, "error", err)
			if storeErr := w.store.FailJob(ctx, job, err.Error()); storeErr != nil {
				w.logger.Error("persist job failure", "jobId", job.ID, "error", storeErr)
			}
			continue
		}
		if err := w.store.FinishJob(ctx, job.ID, result); err != nil {
			w.logger.Error("finish job", "jobId", job.ID, "error", err)
		}
	}
}

func (w *Worker) execute(ctx context.Context, job domain.Job) (any, error) {
	switch job.Kind {
	case "inventory_scan":
		return w.inventoryScan(ctx, job)
	case "skill_import":
		return w.importSkill(ctx, job)
	case "skill_adopt":
		return w.adoptSkill(ctx, job)
	case "update_check":
		return w.checkUpdates(ctx, job)
	case "sync", "rollback":
		return w.syncSkills(ctx, job)
	case "mcp_sync":
		return w.syncMCP(ctx, job)
	case "mcp_health":
		return map[string]any{"status": "queued_on_next_reconcile"}, nil
	case "archive_purge":
		count, err := w.store.PurgeExpiredArchives(ctx)
		return map[string]any{"purged": count}, err
	default:
		return nil, fmt.Errorf("unsupported job kind %q", job.Kind)
	}
}

func (w *Worker) adoptSkill(ctx context.Context, job domain.Job) (any, error) {
	var input struct {
		DiscoveryID string `json:"discoveryId"`
	}
	if err := json.Unmarshal(job.Payload, &input); err != nil || input.DiscoveryID == "" {
		return nil, errors.New("skill adoption requires discoveryId")
	}
	target, err := w.store.SkillDiscoveryForAdoption(ctx, input.DiscoveryID)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"discoveryId": target.DiscoveryID, "runtime": target.Runtime, "path": target.Path, "sha256": target.SHA256}
	task, err := w.store.CreateNodeTask(ctx, target.NodeID, job.ID, "adopt_skill", payload)
	if err != nil {
		w.store.FailSkillAdoption(ctx, input.DiscoveryID, err)
		return nil, err
	}
	delivered := w.deliver(ctx, target.NodeID, task)
	return map[string]any{"taskId": task.ID, "delivered": delivered, "pendingOffline": !delivered}, nil
}

func (w *Worker) inventoryScan(ctx context.Context, job domain.Job) (any, error) {
	var input struct {
		NodeID string `json:"nodeId"`
	}
	if err := json.Unmarshal(job.Payload, &input); err != nil || input.NodeID == "" {
		return nil, errors.New("inventory scan requires nodeId")
	}
	task, err := w.store.CreateNodeTask(ctx, input.NodeID, job.ID, "scan_inventory", map[string]any{"readOnly": true})
	if err != nil {
		return nil, err
	}
	delivered := w.deliver(ctx, input.NodeID, task)
	return map[string]any{"taskId": task.ID, "delivered": delivered, "pendingOffline": !delivered}, nil
}

func (w *Worker) importSkill(ctx context.Context, job domain.Job) (any, error) {
	var input struct {
		Kind, Name, URL, Subdirectory, Commit string
	}
	if err := json.Unmarshal(job.Payload, &input); err != nil {
		return nil, err
	}
	importCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	pkg, commit, err := skills.ImportGit(importCtx, input.URL, input.Subdirectory, input.Commit, skills.DefaultLimits)
	if err != nil {
		return nil, err
	}
	name := input.Name
	if name == "" {
		name = pkg.Name
	}
	result, err := w.store.ImportSkill(ctx, store.SourceInput{Kind: input.Kind, Name: name, URL: input.URL, Subdirectory: input.Subdirectory, Commit: commit}, pkg, map[string]any{"importedByJob": job.ID}, job.CreatedBy)
	return result, err
}

func (w *Worker) checkUpdates(ctx context.Context, job domain.Job) (any, error) {
	var input struct {
		SkillIDs  []string `json:"skillIds"`
		ScopeType string   `json:"scopeType"`
		ScopeID   string   `json:"scopeId"`
	}
	_ = json.Unmarshal(job.Payload, &input)
	if input.ScopeType == "skill" && input.ScopeID != "" {
		input.SkillIDs = append(input.SkillIDs, input.ScopeID)
	}
	sources, err := w.store.UpdateSources(ctx, input.SkillIDs)
	if err != nil {
		return nil, err
	}
	var candidates []map[string]any
	for _, source := range sources {
		if input.ScopeType == "source" && input.ScopeID != source.SourceID {
			continue
		}
		importCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		pkg, commit, importErr := skills.ImportGit(importCtx, source.URL, source.Subdirectory, "HEAD", skills.DefaultLimits)
		cancel()
		if importErr != nil {
			candidates = append(candidates, map[string]any{"skillId": source.SkillID, "status": "failed", "error": importErr.Error()})
			continue
		}
		if pkg.SHA256 == source.CurrentSHA256 {
			candidates = append(candidates, map[string]any{"skillId": source.SkillID, "status": "current", "commit": commit})
			continue
		}
		if job.DryRun {
			candidates = append(candidates, map[string]any{"skillId": source.SkillID, "status": "would_create", "commit": commit, "sha256": pkg.SHA256, "risk": pkg.Report.RiskLevel})
			continue
		}
		updateID, addErr := w.store.AddUpdateCandidate(ctx, source, commit, pkg, job.CreatedBy)
		if addErr != nil {
			candidates = append(candidates, map[string]any{"skillId": source.SkillID, "status": "failed", "error": addErr.Error()})
			continue
		}
		candidates = append(candidates, map[string]any{"skillId": source.SkillID, "status": "available", "updateId": updateID, "commit": commit, "sha256": pkg.SHA256})
	}
	return map[string]any{"candidates": candidates, "dryRun": job.DryRun}, nil
}

func (w *Worker) syncSkills(ctx context.Context, job domain.Job) (any, error) {
	var selectors struct {
		NodeIDs   []string `json:"nodeIds"`
		SkillIDs  []string `json:"skillIds"`
		ScopeType string   `json:"scopeType"`
		ScopeID   string   `json:"scopeId"`
	}
	_ = json.Unmarshal(job.Payload, &selectors)
	if selectors.ScopeType == "skill" && selectors.ScopeID != "" {
		selectors.SkillIDs = append(selectors.SkillIDs, selectors.ScopeID)
	}
	nodes := makeSet(selectors.NodeIDs)
	skillsSet := makeSet(selectors.SkillIDs)
	deployments, err := w.store.PendingSkillDeployments(ctx)
	if err != nil {
		return nil, err
	}
	queued, delivered, skipped := 0, 0, 0
	for _, deployment := range deployments {
		if (len(nodes) > 0 && !nodes[deployment.NodeID]) || (len(skillsSet) > 0 && !skillsSet[deployment.SkillID]) {
			skipped++
			continue
		}
		if selectors.ScopeType == "source" && selectors.ScopeID != deployment.SourceID {
			skipped++
			continue
		}
		if selectors.ScopeType == "node_group" && selectors.ScopeID != deployment.NodeGroup {
			skipped++
			continue
		}
		payload := map[string]any{"deploymentId": deployment.DeploymentID, "runtime": deployment.Runtime, "skillSlug": deployment.SkillSlug, "versionId": deployment.VersionID, "sha256": deployment.SHA256, "enabled": deployment.Enabled}
		if job.DryRun {
			queued++
			continue
		}
		task, err := w.store.CreateNodeTask(ctx, deployment.NodeID, job.ID, "deploy_skill", payload)
		if err != nil {
			return nil, err
		}
		queued++
		if w.deliver(ctx, deployment.NodeID, task) {
			delivered++
		}
	}
	return map[string]any{"queued": queued, "delivered": delivered, "pendingOffline": queued - delivered, "skipped": skipped, "dryRun": job.DryRun}, nil
}

func (w *Worker) syncMCP(ctx context.Context, job domain.Job) (any, error) {
	var selectors struct {
		NodeIDs       []string `json:"nodeIds"`
		ProfileIDs    []string `json:"profileIds"`
		DeploymentIDs []string `json:"deploymentIds"`
		ScopeType     string   `json:"scopeType"`
		ScopeID       string   `json:"scopeId"`
	}
	_ = json.Unmarshal(job.Payload, &selectors)
	nodes := makeSet(selectors.NodeIDs)
	profiles := makeSet(selectors.ProfileIDs)
	deploymentIDs := makeSet(selectors.DeploymentIDs)
	deployments, err := w.store.PendingMCPDeployments(ctx)
	if err != nil {
		return nil, err
	}
	queued, delivered, skipped := 0, 0, 0
	for _, deployment := range deployments {
		if !matchesMCPSelectors(deployment, nodes, profiles, deploymentIDs, selectors.ScopeType, selectors.ScopeID) {
			skipped++
			continue
		}
		nodeID, _, payload, err := w.store.MCPDeploymentPayload(ctx, deployment.DeploymentID)
		if err != nil {
			return nil, err
		}
		payload["deploymentId"] = deployment.DeploymentID
		if job.DryRun {
			queued++
			continue
		}
		task, err := w.store.CreateNodeTask(ctx, nodeID, job.ID, "apply_mcp", payload)
		if err != nil {
			return nil, err
		}
		queued++
		if w.deliver(ctx, nodeID, task) {
			delivered++
		}
	}
	return map[string]any{"queued": queued, "delivered": delivered, "pendingOffline": queued - delivered, "skipped": skipped, "dryRun": job.DryRun}, nil
}

func matchesMCPSelectors(deployment store.MCPDeploymentRef, nodes, profiles, deploymentIDs map[string]bool, scopeType, scopeID string) bool {
	if len(nodes) > 0 && !nodes[deployment.NodeID] {
		return false
	}
	if len(profiles) > 0 && !profiles[deployment.ProfileID] {
		return false
	}
	if len(deploymentIDs) > 0 && !deploymentIDs[deployment.DeploymentID] {
		return false
	}
	return scopeType != "node_group" || scopeID == "" || deployment.NodeGroup == scopeID
}

func (w *Worker) deliver(ctx context.Context, nodeID string, task domain.AgentTask) bool {
	if w.hub.SendTask(nodeID, task) == nil {
		return true
	}
	if w.ssh != nil && w.ssh.Dispatch(ctx, nodeID, task) == nil {
		return true
	}
	return false
}

func makeSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
