package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/ai"
	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/market"
	"github.com/Junhao2314/toolhub/internal/profilebundle"
	"github.com/Junhao2314/toolhub/internal/skills"
	"github.com/Junhao2314/toolhub/internal/store"
)

func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.Overview(r.Context())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *API) listSkills(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.ListSkills(r.Context())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeItems(w, value)
}
func (a *API) listMCPServers(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.ListMCPServers(r.Context())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeItems(w, value)
}
func (a *API) listProfiles(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.ListProfiles(r.Context(), r.URL.Query().Get("includeArchived") == "true")
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeItems(w, value)
}
func (a *API) listNodes(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.ListNodes(r.Context())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeItems(w, value)
}
func (a *API) listTargets(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.ListTargets(r.Context())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeItems(w, value)
}
func (a *API) listOperations(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.ListOperations(r.Context(), 100)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeItems(w, value)
}
func (a *API) getOperation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "operationID")
	if uuid.Validate(id) != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "Operation was not found")
		return
	}
	value, err := a.store.OperationDetail(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.ListAudit(r.Context(), 100)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeItems(w, value)
}

func (a *API) uploadSkill(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(skills.DefaultLimits.MaxArchiveBytes); err != nil {
		writeError(w, r, 400, "invalid_archive", "Skill archive is invalid or too large")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, 400, "invalid_archive", "ZIP file is required")
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, skills.DefaultLimits.MaxArchiveBytes+1))
	if err != nil || int64(len(body)) > skills.DefaultLimits.MaxArchiveBytes {
		writeError(w, r, 400, "invalid_archive", "Skill archive is too large")
		return
	}
	pkg, err := skills.ScanZIP(body, skills.DefaultLimits)
	if err != nil {
		writeError(w, r, 400, "unsafe_archive", err.Error())
		return
	}
	skill, created, err := a.store.ImportSkill(r.Context(), store.SourceInput{Kind: "zip", Name: header.Filename, Metadata: map[string]any{}}, pkg, map[string]any{"filename": header.Filename})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "skill_import", ResourceType: "skill", ResourceID: skill.ID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"source": "zip", "sha256": pkg.SHA256}})
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, skill)
}

