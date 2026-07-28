package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Junhao2314/toolhub/internal/agenthub"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/market"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/remote"
	"github.com/Junhao2314/toolhub/internal/skills"
	"github.com/Junhao2314/toolhub/internal/store"
)

type Worker struct {
	store      *store.Store
	hub        *agenthub.Hub
	logger     *slog.Logger
	ssh        *remote.Executor
	market     *market.Multi
	instanceID string
}

const (
	jobLeaseDuration              = 60 * time.Second
	jobLeaseRenewal               = 20 * time.Second
	activationStatePersistTimeout = 5 * time.Second
)

func New(st *store.Store, hub *agenthub.Hub, ssh *remote.Executor, marketClient *market.Multi, logger *slog.Logger, instanceID string) *Worker {
	if instanceID == "" {
		instanceID = fmt.Sprintf("pid-%d", time.Now().UnixNano())
	}
	return &Worker{store: st, hub: hub, ssh: ssh, market: marketClient, logger: logger, instanceID: instanceID}
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
	owner := fmt.Sprintf("%s/worker-%d", w.instanceID, index)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		job, err := w.store.ClaimJob(ctx, owner, jobLeaseDuration)
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
		result, err := w.executeWithLease(ctx, job, owner)
		if err != nil {
			w.logger.Warn("job failed", "jobId", job.ID, "kind", job.Kind, "attempt", job.Attempts, "error", err)
			if storeErr := w.store.FailJob(ctx, job, owner, err.Error()); storeErr != nil && !errors.Is(storeErr, store.ErrJobCancelled) {
				w.logger.Error("persist job failure", "jobId", job.ID, "error", storeErr)
			}
			continue
		}
		if err := w.store.FinishJob(ctx, job.ID, owner, job.Attempts, result); err != nil && !errors.Is(err, store.ErrJobCancelled) {
			w.logger.Error("finish job", "jobId", job.ID, "error", err)
		}
	}
}

func (w *Worker) executeWithLease(ctx context.Context, job domain.Job, owner string) (any, error) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(jobLeaseRenewal)
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				if err := w.store.RenewJobLease(ctx, job.ID, owner, job.Attempts, jobLeaseDuration); err != nil {
					w.logger.Warn("job lease renewal stopped", "jobId", job.ID, "kind", job.Kind, "attempt", job.Attempts, "leaseOwner", owner, "error", err)
					cancel()
					return
				}
			}
		}
	}()
	result, err := w.execute(jobCtx, job)
	cancel()
	<-done
	return result, err
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
	case "profile_activate":
		return w.activateProfile(ctx, job)
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
		Kind, Name, URL, Subdirectory, Commit, ExternalID string
	}
	if err := json.Unmarshal(job.Payload, &input); err != nil {
		return nil, err
	}
	importCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if input.Kind == "xiaping" {
		return w.importXiapingSkill(importCtx, ctx, job, input.Name, input.ExternalID)
	}
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

// importXiapingSkill downloads the marketplace ZIP through the authenticated Xiaping API,
// scans it under the standard package limits, and queues it for review like any other import.
// Official skills charge the configured account's platform coins at download time.
func (w *Worker) importXiapingSkill(importCtx, ctx context.Context, job domain.Job, name, externalID string) (any, error) {
	if externalID == "" {
		return nil, errors.New("xiaping import requires externalId")
	}
	if w.market == nil {
		return nil, errors.New("marketplace sources are not configured")
	}
	xiaping, ok := w.market.Xiaping()
	if !ok {
		return nil, errors.New("xiaping marketplace source is not configured")
	}
	download, err := xiaping.Download(importCtx, externalID, skills.DefaultLimits.MaxArchiveBytes)
	if err != nil {
		return nil, err
	}
	pkg, err := skills.ScanZIP(download.Archive, skills.DefaultLimits)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = pkg.Name
	}
	result, err := w.store.ImportSkill(ctx, store.SourceInput{Kind: "xiaping", Name: name, URL: download.SkillPage, Commit: download.Version}, pkg, map[string]any{"importedByJob": job.ID, "xiapingSkillId": externalID, "xiapingVersion": download.Version, "xiapingCoinsSpent": download.CoinsSpent}, job.CreatedBy)
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
	var selectors skillDispatchSelectors
	_ = json.Unmarshal(job.Payload, &selectors)
	if selectors.ScopeType == "skill" && selectors.ScopeID != "" {
		selectors.SkillIDs = append(selectors.SkillIDs, selectors.ScopeID)
	}
	summary, err := w.dispatchSkillDeployments(ctx, selectors, job.DryRun, job.ID)
	return summary.asMap(job.DryRun), err
}

