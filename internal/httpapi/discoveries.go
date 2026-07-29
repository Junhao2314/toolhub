package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/store"
)

func (a *API) listDiscoveries(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListDiscoveries(r.Context())
	a.serveList(w, r, raw, err)
}

func (a *API) adoptDiscoveredSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := a.store.ValidateSkillDiscoveryAdoption(r.Context(), id); err != nil {
		a.writeHermesImportError(w, r, err)
		return
	}
	principal := principalFrom(r.Context())
	job, err := a.store.EnqueueJob(r.Context(), "skill_adopt", map[string]any{"discoveryId": id}, false, principal.ID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "adopt_queued", ResourceType: "skill_discovery", ResourceID: id, Outcome: "success", IPAddress: clientIP(r)})
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) importHermesSkill(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExpectedSHA256 string `json:"expectedSha256"`
	}
	if err := decodeJSON(w, r, &input, 32<<10); err != nil || input.ExpectedSHA256 == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "expectedSha256 is required")
		return
	}
	id := chi.URLParam(r, "id")
	principal := principalFrom(r.Context())
	job, err := a.store.QueueHermesSkillImport(r.Context(), id, input.ExpectedSHA256, principal.ID)
	if err != nil {
		a.writeHermesImportError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "hermes_skill_import_queued", ResourceType: "skill_discovery", ResourceID: id, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"expectedSha256": input.ExpectedSHA256, "jobId": job.ID}})
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) importHermesMCP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ObservedGeneration int64 `json:"observedGeneration"`
		ConfirmSecrets     bool  `json:"confirmSecrets"`
	}
	if err := decodeJSON(w, r, &input, 32<<10); err != nil || input.ObservedGeneration <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "observedGeneration must be positive")
		return
	}
	id := chi.URLParam(r, "id")
	principal := principalFrom(r.Context())
	job, err := a.store.QueueHermesMCPImport(r.Context(), id, input.ObservedGeneration, input.ConfirmSecrets, principal.ID)
	if err != nil {
		a.writeHermesImportError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "hermes_mcp_import_queued", ResourceType: "mcp_discovery", ResourceID: id, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"observedGeneration": input.ObservedGeneration, "confirmedSecretKeys": input.ConfirmSecrets, "jobId": job.ID}})
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) writeHermesImportError(w http.ResponseWriter, r *http.Request, err error) {
	var changed *store.SourceChangedError
	if errors.As(err, &changed) {
		writeErrorDetails(w, r, http.StatusConflict, "source_changed", "The Hermes source changed; refresh discoveries and confirm the latest snapshot", map[string]any{
			"expectedSha256": changed.ExpectedSHA256, "observedSha256": changed.ObservedSHA256,
			"expectedGeneration": changed.ExpectedGeneration, "observedGeneration": changed.ObservedGeneration,
		})
		return
	}
	var confirmation *store.SecretConfirmationRequiredError
	if errors.As(err, &confirmation) {
		writeErrorDetails(w, r, http.StatusConflict, "secret_confirmation_required", "Confirm the named secret keys before importing this Hermes MCP snapshot", map[string]any{
			"envKeys": confirmation.EnvKeys, "headerKeys": confirmation.HeaderKeys, "targets": confirmation.Targets,
		})
		return
	}
	var inProgress *store.ImportInProgressError
	if errors.As(err, &inProgress) {
		writeErrorDetails(w, r, http.StatusConflict, "import_in_progress", "An import is already in progress", map[string]any{"status": inProgress.Status})
		return
	}
	if errors.Is(err, store.ErrAgentUpgradeRequired) {
		writeError(w, r, http.StatusPreconditionFailed, "agent_upgrade_required", "Upgrade the Agent before importing Hermes snapshots")
		return
	}
	if errors.Is(err, store.ErrHermesReadOnly) {
		writeError(w, r, http.StatusConflict, "hermes_read_only", "This discovery is not a Hermes read-only import source")
		return
	}
	a.handleStoreError(w, r, err)
}

func (a *API) reconcileNow(w http.ResponseWriter, r *http.Request) {
	var input struct {
		NodeIDs          []string `json:"nodeIds"`
		SkillIDs         []string `json:"skillIds"`
		ProfileIDs       []string `json:"profileIds"`
		MCPDeploymentIDs []string `json:"mcpDeploymentIds"`
		DryRun           bool     `json:"dryRun"`
	}
	if err := decodeJSON(w, r, &input, 256<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	principal := principalFrom(r.Context())
	jobs, err := a.store.EnqueueJobs(r.Context(), []store.JobInput{
		{Kind: "sync", Payload: map[string]any{"nodeIds": input.NodeIDs, "skillIds": input.SkillIDs, "manual": true}, DryRun: input.DryRun},
		{Kind: "mcp_sync", Payload: map[string]any{"nodeIds": input.NodeIDs, "profileIds": input.ProfileIDs, "deploymentIds": input.MCPDeploymentIDs, "manual": true}, DryRun: input.DryRun},
	}, principal.ID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "reconcile", ResourceType: "runtime", Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"nodeIds": input.NodeIDs, "skillIds": input.SkillIDs, "profileIds": input.ProfileIDs, "mcpDeploymentIds": input.MCPDeploymentIDs, "dryRun": input.DryRun}})
	writeJSON(w, http.StatusAccepted, map[string]any{"jobs": jobs})
}
