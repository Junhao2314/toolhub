package httpapi

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/skills"
	"github.com/Junhao2314/toolhub/internal/store"
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
		a.handleStoreError(w, r, err)
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
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": string(value)})
}

func (a *API) agentDiscoveryDescriptors(w http.ResponseWriter, r *http.Request) {
	if !a.verifyAgentRequest(r) {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Invalid agent credentials")
		return
	}
	var input domain.AgentInventory
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_discovery", "Invalid Agent discovery descriptor")
		return
	}
	nodeID := strings.TrimSpace(r.Header.Get("X-ToolHub-Node-ID"))
	requests, err := a.store.ProcessAgentInventory(r.Context(), nodeID, input, true)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "discovery_rejected", "Agent discovery descriptor was rejected")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"captureRequests": requests})
}

func (a *API) agentDiscoveryCapture(w http.ResponseWriter, r *http.Request) {
	if !a.verifyAgentRequest(r) {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Invalid agent credentials")
		return
	}
	var input store.MCPSecretCapture
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_capture", "Invalid MCP secret capture")
		return
	}
	nodeID := strings.TrimSpace(r.Header.Get("X-ToolHub-Node-ID"))
	result, err := a.store.CaptureRuntimeMCP(r.Context(), nodeID, input)
	if err != nil {
		writeError(w, r, http.StatusForbidden, "capture_rejected", "MCP secret capture was rejected")
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (a *API) agentSkillAdoptionUpload(w http.ResponseWriter, r *http.Request) {
	if !a.verifyAgentRequest(r) {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Invalid agent credentials")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, skills.DefaultLimits.MaxArchiveBytes+1)
	body, err := io.ReadAll(io.LimitReader(r.Body, skills.DefaultLimits.MaxArchiveBytes+1))
	if err != nil || int64(len(body)) > skills.DefaultLimits.MaxArchiveBytes {
		writeError(w, r, http.StatusRequestEntityTooLarge, "archive_too_large", "The Skill snapshot exceeds the upload limit")
		return
	}
	pkg, err := skills.ScanZIP(body, skills.DefaultLimits)
	if err != nil || r.Header.Get("X-Content-SHA256") != pkg.SHA256 {
		writeError(w, r, http.StatusUnprocessableEntity, "skill_scan_failed", "The Skill snapshot failed validation")
		return
	}
	nodeID := strings.TrimSpace(r.Header.Get("X-ToolHub-Node-ID"))
	result, err := a.store.ImportDiscoveredSkill(r.Context(), nodeID, chi.URLParam(r, "id"), strings.TrimSpace(r.Header.Get("X-ToolHub-Task-ID")), pkg)
	if err != nil {
		writeError(w, r, http.StatusConflict, "skill_adoption_failed", "The Skill snapshot could not be imported")
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "runtime_skill_snapshot", ResourceType: "skill_discovery", ResourceID: chi.URLParam(r, "id"), Outcome: "success", Metadata: map[string]any{"nodeId": nodeID, "sha256": result.SHA256, "riskLevel": result.RiskLevel}})
	writeJSON(w, http.StatusCreated, result)
}

func (a *API) verifyAgentRequest(r *http.Request) bool {
	nodeID := strings.TrimSpace(r.Header.Get("X-ToolHub-Node-ID"))
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	return nodeID != "" && token != "" && a.store.VerifyAgent(r.Context(), nodeID, token) == nil
}