type skillDispatchSelectors struct {
	NodeIDs       []string `json:"nodeIds"`
	SkillIDs      []string `json:"skillIds"`
	DeploymentIDs []string `json:"deploymentIds"`
	Runtime       string   `json:"runtime"`
	ScopeType     string   `json:"scopeType"`
	ScopeID       string   `json:"scopeId"`
}

type mcpDispatchSelectors struct {
	NodeIDs       []string `json:"nodeIds"`
	ProfileIDs    []string `json:"profileIds"`
	DeploymentIDs []string `json:"deploymentIds"`
	ScopeType     string   `json:"scopeType"`
	ScopeID       string   `json:"scopeId"`
}

type dispatchSummary struct {
	Queued    int `json:"queued"`
	Delivered int `json:"delivered"`
	Skipped   int `json:"skipped"`
}

func (s dispatchSummary) pendingOffline() int { return s.Queued - s.Delivered }

func (s dispatchSummary) asMap(dryRun bool) map[string]any {
	return map[string]any{"queued": s.Queued, "delivered": s.Delivered, "pendingOffline": s.pendingOffline(), "skipped": s.Skipped, "dryRun": dryRun}
}

func (w *Worker) dispatchSkillDeployments(ctx context.Context, selectors skillDispatchSelectors, dryRun bool, jobID string) (dispatchSummary, error) {
	nodes := makeSet(selectors.NodeIDs)
	skillsSet := makeSet(selectors.SkillIDs)
	deploymentIDs := makeSet(selectors.DeploymentIDs)
	deployments, err := w.store.PendingSkillDeployments(ctx)
	if err != nil {
		return dispatchSummary{}, err
	}
	summary := dispatchSummary{}
	for _, deployment := range deployments {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if (len(nodes) > 0 && !nodes[deployment.NodeID]) || (len(skillsSet) > 0 && !skillsSet[deployment.SkillID]) ||
			(len(deploymentIDs) > 0 && !deploymentIDs[deployment.DeploymentID]) || (selectors.Runtime != "" && selectors.Runtime != deployment.Runtime) {
			summary.Skipped++
			continue
		}
		if selectors.ScopeType == "source" && selectors.ScopeID != deployment.SourceID {
			summary.Skipped++
			continue
		}
		if selectors.ScopeType == "node_group" && selectors.ScopeID != deployment.NodeGroup {
			summary.Skipped++
			continue
		}
		payload := protocol.DeploySkillPayload{
			DeploymentID:      deployment.DeploymentID,
			DesiredGeneration: deployment.DesiredGeneration,
			Runtime:           deployment.Runtime,
			SkillSlug:         deployment.SkillSlug,
			VersionID:         deployment.VersionID,
			SHA256:            deployment.SHA256,
			Enabled:           deployment.Enabled,
		}
		if dryRun {
			summary.Queued++
			continue
		}
		task, err := w.store.CreateNodeTaskWithOptions(ctx, deployment.NodeID, jobID, "deploy_skill", payload, store.NodeTaskOptions{
			TargetKind:       "skill_deployment",
			TargetID:         deployment.DeploymentID,
			TargetGeneration: deployment.DesiredGeneration,
			SemanticKey:      fmt.Sprintf("deploy_skill:%s:%d:%s:%t", deployment.DeploymentID, deployment.DesiredGeneration, deployment.VersionID, deployment.Enabled),
		})
		if err != nil {
			return summary, err
		}
		summary.Queued++
		if w.deliver(ctx, deployment.NodeID, task) {
			summary.Delivered++
		}
	}
	return summary, nil
}

func (w *Worker) syncMCP(ctx context.Context, job domain.Job) (any, error) {
	var selectors mcpDispatchSelectors
	_ = json.Unmarshal(job.Payload, &selectors)
	summary, err := w.dispatchMCPDeployments(ctx, selectors, job.DryRun, job.ID)
	return summary.asMap(job.DryRun), err
}

func (w *Worker) dispatchMCPDeployments(ctx context.Context, selectors mcpDispatchSelectors, dryRun bool, jobID string) (dispatchSummary, error) {
	nodes := makeSet(selectors.NodeIDs)
	profiles := makeSet(selectors.ProfileIDs)
	deploymentIDs := makeSet(selectors.DeploymentIDs)
	deployments, err := w.store.PendingMCPDeployments(ctx)
	if err != nil {
		return dispatchSummary{}, err
	}
	summary := dispatchSummary{}
	for _, deployment := range deployments {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if !matchesMCPSelectors(deployment, nodes, profiles, deploymentIDs, selectors.ScopeType, selectors.ScopeID) {
			summary.Skipped++
			continue
		}
		nodeID, payload, err := w.store.MCPDeploymentPayload(ctx, deployment.DeploymentID)
		if err != nil {
			return summary, err
		}
		if dryRun {
			summary.Queued++
			continue
		}
		task, err := w.store.CreateNodeTaskWithOptions(ctx, nodeID, jobID, "apply_mcp", payload, store.NodeTaskOptions{
			TargetKind:       "mcp_deployment",
			TargetID:         payload.DeploymentID,
			TargetGeneration: payload.DesiredGeneration,
			SemanticKey:      fmt.Sprintf("apply_mcp:%s:%d:%s:%t", payload.DeploymentID, payload.DesiredGeneration, payload.DesiredHash, payload.Enabled),
		})
		if err != nil {
			return summary, err
		}
		summary.Queued++
		if w.deliver(ctx, nodeID, task) {
			summary.Delivered++
		}
	}
	return summary, nil
}

