package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
)

func (a *API) listSharedSources(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListSharedSources(r.Context())
	a.serveList(w, r, raw, err)
}

func (a *API) getSharedSource(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.GetSharedSource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, raw)
}

func (a *API) syncSharedSource(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Scopes []string `json:"scopes"`
		DryRun bool     `json:"dryRun"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var err error
	input.Scopes, err = normalizeSharedSyncScopes(input.Scopes)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	target, err := a.store.SharedSyncTarget(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !input.DryRun && target.Mode != "managed" {
		writeError(w, r, http.StatusConflict, "shared_source_observed", "This shared source is observed-only; enable managed mode in the local Agent configuration before writing")
		return
	}
	principal := principalFrom(r.Context())
	job, err := a.store.EnqueueJob(r.Context(), "shared_sync", map[string]any{
		"sourceIds": []string{target.SourceID},
		"nodeIds":   []string{target.NodeID},
		"scopes":    input.Scopes,
	}, input.DryRun, principal.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "shared_sync_queued", ResourceType: "shared_source", ResourceID: target.SourceID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"nodeId": target.NodeID, "scopes": input.Scopes, "dryRun": input.DryRun}})
	writeJSON(w, http.StatusAccepted, job)
}

func normalizeSharedSyncScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return []string{"skills", "mcp"}, nil
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope != "skills" && scope != "mcp" {
			return nil, errors.New("Shared sync scopes must contain only skills or mcp")
		}
		seen[scope] = true
	}
	result := make([]string, 0, len(seen))
	for _, scope := range []string{"skills", "mcp"} {
		if seen[scope] {
			result = append(result, scope)
		}
	}
	return result, nil
}
