package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/toolhub-dev/toolhub/internal/domain"
	"github.com/toolhub-dev/toolhub/internal/security"
)

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !a.loginLimit.allow(ip) {
		writeError(w, r, http.StatusTooManyRequests, "login_rate_limited", "Too many login attempts; retry later")
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, err := a.store.UserByEmail(r.Context(), input.Email)
	hash := a.dummyHash
	if err == nil {
		hash = user.PasswordHash
	}
	valid, verifyErr := security.VerifyPassword(hash, input.Password)
	if err != nil || verifyErr != nil || !valid || user.Disabled {
		_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "login", ResourceType: "session", Outcome: "failure", IPAddress: ip, Metadata: map[string]any{"email": strings.ToLower(strings.TrimSpace(input.Email))}})
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect")
		return
	}
	token, csrf, expires, err := a.store.CreateSession(r.Context(), user.ID, a.config.SessionTTL, ip, r.UserAgent())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "toolhub_session", Value: token, Path: "/", HttpOnly: true, Secure: a.config.SecureCookies, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: user.ID, Action: "login", ResourceType: "session", Outcome: "success", IPAddress: ip})
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "csrfToken": csrf, "expiresAt": expires})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("toolhub_session"); err == nil {
		_ = a.store.DeleteSession(r.Context(), cookie.Value)
	}
	principal := principalFrom(r.Context())
	_ = a.store.Audit(r.Context(), domain.AuditEvent{ActorUserID: principal.ID, Action: "logout", ResourceType: "session", Outcome: "success", IPAddress: clientIP(r)})
	clearSessionCookie(w, a.config.SecureCookies)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": principalFrom(r.Context())})
}

func (a *API) session(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("toolhub_session")
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	principal, err := a.store.SessionPrincipal(r.Context(), cookie.Value)
	if err != nil {
		clearSessionCookie(w, a.config.SecureCookies)
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	csrf, err := a.store.RotateSessionCSRF(r.Context(), cookie.Value)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "user": principal, "csrfToken": csrf, "expiresAt": principal.ExpiresAt})
}

func (a *API) csrf(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("toolhub_session")
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication is required")
		return
	}
	token, err := a.store.RotateSessionCSRF(r.Context(), cookie.Value)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"csrfToken": token})
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: "toolhub_session", Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}