func (a *API) importSkill(w http.ResponseWriter, r *http.Request) {
	var input store.SourceInput
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	op, err := a.store.CreateOperation(r.Context(), store.CreateOperationInput{Kind: "skill_import", IdempotencyKey: key, Request: input, Metadata: map[string]any{"source": input}, TargetIDs: nil})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (a *API) createMCPServer(w http.ResponseWriter, r *http.Request) { a.saveMCPServer(w, r, "") }
func (a *API) updateMCPServer(w http.ResponseWriter, r *http.Request) {
	a.saveMCPServer(w, r, chi.URLParam(r, "serverID"))
}
func (a *API) saveMCPServer(w http.ResponseWriter, r *http.Request, id string) {
	var input store.MCPInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	server, err := a.store.SaveMCPServer(r.Context(), id, input)
	if err != nil {
		writeError(w, r, 400, "invalid_mcp_server", err.Error())
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "mcp_server_save", ResourceType: "mcp_server", ResourceID: server.ID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"name": server.Name, "secretKeys": append(server.EnvKeys, server.HeaderKeys...)}})
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, server)
}
func (a *API) deleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteMCPServer(r.Context(), chi.URLParam(r, "serverID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) createProfile(w http.ResponseWriter, r *http.Request) { a.saveProfile(w, r, "") }
func (a *API) updateProfile(w http.ResponseWriter, r *http.Request) {
	a.saveProfile(w, r, chi.URLParam(r, "profileID"))
}
func (a *API) saveProfile(w http.ResponseWriter, r *http.Request, id string) {
	var input store.ProfileInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	profile, err := a.store.SaveProfile(r.Context(), id, input)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, profile)
}
func (a *API) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteProfile(r.Context(), chi.URLParam(r, "profileID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := a.store.Profile(r.Context(), chi.URLParam(r, "profileID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (a *API) profileHistory(w http.ResponseWriter, r *http.Request) {
	history, err := a.store.ProfileHistory(r.Context(), chi.URLParam(r, "profileID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": history})
}

func (a *API) profileSecretBindings(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.PendingSecretBindings(r.Context(), chi.URLParam(r, "profileID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeItems(w, value)
}

func (a *API) refreshProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision int64 `json:"revision"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	profile, err := a.store.RefreshProfile(r.Context(), chi.URLParam(r, "profileID"), input.Revision)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (a *API) cloneProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	profile, err := a.store.CloneProfile(r.Context(), chi.URLParam(r, "profileID"), input.Name)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, profile)
}

func (a *API) archiveProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision int64 `json:"revision"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	if err := a.store.ArchiveProfile(r.Context(), chi.URLParam(r, "profileID"), input.Revision); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) restoreProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision int64 `json:"revision"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	profile, err := a.store.RestoreArchivedProfile(r.Context(), chi.URLParam(r, "profileID"), input.Revision)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (a *API) purgeProfile(w http.ResponseWriter, r *http.Request) {
	if err := a.store.PurgeProfile(r.Context(), chi.URLParam(r, "profileID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) completeProfileBindings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision int64             `json:"revision"`
		Values   map[string]string `json:"values"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	profile, err := a.store.CompletePendingSecretBindings(r.Context(), chi.URLParam(r, "profileID"), input.Revision, input.Values)
	for key := range input.Values {
		input.Values[key] = ""
	}
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (a *API) exportProfileBundle(w http.ResponseWriter, r *http.Request) {
	a.streamProfileBundle(w, r, false)
}

func (a *API) exportSecretProfileBundle(w http.ResponseWriter, r *http.Request) {
	var input struct {
		OriginLabel     string `json:"originLabel"`
		CurrentPassword string `json:"currentPassword"`
	}
	if err := decodeJSON(w, r, &input, 16<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	if err := a.store.VerifyCurrentPassword(r.Context(), input.CurrentPassword); err != nil {
		if errors.Is(err, store.ErrInvalidCurrentPassword) {
			writeError(w, r, http.StatusForbidden, "current_password_invalid", "Current password is incorrect")
		} else {
			a.handleStoreError(w, r, err)
		}
		return
	}
	a.streamProfileBundleWithLabel(w, r, input.OriginLabel, true)
}

func (a *API) streamProfileBundle(w http.ResponseWriter, r *http.Request, includeSecrets bool) {
	a.streamProfileBundleWithLabel(w, r, "ToolHub", includeSecrets)
}

func (a *API) streamProfileBundleWithLabel(w http.ResponseWriter, r *http.Request, label string, includeSecrets bool) {
	body, err := a.store.ExportProfileBundle(r.Context(), chi.URLParam(r, "profileID"), label, includeSecrets)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if includeSecrets {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Content-Disposition", "attachment; filename=profile.toolhub-profile-secrets")
	} else {
		w.Header().Set("Content-Disposition", "attachment; filename=profile.toolhub-profile")
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func readBundleUpload(r *http.Request) ([]byte, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/") {
		if err := r.ParseMultipartForm(profilebundle.MaxBundleBytes); err != nil {
			return nil, err
		}
		file, _, err := r.FormFile("bundle")
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return io.ReadAll(io.LimitReader(file, profilebundle.MaxBundleBytes+1))
	}
	return io.ReadAll(io.LimitReader(r.Body, profilebundle.MaxBundleBytes+1))
}

func (a *API) previewProfileBundle(w http.ResponseWriter, r *http.Request) {
	body, err := readBundleUpload(r)
	if err != nil || len(body) == 0 || len(body) > profilebundle.MaxBundleBytes {
		writeError(w, r, 400, "invalid_bundle", "Profile Bundle upload is invalid or too large")
		return
	}
	preview, err := a.store.PreviewProfileBundle(r.Context(), body, 5*time.Minute)
	if err != nil {
		writeError(w, r, 400, "invalid_bundle", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (a *API) importProfileBundle(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		writeError(w, r, 400, "invalid_request", "Bundle import requires multipart form data")
		return
	}
	if err := r.ParseMultipartForm(profilebundle.MaxBundleBytes + 1); err != nil {
		writeError(w, r, 400, "invalid_bundle", err.Error())
		return
	}
	body, err := readBundleUpload(r)
	if err != nil || len(body) == 0 || len(body) > profilebundle.MaxBundleBytes {
		writeError(w, r, 400, "invalid_bundle", "Profile Bundle upload is invalid or too large")
		return
	}
	input := store.BundleImportInput{ConfirmationToken: strings.TrimSpace(r.FormValue("confirmationToken")), Name: strings.TrimSpace(r.FormValue("name")), ConfirmDuplicate: r.FormValue("confirmDuplicate") == "true", ImportAsNew: r.FormValue("importAsNew") == "true"}
	profile, err := a.store.ImportProfileBundle(r.Context(), body, input)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, profile)
}

func (a *API) getTarget(w http.ResponseWriter, r *http.Request) {
	target, err := a.store.Target(r.Context(), chi.URLParam(r, "targetID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	inventory, revision, err := a.store.RuntimeSnapshot(r.Context(), target.ID)
	if errors.Is(err, store.ErrNotFound) {
		inventory = []byte(`{}`)
		revision = ""
	} else if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	var desired any
	if snapshot, manifest, desiredErr := a.store.ActiveDesiredManifest(r.Context(), target.ID); desiredErr == nil {
		desired = map[string]any{"snapshot": snapshot, "manifest": manifest}
	} else if !errors.Is(desiredErr, store.ErrNotFound) {
		a.handleStoreError(w, r, desiredErr)
		return
	}
	writeJSON(w, 200, map[string]any{"target": target, "inventory": inventory, "targetRevision": revision, "desired": desired})
}

func (a *API) scanTarget(w http.ResponseWriter, r *http.Request) {
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	targetID := chi.URLParam(r, "targetID")
	if _, err := a.store.Target(r.Context(), targetID); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	op, err := a.store.CreateOperation(r.Context(), store.CreateOperationInput{Kind: "scan", IdempotencyKey: key, Request: map[string]any{"targetId": targetID}, TargetIDs: []string{targetID}})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, 202, op)
}

func (a *API) importLocalSkill(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name             string `json:"name"`
		ExpectedRevision string `json:"expectedRevision"`
		ContentHash      string `json:"contentHash"`
	}
	if err := decodeJSON(w, r, &input, 16<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if bridgeprotocol.IsProtectedSkillEntry(input.Name) || !bridgeprotocol.IsSHA256(input.ExpectedRevision) || !bridgeprotocol.IsSHA256(input.ContentHash) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A scanned, importable local Skill is required")
		return
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	targetID := chi.URLParam(r, "targetID")
	target, err := a.store.Target(r.Context(), targetID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if target.NodeKind != bridgeprotocol.NodeKindLocal || (target.Runtime != domain.RuntimeClaude && target.Runtime != domain.RuntimeCodex) {
		writeError(w, r, http.StatusBadRequest, "invalid_target", "Local Skill intake is available only from local Claude or Codex")
		return
	}
	raw, revision, err := a.store.RuntimeSnapshot(r.Context(), targetID)
	if err != nil || revision != input.ExpectedRevision {
		writeError(w, r, http.StatusConflict, "revision_conflict", "Scan the target again before importing this Skill")
		return
	}
	var inventory struct {
		Members []bridgeprotocol.InventoryMember `json:"members"`
	}
	if json.Unmarshal(raw, &inventory) != nil {
		writeError(w, r, http.StatusConflict, "revision_conflict", "Stored target inventory is invalid; scan again")
		return
	}
	matched := false
	for _, member := range inventory.Members {
		if member.Kind == "skill" && member.Name == input.Name && member.ContentHash == input.ContentHash && !member.Protected {
			matched = true
			break
		}
	}
	if !matched {
		writeError(w, r, http.StatusConflict, "revision_conflict", "Local Skill no longer matches the scanned inventory")
		return
	}
	request := map[string]any{"targetRevision": input.ExpectedRevision, "name": input.Name, "contentHash": input.ContentHash}
	op, err := a.store.CreateOperation(r.Context(), store.CreateOperationInput{Kind: "skill_import", SourceID: targetID, IdempotencyKey: key, Request: request, Metadata: map[string]any{"source": "local", "targetId": targetID, "name": input.Name}, TargetIDs: []string{targetID}, TargetRequests: map[string]any{targetID: request}})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (a *API) preflightLocalMCPImport(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := decodeJSON(w, r, &input, 4<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	targetID := chi.URLParam(r, "targetID")
	target, err := a.store.Target(r.Context(), targetID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if target.NodeKind != bridgeprotocol.NodeKindLocal || (target.Runtime != domain.RuntimeClaude && target.Runtime != domain.RuntimeCodex) {
		writeError(w, r, http.StatusBadRequest, "invalid_target", "Local MCP capture is available only from local Claude or Codex user scope")
		return
	}
	preview, err := a.bridge.PreviewLocalMCP(r.Context(), bridgeprotocol.LocalMCPPreviewRequest{Target: browserBridgeTarget(target)})
	if err != nil {
		writeError(w, r, http.StatusConflict, "local_mcp_preview_failed", err.Error())
		return
	}
	type confirmedPreview struct {
		bridgeprotocol.LocalMCPServerPreview
		ConfirmationToken string    `json:"confirmationToken"`
		ExpiresAt         time.Time `json:"expiresAt"`
	}
	items := make([]confirmedPreview, 0, len(preview.Items))
	for _, item := range preview.Items {
		token, expires, err := a.store.CreateLocalMCPImportConfirmation(r.Context(), targetID, preview.TargetRevision, item, 5*time.Minute)
		if err != nil {
			a.handleStoreError(w, r, err)
			return
		}
		items = append(items, confirmedPreview{LocalMCPServerPreview: item, ConfirmationToken: token, ExpiresAt: expires})
	}
	writeJSON(w, http.StatusOK, map[string]any{"targetRevision": preview.TargetRevision, "items": items})
}

func (a *API) importLocalMCP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ConfirmationToken string `json:"confirmationToken"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	op, err := a.store.CreateLocalMCPImportOperation(r.Context(), input.ConfirmationToken, key)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, r, http.StatusConflict, "confirmation_conflict", "Local MCP capture confirmation expired, was used, or no longer matches")
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func browserBridgeTarget(target domain.Target) bridgeprotocol.Target {
	return bridgeprotocol.Target{
		ID:              target.ID,
		NodeID:          target.NodeID,
		NodeKind:        target.NodeKind,
		SaltMinionID:    target.SaltMinionID,
		Runtime:         target.Runtime,
		ManagedUsername: target.ManagedUsername,
	}
}

func (a *API) refreshNodes(w http.ResponseWriter, r *http.Request) {
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	op, err := a.store.CreateOperation(r.Context(), store.CreateOperationInput{Kind: "refresh", IdempotencyKey: key, Request: map[string]any{"requested": true}, Metadata: map[string]any{"requested": true}})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "node_refresh_request", ResourceType: "node", Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"operationId": op.ID}})
	writeJSON(w, http.StatusAccepted, op)
}

func (a *API) updateNode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ManagedUsername string `json:"managedUsername"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	input.ManagedUsername = strings.TrimSpace(input.ManagedUsername)
	if input.ManagedUsername != "" {
		if err := bridgeprotocol.ValidateManagedUsername(input.ManagedUsername); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_managed_username", err.Error())
			return
		}
	}
	nodeID := chi.URLParam(r, "nodeID")
	if err := a.store.UpdateNodeManagedUsername(r.Context(), nodeID, input.ManagedUsername); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "node_managed_username_update", ResourceType: "node", ResourceID: nodeID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"overrideConfigured": input.ManagedUsername != ""}})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) profilePreflight(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TargetIDs []string `json:"targetIds"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	profileID := chi.URLParam(r, "profileID")
	if len(input.TargetIDs) == 0 || len(input.TargetIDs) > 100 {
		writeError(w, r, 400, "invalid_request", "Select 1-100 targets")
		return
	}
	profile, err := a.store.Profile(r.Context(), profileID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if profile.ClientKind != bridgeprotocol.RuntimeClaude && profile.ClientKind != bridgeprotocol.RuntimeCodex {
		writeError(w, r, http.StatusConflict, "profile_client_unsupported", "Profile client does not support native launch")
		return
	}
	type resolvedTarget struct {
		id       string
		manifest bridgeprotocol.DesiredManifest
	}
	resolved := make([]resolvedTarget, 0, len(input.TargetIDs))
	seenTargets := make(map[string]struct{}, len(input.TargetIDs))
	var nativeRequest *bridgeprotocol.NativeClientInspectionRequest
	for _, targetID := range input.TargetIDs {
		if uuid.Validate(targetID) != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "Target IDs must be unique UUIDs")
			return
		}
		if _, exists := seenTargets[targetID]; exists {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "Target IDs must be unique UUIDs")
			return
		}
		seenTargets[targetID] = struct{}{}
		manifest, err := a.store.ResolveProfileManifest(r.Context(), profileID, targetID)
		if err != nil {
			a.handleStoreError(w, r, err)
			return
		}
		if manifest.Target.NodeKind == bridgeprotocol.NodeKindLocal && manifest.Target.Runtime == profile.ClientKind {
			if nativeRequest != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "Select one local client target")
				return
			}
			nativeRequest = &bridgeprotocol.NativeClientInspectionRequest{ManagedUsername: manifest.Target.ManagedUsername, ClientKind: profile.ClientKind}
		}
		resolved = append(resolved, resolvedTarget{id: targetID, manifest: manifest})
	}
	if nativeRequest == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Select the Profile local client target")
		return
	}
	inspection, err := a.bridge.InspectNativeClient(r.Context(), *nativeRequest)
	if err != nil {
		writeError(w, r, http.StatusConflict, "native_client_inspection_failed", "Native client inspection could not be completed")
		return
	}
	if reason := nativeClientPreflightReason(profile.ClientKind, inspection); reason != "" {
		writeError(w, r, http.StatusConflict, reason, "Native client does not satisfy Profile requirements")
		return
	}
	type item struct {
		TargetID  string                           `json:"targetId"`
		Token     string                           `json:"confirmationToken"`
		ExpiresAt time.Time                        `json:"expiresAt"`
		Result    bridgeprotocol.PreflightResponse `json:"result"`
	}
	items := make([]item, 0, len(input.TargetIDs))
	for _, target := range resolved {
		result, err := a.bridge.Preflight(r.Context(), "preflight-"+target.id+"-"+strconv.FormatInt(time.Now().UnixNano(), 10), bridgeprotocol.PreflightRequest{Target: target.manifest.Target, Manifest: target.manifest})
		if err != nil {
			writeError(w, r, 409, "preflight_failed", err.Error())
			return
		}
		if !validBrowserPreflightResponse(target.manifest, result) {
			writeError(w, r, http.StatusBadGateway, "relay_response_invalid", "Profile preflight response is invalid")
			return
		}
		token, expires, err := a.store.CreatePreflightConfirmation(r.Context(), profileID, target.id, result.TargetRevision, target.manifest, result.Diff, 5*time.Minute)
		if err != nil {
			a.handleStoreError(w, r, err)
			return
		}
		items = append(items, item{TargetID: target.id, Token: token, ExpiresAt: expires, Result: result})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func nativeClientPreflightReason(expectedKind string, inspection bridgeprotocol.NativeClientInspectionResponse) string {
	if !validNativeClientInspection(inspection, expectedKind) {
		return "native_client_inspection_invalid"
	}
	if inspection.Supported {
		return ""
	}
	return inspection.ErrorCode
}

func (a *API) applyProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ConfirmationTokens []string `json:"confirmationTokens"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	profileID := chi.URLParam(r, "profileID")
	if len(input.ConfirmationTokens) == 0 || len(input.ConfirmationTokens) > 100 {
		writeError(w, r, 400, "invalid_request", "At least one confirmation token is required")
		return
	}
	op, err := a.store.CreateProfileApplyOperation(r.Context(), profileID, input.ConfirmationTokens, key)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, r, 409, "preflight_conflict", "Preflight expired, was already used, or no longer matches the Profile")
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, 202, op)
}

func (a *API) importLocalSkillBatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TargetRevision string `json:"targetRevision"`
		ConfirmUpdates bool   `json:"confirmUpdates"`
		Items          []struct {
			ID          string `json:"id"`
			Slug        string `json:"slug"`
			ContentHash string `json:"contentHash"`
		} `json:"items"`
	}
	if err := decodeJSON(w, r, &input, 256<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(input.Items) == 0 || len(input.Items) > 50 || !bridgeprotocol.IsSHA256(input.TargetRevision) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "up to 50 items and a current inventory revision are required")
		return
	}
	targetID := chi.URLParam(r, "targetID")
	target, err := a.store.Target(r.Context(), targetID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if target.NodeKind != bridgeprotocol.NodeKindLocal || target.Runtime != domain.RuntimeHermes {
		writeError(w, r, http.StatusBadRequest, bridgeprotocol.ErrProtectedScope, "batch Skill intake requires local Hermes")
		return
	}
	inventoryBody, revision, err := a.store.RuntimeSnapshot(r.Context(), targetID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if revision != input.TargetRevision {
		writeError(w, r, http.StatusConflict, bridgeprotocol.ErrRevisionConflict, "Hermes inventory changed; scan again")
		return
	}
	var inventory struct {
		Members []bridgeprotocol.InventoryMember `json:"members"`
	}
	if err := json.Unmarshal(inventoryBody, &inventory); err != nil {
		writeError(w, r, http.StatusConflict, bridgeprotocol.ErrRevisionConflict, "Hermes inventory is invalid; scan again")
		return
	}
	byID := make(map[string]bridgeprotocol.InventoryMember, len(inventory.Members))
	for _, member := range inventory.Members {
		byID[member.ID] = member
	}
	seenIDs, seenSlugs := map[string]bool{}, map[string]bool{}
	for _, item := range input.Items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Slug) == "" || !bridgeprotocol.IsSHA256(item.ContentHash) || seenIDs[item.ID] || seenSlugs[item.Slug] {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "batch IDs, slugs, and hashes must be unique and valid")
			return
		}
		seenIDs[item.ID], seenSlugs[item.Slug] = true, true
		member, ok := byID[item.ID]
		if !ok || member.Kind != "skill" || !member.Importable || member.Shadowed || member.Builtin || member.ContentHash != item.ContentHash || member.Slug != item.Slug {
			writeError(w, r, http.StatusConflict, bridgeprotocol.ErrRevisionConflict, "one or more Hermes Skills are stale or not importable")
			return
		}
	}
	var library []domain.Skill
	if raw, err := a.store.ListSkills(r.Context()); err == nil {
		_ = json.Unmarshal(raw, &library)
	}
	for _, item := range input.Items {
		for _, skill := range library {
			if skill.Slug == item.Slug && skill.CurrentContentHash != item.ContentHash && !input.ConfirmUpdates {
				writeError(w, r, http.StatusConflict, "update_confirmation_required", "an existing Skill has different content; confirm the update")
				return
			}
		}
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	items := make([]bridgeprotocol.LocalSkillBatchItem, 0, len(input.Items))
	for _, item := range input.Items {
		items = append(items, bridgeprotocol.LocalSkillBatchItem{ID: item.ID, ContentHash: item.ContentHash})
	}
	request := map[string]any{"targetRevision": input.TargetRevision, "items": items, "confirmUpdates": input.ConfirmUpdates}
	op, err := a.store.CreateOperation(r.Context(), store.CreateOperationInput{Kind: "skill_import", SourceID: targetID, IdempotencyKey: key, Request: request, Metadata: map[string]any{"source": "hermes", "targetId": targetID, "itemCount": len(items)}, TargetIDs: []string{targetID}, TargetRequests: map[string]any{targetID: request}})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (a *API) listBackups(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.ListBackups(r.Context(), chi.URLParam(r, "targetID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeItems(w, value)
}
func (a *API) restoreTarget(w http.ResponseWriter, r *http.Request) {
	var input struct{ BackupID, ExpectedRevision string }
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	if input.BackupID == "" || !bridgeprotocol.IsSHA256(input.ExpectedRevision) {
		writeError(w, r, 400, "invalid_request", "Backup and current target revision are required")
		return
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	targetID := chi.URLParam(r, "targetID")
	request := map[string]any{"backupId": input.BackupID, "targetRevision": input.ExpectedRevision, "sourceKind": "restore", "sourceId": input.BackupID}
	op, err := a.store.CreateOperation(r.Context(), store.CreateOperationInput{Kind: "restore", SourceID: targetID, IdempotencyKey: key, Request: request, TargetIDs: []string{targetID}, TargetRequests: map[string]any{targetID: request}})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, 202, op)
}

func (a *API) adoptTargetProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	profile, err := a.store.AdoptRestoredTarget(r.Context(), chi.URLParam(r, "targetID"), strings.TrimSpace(input.Name))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, profile)
}

func (a *API) relayAction(w http.ResponseWriter, r *http.Request) {
	action := chi.URLParam(r, "action")
	kind := map[string]string{"start": "relay_start", "stop": "relay_stop", "restart": "relay_restart", "health": "relay_health"}[action]
	if kind == "" {
		writeError(w, r, 404, "not_found", "Relay action was not found")
		return
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	targetID := chi.URLParam(r, "targetID")
	target, err := a.store.Target(r.Context(), targetID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if target.Runtime != domain.RuntimeSharedRelay {
		writeError(w, r, 400, "invalid_target", "Relay controls require local/shared-relay")
		return
	}
	op, err := a.store.CreateOperation(r.Context(), store.CreateOperationInput{Kind: kind, IdempotencyKey: key, Request: map[string]any{"targetId": targetID, "action": action}, TargetIDs: []string{targetID}})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, 202, op)
}

func (a *API) cancelOperation(w http.ResponseWriter, r *http.Request) {
	if err := a.store.CancelOperation(r.Context(), chi.URLParam(r, "operationID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *API) retryFailedTargets(w http.ResponseWriter, r *http.Request) {
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	op, err := a.store.RetryFailedTargets(r.Context(), chi.URLParam(r, "operationID"), key)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, 202, op)
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := a.store.Settings(r.Context())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, 200, settings)
}
func (a *API) updateSettings(w http.ResponseWriter, r *http.Request) {
	var input domain.Settings
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	settings, err := a.store.UpdateSettings(r.Context(), input)
	if err != nil {
		writeError(w, r, 400, "invalid_settings", err.Error())
		return
	}
	writeJSON(w, 200, settings)
}

func (a *API) checkUpdates(w http.ResponseWriter, r *http.Request) {
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	op, err := a.store.CreateOperation(r.Context(), store.CreateOperationInput{Kind: "update_check", IdempotencyKey: key, Request: map[string]any{"manual": true}, Metadata: map[string]any{"manual": true}})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, 202, op)
}

func (a *API) searchMarket(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := a.market.Search(r.Context(), r.URL.Query().Get("source"), r.URL.Query().Get("q"), page, limit)
	if err != nil {
		switch {
		case errors.Is(err, market.ErrRateLimited):
			writeError(w, r, 429, "market_rate_limited", "Marketplace rate limit is exhausted")
		case errors.Is(err, market.ErrUnknownSource), errors.Is(err, market.ErrInvalidQuery):
			writeError(w, r, 400, "invalid_request", err.Error())
		default:
			writeError(w, r, 502, "market_unavailable", "Marketplace is unavailable")
		}
		return
	}
	writeJSON(w, 200, result)
}
func (a *API) recommend(w http.ResponseWriter, r *http.Request) {
	var input ai.Request
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	candidates, err := a.recommender.Recommend(r.Context(), input)
	if err != nil {
		writeError(w, r, 502, "recommendation_failed", "Recommendation provider is unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"candidates": candidates, "automaticInstall": false})
}

var _ = context.Background
var _ = encodingJSONAnchor
var encodingJSONAnchor = json.Valid
var _ = fmt.Sprintf
var _ = strings.TrimSpace
