package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
)

func (a *API) listDiscoveries(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListDiscoveries(r.Context())
	a.serveList(w, r, raw, err)
}

func (a *API) adoptDiscoveredSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	principal := principalFrom(r.Context())
	job, err := a.store.EnqueueJob(r.Context(), "skill_adopt", map[string]any{"discoveryId": id}, false, principal.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "adopt_queued", ResourceType: "skill_discovery", ResourceID: id, Outcome: "success", IPAddress: clientIP(r)})
	writeJSON(w, http.StatusAccepted, job)
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
	skillJob, err := a.store.EnqueueJob(r.Context(), "sync", map[string]any{"nodeIds": input.NodeIDs, "skillIds": input.SkillIDs, "manual": true}, input.DryRun, principal.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	mcpJob, err := a.store.EnqueueJob(r.Context(), "mcp_sync", map[string]any{"nodeIds": input.NodeIDs, "profileIds": input.ProfileIDs, "deploymentIds": input.MCPDeploymentIDs, "manual": true}, input.DryRun, principal.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "reconcile", ResourceType: "runtime", Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"nodeIds": input.NodeIDs, "skillIds": input.SkillIDs, "profileIds": input.ProfileIDs, "mcpDeploymentIds": input.MCPDeploymentIDs, "dryRun": input.DryRun}})
	writeJSON(w, http.StatusAccepted, map[string]any{"jobs": []any{skillJob, mcpJob}})
}
