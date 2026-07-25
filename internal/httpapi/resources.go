package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/toolhub-dev/toolhub/internal/domain"
)

var enrollmentHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)

func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.Overview(r.Context())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListUsers(r.Context())
	a.serveList(w, r, raw, err)
}
func (a *API) listNodes(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListNodes(r.Context())
	a.serveList(w, r, raw, err)
}
func (a *API) listSkills(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListSkills(r.Context())
	a.serveList(w, r, raw, err)
}
func (a *API) listSources(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListSources(r.Context())
	a.serveList(w, r, raw, err)
}
func (a *API) listDeployments(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListDeployments(r.Context())
	a.serveList(w, r, raw, err)
}
func (a *API) listUpdates(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListUpdates(r.Context())
	a.serveList(w, r, raw, err)
}
func (a *API) listJobs(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListJobs(r.Context())
	a.serveList(w, r, raw, err)
}
func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ListAudit(r.Context())
	a.serveList(w, r, raw, err)
}

func (a *API) serveList(w http.ResponseWriter, r *http.Request, raw json.RawMessage, err error) {
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeItems(w, raw)
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, err := a.store.CreateUser(r.Context(), input.Email, input.DisplayName, input.Password, input.Role)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "user_create_failed", err.Error())
		return
	}
	principal := principalFrom(r.Context())
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "create", ResourceType: "user", ResourceID: user.ID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"role": input.Role}})
	writeJSON(w, http.StatusCreated, user)
}

func (a *API) createEnrollment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	serverURL, err := enrollmentServerURL(a.config.PublicURL, r)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "invalid_public_url", err.Error())
		return
	}
	principal := principalFrom(r.Context())
	token, expires, err := a.store.CreateEnrollmentToken(r.Context(), input.Name, input.Labels, principal.ID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "enrollment_create_failed", err.Error())
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "create_enrollment", ResourceType: "node", ResourceID: input.Name, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"expiresAt": expires}})
	command := fmt.Sprintf("toolhub-agent enroll --server %s --token %s", serverURL, token)
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "expiresAt": expires, "agentCommand": command})
}

func enrollmentServerURL(configured string, r *http.Request) (string, error) {
	serverURL := strings.TrimSpace(configured)
	if serverURL == "" {
		scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
		if scheme != "http" && scheme != "https" {
			scheme = "http"
			if r.TLS != nil {
				scheme = "https"
			}
		}
		serverURL = scheme + "://" + r.Host
	}
	parsed, err := url.Parse(serverURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("TOOLHUB_PUBLIC_URL must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	hostname := parsed.Hostname()
	if net.ParseIP(hostname) == nil && !enrollmentHostnamePattern.MatchString(hostname) {
		return "", errors.New("TOOLHUB_PUBLIC_URL contains an invalid hostname")
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port := parsed.Port(); port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	return parsed.Scheme + "://" + host, nil
}

func (a *API) getNode(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, raw)
}

func (a *API) updateNode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name                 string            `json:"name"`
		Labels               map[string]string `json:"labels"`
		ConnectionPreference string            `json:"connectionPreference"`
	}
	if err := decodeJSON(w, r, &input, 128<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if input.ConnectionPreference != "" && input.ConnectionPreference != "agent" && input.ConnectionPreference != "ssh" {
		writeError(w, r, http.StatusBadRequest, "invalid_connection", "Connection preference must be agent or ssh")
		return
	}
	labels, _ := json.Marshal(input.Labels)
	command, err := a.store.Pool().Exec(r.Context(), `UPDATE nodes SET name=CASE WHEN $2='' THEN name ELSE $2 END,
		labels=CASE WHEN $3::jsonb='null'::jsonb THEN labels ELSE $3::jsonb END,
		connection_preference=CASE WHEN $4='' THEN connection_preference ELSE $4 END,updated_at=now() WHERE id=$1`, chi.URLParam(r, "id"), strings.TrimSpace(input.Name), labels, input.ConnectionPreference)
	if err != nil || command.RowsAffected() == 0 {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": chi.URLParam(r, "id"), "updated": true})
}

func (a *API) archiveNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	command, err := a.store.Pool().Exec(r.Context(), "UPDATE nodes SET status='archived',archived_at=now(),updated_at=now() WHERE id=$1 AND archived_at IS NULL", id)
	if err != nil || command.RowsAffected() == 0 {
		handleStoreError(w, r, err)
		return
	}
	principal := principalFrom(r.Context())
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "archive", ResourceType: "node", ResourceID: id, Outcome: "success", IPAddress: clientIP(r)})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) scanNode(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	job, err := a.store.EnqueueJob(r.Context(), "inventory_scan", map[string]any{"nodeId": chi.URLParam(r, "id"), "readOnly": true}, false, principal.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) createNodeConnection(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Kind       string `json:"kind"`
		Address    string `json:"address"`
		KnownHosts string `json:"knownHosts"`
		PrivateKey string `json:"privateKey"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if input.Kind != "ssh" {
		writeError(w, r, http.StatusBadRequest, "invalid_connection", "Only the SSH fallback connection can be created manually")
		return
	}
	principal := principalFrom(r.Context())
	id, err := a.store.CreateSSHConnection(r.Context(), chi.URLParam(r, "id"), input.Address, input.KnownHosts, input.PrivateKey, principal.ID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "ssh_connection_failed", err.Error())
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "create", ResourceType: "node_connection", ResourceID: id, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"kind": "ssh", "nodeId": chi.URLParam(r, "id")}})
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (a *API) cancelJob(w http.ResponseWriter, r *http.Request) {
	if err := a.store.CancelJob(r.Context(), chi.URLParam(r, "id")); err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}
