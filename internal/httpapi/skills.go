package httpapi

import (
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/skills"
	"github.com/Junhao2314/toolhub/internal/store"
)

func (a *API) getSkill(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.GetSkill(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, raw)
}

func (a *API) uploadSkill(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, skills.DefaultLimits.MaxArchiveBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_upload", "A multipart ZIP upload is required")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" {
		writeError(w, r, http.StatusBadRequest, "invalid_upload", "The first multipart field must be file")
		return
	}
	body, err := io.ReadAll(io.LimitReader(part, skills.DefaultLimits.MaxArchiveBytes+1))
	if err != nil || int64(len(body)) > skills.DefaultLimits.MaxArchiveBytes {
		writeError(w, r, http.StatusRequestEntityTooLarge, "archive_too_large", "The archive exceeds the upload limit")
		return
	}
	if extra, _ := reader.NextPart(); extra != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_upload", "Only one file may be uploaded")
		return
	}
	pkg, err := skills.ScanZIP(body, skills.DefaultLimits)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "skill_scan_failed", err.Error())
		return
	}
	principal := principalFrom(r.Context())
	result, err := a.store.ImportSkill(r.Context(), store.SourceInput{Kind: "upload", Name: part.FileName()}, pkg, map[string]any{"uploadedFilename": part.FileName()}, principal.ID)
	if err != nil {
		writeError(w, r, http.StatusConflict, "skill_import_failed", err.Error())
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "import", ResourceType: "skill", ResourceID: result.SkillID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"sha256": result.SHA256, "riskLevel": result.RiskLevel}})
	writeJSON(w, http.StatusCreated, map[string]any{"import": result, "package": pkg})
}

func (a *API) importSkill(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Kind         string `json:"kind"`
		Name         string `json:"name"`
		URL          string `json:"url"`
		GitHubURL    string `json:"githubUrl"`
		Subdirectory string `json:"subdirectory"`
		Commit       string `json:"commit"`
	}
	if err := decodeJSON(w, r, &input, 256<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if input.Kind != "git" && input.Kind != "skillsmp" && input.Kind != "openai" {
		writeError(w, r, http.StatusBadRequest, "invalid_source", "Source kind must be git, skillsmp, or openai")
		return
	}
	remote := strings.TrimSpace(input.URL)
	if input.Kind == "skillsmp" {
		remote = strings.TrimSpace(input.GitHubURL)
	}
	principal := principalFrom(r.Context())
	job, err := a.store.EnqueueJob(r.Context(), "skill_import", map[string]any{"kind": input.Kind, "name": input.Name, "url": remote, "subdirectory": input.Subdirectory, "commit": input.Commit}, false, principal.ID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) reviewSkill(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Decision string `json:"decision"`
	}
	if err := decodeJSON(w, r, &input, 32<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	principal := principalFrom(r.Context())
	if err := a.store.ReviewSkill(r.Context(), id, input.Decision, principal.ID); err != nil {
		writeError(w, r, http.StatusBadRequest, "review_failed", err.Error())
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "review", ResourceType: "skill", ResourceID: id, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"decision": input.Decision}})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "reviewStatus": input.Decision})
}

func (a *API) setSkillTargets(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Targets []store.DeploymentTarget `json:"targets"`
		Sync    bool                     `json:"sync"`
		DryRun  bool                     `json:"dryRun"`
	}
	if err := decodeJSON(w, r, &input, 512<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	principal := principalFrom(r.Context())
	job, err := a.store.SetSkillTargets(r.Context(), chi.URLParam(r, "id"), principal.ID, input.Targets, input.DryRun)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "target_update_failed", err.Error())
		return
	}
	_ = input.Sync
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) archiveSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := a.store.ArchiveSkill(r.Context(), id); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	principal := principalFrom(r.Context())
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "archive", ResourceType: "skill", ResourceID: id, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"retentionDays": 30}})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) checkUpdates(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SkillIDs []string `json:"skillIds"`
		DryRun   bool     `json:"dryRun"`
	}
	if err := decodeJSON(w, r, &input, 256<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	job, err := a.store.EnqueueJob(r.Context(), "update_check", input, input.DryRun, principalFrom(r.Context()).ID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) approveUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	principal := principalFrom(r.Context())
	job, err := a.store.ApproveUpdate(r.Context(), id, principal.ID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "update_approval_failed", err.Error())
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "approve", ResourceType: "update", ResourceID: id, Outcome: "success", IPAddress: clientIP(r)})
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "approved", "job": job})
}

func (a *API) syncNow(w http.ResponseWriter, r *http.Request) {
	var input struct {
		NodeIDs  []string `json:"nodeIds"`
		SkillIDs []string `json:"skillIds"`
		DryRun   bool     `json:"dryRun"`
	}
	if err := decodeJSON(w, r, &input, 256<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	job, err := a.store.EnqueueJob(r.Context(), "sync", input, input.DryRun, principalFrom(r.Context()).ID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) rollbackDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := a.store.RollbackDeployment(r.Context(), id, principalFrom(r.Context()).ID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
