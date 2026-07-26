package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/store"
)

func (a *API) listMCPServers(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListMCPServers(r.Context())
	a.serveList(w, r, raw, err)
}
func (a *API) listMCPProfiles(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListMCPProfiles(r.Context())
	a.serveList(w, r, raw, err)
}
func (a *API) listMCPDeployments(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListMCPDeployments(r.Context())
	a.serveList(w, r, raw, err)
}

func (a *API) createMCPServer(w http.ResponseWriter, r *http.Request) {
	var input store.MCPServerInput
	if err := decodeJSON(w, r, &input, 512<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	principal := principalFrom(r.Context())
	id, err := a.store.CreateMCPServer(r.Context(), input, principal.ID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "mcp_create_failed", err.Error())
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "create", ResourceType: "mcp_server", ResourceID: id, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"transport": input.Transport}})
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (a *API) updateMCPServer(w http.ResponseWriter, r *http.Request) {
	var input store.MCPServerPatch
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := a.store.UpdateMCPServer(r.Context(), chi.URLParam(r, "id"), input); err != nil {
		writeError(w, r, http.StatusBadRequest, "mcp_update_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
}

func (a *API) deleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteMCPServer(r.Context(), chi.URLParam(r, "id")); err != nil {
		handleStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) createMCPProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		ServerIDs   []string `json:"serverIds"`
	}
	if err := decodeJSON(w, r, &input, 256<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id, err := a.store.CreateMCPProfile(r.Context(), input.Name, input.Description, principalFrom(r.Context()).ID, input.ServerIDs)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "mcp_profile_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (a *API) deployMCPProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProfileID string                      `json:"profileId"`
		Targets   []store.MCPDeploymentTarget `json:"targets"`
		DryRun    bool                        `json:"dryRun"`
	}
	if err := decodeJSON(w, r, &input, 512<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	deploymentIDs, err := a.store.SetMCPDeployments(r.Context(), input.ProfileID, input.Targets)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "mcp_deploy_failed", err.Error())
		return
	}
	job, err := a.store.EnqueueJob(r.Context(), "mcp_sync", map[string]any{"profileIds": []string{input.ProfileID}, "deploymentIds": deploymentIDs}, input.DryRun, principalFrom(r.Context()).ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) checkMCPHealth(w http.ResponseWriter, r *http.Request) {
	job, err := a.store.EnqueueJob(r.Context(), "mcp_health", map[string]string{"serverId": chi.URLParam(r, "id")}, false, principalFrom(r.Context()).ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
