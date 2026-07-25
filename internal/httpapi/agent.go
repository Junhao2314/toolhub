package httpapi

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (a *API) enrollAgent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token        string `json:"token"`
		Hostname     string `json:"hostname"`
		Platform     string `json:"platform"`
		Architecture string `json:"architecture"`
		TailscaleIP  string `json:"tailscaleIp"`
		PublicKey    string `json:"publicKey"`
	}
	if err := decodeJSON(w, r, &input, 128<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Invalid enrollment request")
		return
	}
	publicKey, err := base64.StdEncoding.DecodeString(input.PublicKey)
	if err != nil || len(publicKey) > 4096 {
		writeError(w, r, http.StatusBadRequest, "invalid_public_key", "Agent public key is invalid")
		return
	}
	result, err := a.store.EnrollAgent(r.Context(), input.Token, input.Hostname, input.Platform, input.Architecture, input.TailscaleIP, publicKey)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "enrollment_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (a *API) agentArtifact(w http.ResponseWriter, r *http.Request) {
	if !a.verifyAgentRequest(r) {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Invalid agent credentials")
		return
	}
	content, hash, err := a.store.Artifact(r.Context(), chi.URLParam(r, "versionID"))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("ETag", `"`+hash+`"`)
	w.Header().Set("X-Content-SHA256", hash)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (a *API) agentSecret(w http.ResponseWriter, r *http.Request) {
	if !a.verifyAgentRequest(r) {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Invalid agent credentials")
		return
	}
	nodeID := strings.TrimSpace(r.Header.Get("X-ToolHub-Node-ID"))
	value, err := a.store.AgentSecretValue(r.Context(), nodeID, chi.URLParam(r, "secretID"))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": string(value)})
}

func (a *API) verifyAgentRequest(r *http.Request) bool {
	nodeID := strings.TrimSpace(r.Header.Get("X-ToolHub-Node-ID"))
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	return nodeID != "" && token != "" && a.store.VerifyAgent(r.Context(), nodeID, token) == nil
}
