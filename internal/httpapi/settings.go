package httpapi

import (
	"net/http"
	"strings"
)

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	policies, err := a.store.JSONObject(r.Context(), `SELECT
		(SELECT to_jsonb(u) FROM (SELECT schedule,timezone,enabled,require_approval AS "requireApproval",settings FROM update_policies WHERE scope_type='global' AND scope_id='') u) AS "updatePolicy",
		(SELECT to_jsonb(s) FROM (SELECT schedule,timezone,enabled,settings FROM sync_policies WHERE scope_type='global' AND scope_id='') s) AS "syncPolicy"`)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"publicUrl": a.config.PublicURL, "listenPort": 18480, "timezone": a.config.Timezone.String(), "localNodeName": a.config.LocalNodeName, "inventoryIntervalHours": 6, "policies": policies, "marketApiKeyConfigured": a.config.SkillsMPAPIKey != "", "xiapingApiKeyConfigured": a.config.XiapingAPIKey != ""})
}

func (a *API) updateSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		UpdateSchedule string `json:"updateSchedule"`
		SyncSchedule   string `json:"syncSchedule"`
		Timezone       string `json:"timezone"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.Timezone) == "" {
		input.Timezone = a.config.Timezone.String()
	}
	if input.UpdateSchedule != "" {
		if _, err := a.store.Pool().Exec(r.Context(), "UPDATE update_policies SET schedule=$1,timezone=$2 WHERE scope_type='global' AND scope_id=''", input.UpdateSchedule, input.Timezone); err != nil {
			writeError(w, r, http.StatusBadRequest, "settings_update_failed", err.Error())
			return
		}
	}
	if input.SyncSchedule != "" {
		if _, err := a.store.Pool().Exec(r.Context(), "UPDATE sync_policies SET schedule=$1,timezone=$2 WHERE scope_type='global' AND scope_id=''", input.SyncSchedule, input.Timezone); err != nil {
			writeError(w, r, http.StatusBadRequest, "settings_update_failed", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
}

func (a *API) listAIProviders(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListAIProviders(r.Context())
	a.serveList(w, r, raw, err)
}

func (a *API) createAIProvider(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string `json:"name"`
		BaseURL   string `json:"baseUrl"`
		Model     string `json:"model"`
		APIKey    string `json:"apiKey"`
		IsDefault bool   `json:"isDefault"`
	}
	if err := decodeJSON(w, r, &input, 128<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id, err := a.store.CreateAIProvider(r.Context(), input.Name, input.BaseURL, input.Model, input.APIKey, principalFrom(r.Context()).ID, input.IsDefault)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "ai_provider_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}