func (w *Worker) activateProfile(ctx context.Context, job domain.Job) (any, error) {
	var input struct {
		ActivationID       string   `json:"activationId"`
		NodeIDs            []string `json:"nodeIds"`
		Runtime            string   `json:"runtime"`
		SkillIDs           []string `json:"skillIds"`
		SkillDeploymentIDs []string `json:"skillDeploymentIds"`
		ProfileIDs         []string `json:"profileIds"`
		MCPDeploymentIDs   []string `json:"mcpDeploymentIds"`
	}
	if err := json.Unmarshal(job.Payload, &input); err != nil || input.ActivationID == "" || len(input.NodeIDs) != 1 || input.Runtime == "" {
		return nil, errors.New("profile activation requires activationId, one nodeId, and runtime")
	}
	result := map[string]any{"activationId": input.ActivationID}
	skillsSummary, err := w.dispatchSkillDeployments(ctx, skillDispatchSelectors{
		NodeIDs: input.NodeIDs, SkillIDs: input.SkillIDs, DeploymentIDs: input.SkillDeploymentIDs, Runtime: input.Runtime,
	}, false, job.ID)
	result["skills"] = skillsSummary.asMap(false)
	if err != nil {
		state := "failed"
		if skillsSummary.Queued > 0 {
			state = "partial"
		}
		result["state"] = state
		if stateErr := w.persistProfileActivationState(ctx, input.ActivationID, state, err.Error()); stateErr != nil {
			return result, errors.Join(err, fmt.Errorf("persist profile activation state: %w", stateErr))
		}
		return result, err
	}
	if skillsSummary.pendingOffline() > 0 {
		message := fmt.Sprintf("%d Skill task(s) pending while the node is offline", skillsSummary.pendingOffline())
		if err := w.persistProfileActivationState(ctx, input.ActivationID, "partial", message); err != nil {
			return result, err
		}
		result["state"] = "partial"
		return result, nil
	}

	mcpSummary := dispatchSummary{}
	if len(input.ProfileIDs) > 0 || len(input.MCPDeploymentIDs) > 0 {
		mcpSummary, err = w.dispatchMCPDeployments(ctx, mcpDispatchSelectors{
			NodeIDs: input.NodeIDs, ProfileIDs: input.ProfileIDs, DeploymentIDs: input.MCPDeploymentIDs,
		}, false, job.ID)
		result["mcp"] = mcpSummary.asMap(false)
		if err != nil {
			result["state"] = "partial"
			if stateErr := w.persistProfileActivationState(ctx, input.ActivationID, "partial", err.Error()); stateErr != nil {
				return result, errors.Join(err, fmt.Errorf("persist profile activation state: %w", stateErr))
			}
			return result, err
		}
		if mcpSummary.pendingOffline() > 0 {
			message := fmt.Sprintf("%d MCP task(s) pending while the node is offline", mcpSummary.pendingOffline())
			if err := w.persistProfileActivationState(ctx, input.ActivationID, "partial", message); err != nil {
				return result, err
			}
			result["state"] = "partial"
			return result, nil
		}
	}
	if err := w.persistProfileActivationState(ctx, input.ActivationID, "active", ""); err != nil {
		return result, err
	}
	result["state"] = "active"
	return result, nil
}

func (w *Worker) persistProfileActivationState(ctx context.Context, activationID, state, lastError string) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), activationStatePersistTimeout)
	defer cancel()
	return w.store.SetProfileActivationState(persistCtx, activationID, state, lastError)
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
	owner := w.instanceID + "/dispatch"
	if w.hub.IsOnline(nodeID) {
		if err := w.hub.SendTask(ctx, nodeID, task, owner); err != nil {
			w.logger.Warn("WSS task delivery failed after online selection", "nodeId", nodeID, "taskId", task.ID, "kind", task.Kind, "error", err)
			return false
		}
		return true
	}
	if w.ssh != nil {
		if err := w.ssh.Dispatch(ctx, nodeID, task, owner); err != nil {
			w.logger.Warn("SSH task delivery failed", "nodeId", nodeID, "taskId", task.ID, "kind", task.Kind, "error", err)
			return false
		}
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
