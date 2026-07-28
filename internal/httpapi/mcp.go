package httpapi

import (
	"errors"
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
		if errors.Is(err, store.ErrSourceFileAuthoritative) || errors.Is(err, store.ErrTargetManagedByProfile) {
			a.handleStoreError(w, r, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, "mcp_update_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
}

func (a *API) deleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteMCPServer(r.Context(), chi.URLParam(r, "id")); err != nil {
		a.handleStoreError(w, r, err)
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
		if errors.Is(err, store.ErrSourceFileAuthoritative) {
			a.handleStoreError(w, r, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, "mcp_profile_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (a *API) setMCPProfileServers(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ServerIDs []string `json:"serverIds"`
	}
	if err := decodeJSON(w, r, &input, 256<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	profileID := chi.URLParam(r, "id")
	if err := a.store.SetMCPProfileServers(r.Context(), profileID, input.ServerIDs); err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrSourceFileAuthoritative) || errors.Is(err, store.ErrManagedMCPProfile) || errors.Is(err, store.ErrMCPProfileRuntime) || errors.Is(err, store.ErrTargetManagedByProfile) {
			a.handleStoreError(w, r, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, "mcp_profile_membership_failed", err.Error())
		return
	}
	principal := principalFrom(r.Context())
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "update_membership", ResourceType: "mcp_profile", ResourceID: profileID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"serverCount": len(input.ServerIDs)}})
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
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
	job, err := a.store.SetMCPDeployments(r.Context(), input.ProfileID, principalFrom(r.Context()).ID, input.Targets, input.DryRun)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrManagedMCPProfile) || errors.Is(err, store.ErrMCPProfileRuntime) || errors.Is(err, store.ErrTargetManagedByProfile) {
			a.handleStoreError(w, r, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, "mcp_deploy_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) checkMCPHealth(w http.ResponseWriter, r *http.Request) {
	job, err := a.store.EnqueueJob(r.Context(), "mcp_health", map[string]string{"serverId": chi.URLParam(r, "id")}, false, principalFrom(r.Context()).ID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) archiveMCPServer(w http.ResponseWriter, r *http.Request) {
	a.setMCPServerArchived(w, r, true)
}

func (a *API) unarchiveMCPServer(w http.ResponseWriter, r *http.Request) {
	a.setMCPServerArchived(w, r, false)
}

func (a *API) setMCPServerArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	id := chi.URLParam(r, "id")
	if err := a.store.SetMCPServerArchived(r.Context(), id, archived); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	principal := principalFrom(r.Context())
	action := "unarchive"
	if archived {
		action = "archive"
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: action, ResourceType: "mcp_server", ResourceID: id, Outcome: "success", IPAddress: clientIP(r)})
	w.WriteHeader(http.StatusNoContent)
}
