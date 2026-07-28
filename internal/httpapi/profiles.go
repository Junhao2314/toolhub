package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/store"
)

type profileTargetRequest struct {
	NodeID  string `json:"nodeId"`
	Runtime string `json:"runtime"`
}

func (a *API) listProfiles(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListToolHubProfiles(r.Context())
	a.serveList(w, r, raw, err)
}

func (a *API) getProfile(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.GetToolHubProfile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, raw)
}

func (a *API) createProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	principal := principalFrom(r.Context())
	id, err := a.store.CreateToolHubProfile(r.Context(), input.Name, input.Description, principal.ID)
	if err != nil {
		a.handleProfileMutationError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "create", ResourceType: "toolhub_profile", ResourceID: id, Outcome: "success", IPAddress: clientIP(r)})
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (a *API) updateProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := a.store.UpdateToolHubProfile(r.Context(), id, input.Name, input.Description); err != nil {
		a.handleProfileMutationError(w, r, err)
		return
	}
	principal := principalFrom(r.Context())
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "update", ResourceType: "toolhub_profile", ResourceID: id, Outcome: "success", IPAddress: clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
}

func (a *API) setProfileMembers(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MCPServerIDs []string `json:"mcpServerIds"`
		SkillIDs     []string `json:"skillIds"`
	}
	if err := decodeJSON(w, r, &input, 256<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := a.store.SetToolHubProfileMembers(r.Context(), id, input.MCPServerIDs, input.SkillIDs); err != nil {
		a.handleProfileMutationError(w, r, err)
		return
	}
	principal := principalFrom(r.Context())
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "update_membership", ResourceType: "toolhub_profile", ResourceID: id, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"mcpServerCount": len(input.MCPServerIDs), "skillCount": len(input.SkillIDs)}})
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
}

func (a *API) deleteProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := a.store.DeleteToolHubProfile(r.Context(), id); err != nil {
		a.handleProfileMutationError(w, r, err)
		return
	}
	principal := principalFrom(r.Context())
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "delete", ResourceType: "toolhub_profile", ResourceID: id, Outcome: "success", IPAddress: clientIP(r)})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) preflightProfile(w http.ResponseWriter, r *http.Request) {
	var input profileTargetRequest
	if err := decodeJSON(w, r, &input, 32<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.NodeID) == "" || strings.TrimSpace(input.Runtime) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "nodeId and runtime are required")
		return
	}
	preflight, err := a.store.PreflightProfileActivation(r.Context(), chi.URLParam(r, "id"), input.NodeID, input.Runtime)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if !preflight.OK {
		writeActivationConflict(w, r, preflight)
		return
	}
	writeJSON(w, http.StatusOK, preflight)
}

func (a *API) activateProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		profileTargetRequest
		ConfirmSecrets bool `json:"confirmSecrets"`
	}
	if err := decodeJSON(w, r, &input, 32<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.NodeID) == "" || strings.TrimSpace(input.Runtime) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "nodeId and runtime are required")
		return
	}
	profileID := chi.URLParam(r, "id")
	principal := principalFrom(r.Context())
	job, err := a.store.ActivateProfile(r.Context(), profileID, input.NodeID, input.Runtime, principal.ID, input.ConfirmSecrets)
	if err != nil {
		var preflightErr *store.ActivationPreflightError
		if errors.As(err, &preflightErr) {
			writeActivationConflict(w, r, preflightErr.Preflight)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	var details struct {
		PreviousProfileID string                  `json:"previousProfileId"`
		Skipped           []store.ActivationIssue `json:"skipped"`
		RemoteSecretKeys  []string                `json:"remoteSecretKeys"`
	}
	_ = json.Unmarshal(job.Payload, &details)
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "profile_activate", ResourceType: "toolhub_profile", ResourceID: profileID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{
		"nodeId": input.NodeID, "runtime": input.Runtime, "previousProfileId": details.PreviousProfileID,
		"skipped": details.Skipped, "remoteSecretKeys": details.RemoteSecretKeys,
	}})
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) getTargetView(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.TargetView(r.Context(), chi.URLParam(r, "nodeId"), chi.URLParam(r, "runtime"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, raw)
}

func (a *API) deactivateTarget(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	runtimeKind := chi.URLParam(r, "runtime")
	principal := principalFrom(r.Context())
	if err := a.store.DeactivateProfile(r.Context(), nodeID, runtimeKind, principal.ID); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "profile_deactivate", ResourceType: "target", ResourceID: nodeID + ":" + runtimeKind, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"nodeId": nodeID, "runtime": runtimeKind}})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleProfileMutationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrStateConflict) || errors.Is(err, store.ErrToolHubProfileNameTaken) {
		a.handleStoreError(w, r, err)
		return
	}
	if errors.Is(err, store.ErrInvalidToolHubProfile) {
		message := strings.TrimPrefix(err.Error(), store.ErrInvalidToolHubProfile.Error()+": ")
		writeError(w, r, http.StatusBadRequest, "invalid_profile", message)
		return
	}
	a.handleStoreError(w, r, err)
}

func writeActivationConflict(w http.ResponseWriter, r *http.Request, preflight store.ActivationPreflight) {
	code := "profile_activation_preflight_failed"
	message := "Profile activation preflight failed"
	if len(preflight.Errors) > 0 {
		code = preflight.Errors[0].Code
		message = activationIssueMessage(preflight.Errors[0])
	}
	writeErrorDetails(w, r, http.StatusConflict, code, message, map[string]any{
		"issues": preflight.Errors, "skipped": preflight.Skipped, "nodeName": preflight.NodeName, "secretKeys": preflight.RemoteSecretKeys,
	})
}

func activationIssueMessage(issue store.ActivationIssue) string {
	messages := map[string]string{
		"node_not_found":                      "The target node was not found",
		"node_offline":                        "The target node is offline and has no SSH fallback",
		"runtime_unavailable":                 "The runtime is not available on this node",
		"skill_not_approved":                  "The Profile contains unavailable Skills",
		"mcp_server_unavailable":              "The Profile contains unavailable MCP servers",
		"managed_mcp_profile_missing":         "The fixed MCP delivery profile is unavailable",
		"remote_secret_confirmation_required": "Confirm the named secret keys before remote delivery",
	}
	if message := messages[issue.Code]; message != "" {
		return message
	}
	return "Profile activation preflight failed"
}
