package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/store"
)

var dummyHash, _ = security.HashPassword("toolhub-dummy-password-only")

type loginAttempt struct {
	count int
	reset time.Time
}
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{attempts: map[string]loginAttempt{}} }
func (l *loginLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	value := l.attempts[ip]
	if now.After(value.reset) {
		value = loginAttempt{reset: now.Add(10 * time.Minute)}
	}
	if value.count >= 10 {
		return false
	}
	value.count++
	l.attempts[ip] = value
	return true
}
func (l *loginLimiter) success(ip string) { l.mu.Lock(); delete(l.attempts, ip); l.mu.Unlock() }

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !a.limiter.allow(ip, time.Now()) {
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many login attempts")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Invalid login request")
		return
	}
	account, err := a.store.AccountByUsername(r.Context(), input.Username)
	hash := dummyHash
	if err == nil {
		hash = account.PasswordHash
	}
	valid, verifyErr := security.VerifyPassword(hash, input.Password)
	if err != nil || verifyErr != nil || !valid {
		_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "login", ResourceType: "session", Outcome: "denied", IPAddress: ip})
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "Username or password is incorrect")
		return
	}
	a.limiter.success(ip)
	token, csrfToken, expires, err := a.store.CreateSession(r.Context(), a.config.SessionTTL, ip, r.UserAgent())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "toolhub_session", Value: token, Path: "/", HttpOnly: true, Secure: a.config.SecureCookies, SameSite: http.SameSiteStrictMode, Expires: expires})
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "login", ResourceType: "session", Outcome: "success", IPAddress: ip})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "user": account, "csrfToken": csrfToken, "expiresAt": expires})
}

func (a *API) sessionProbe(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("toolhub_session")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	principal, err := a.store.SessionPrincipal(r.Context(), cookie.Value)
	if err != nil {
		clearSessionCookie(w, a.config.SecureCookies)
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	csrfToken, err := a.store.RotateSessionCSRF(r.Context(), cookie.Value)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "user": map[string]any{"username": principal.Username, "passwordChangeRecommended": principal.PasswordChangeRecommended}, "csrfToken": csrfToken, "expiresAt": principal.ExpiresAt})
}

func (a *API) csrf(w http.ResponseWriter, r *http.Request) {
	token, err := a.store.RotateSessionCSRF(r.Context(), sessionTokenFrom(r.Context()))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"csrfToken": token})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	_ = a.store.DeleteSession(r.Context(), sessionTokenFrom(r.Context()))
	clearSessionCookie(w, a.config.SecureCookies)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getAccount(w http.ResponseWriter, r *http.Request) {
	account, err := a.store.Account(r.Context())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (a *API) updateUsername(w http.ResponseWriter, r *http.Request) {
	var input struct{ Username, CurrentPassword string }
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	if err := a.store.UpdateUsername(r.Context(), input.Username, input.CurrentPassword); err != nil {
		a.credentialError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "update_username", ResourceType: "account", ResourceID: "singleton", Outcome: "success", IPAddress: clientIP(r)})
	clearSessionCookie(w, a.config.SecureCookies)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) updatePassword(w http.ResponseWriter, r *http.Request) {
	var input struct{ CurrentPassword, NewPassword string }
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	if err := security.ValidatePassword(input.NewPassword); err != nil {
		writeError(w, r, 400, "invalid_password", err.Error())
		return
	}
	if err := a.store.UpdatePassword(r.Context(), input.CurrentPassword, input.NewPassword); err != nil {
		a.credentialError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "update_password", ResourceType: "account", ResourceID: "singleton", Outcome: "success", IPAddress: clientIP(r)})
	clearSessionCookie(w, a.config.SecureCookies)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) credentialError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrInvalidCurrentPassword) {
		writeError(w, r, 403, "current_password_invalid", "Current password is incorrect")
		return
	}
	if errors.Is(err, store.ErrUsernameUnavailable) {
		writeError(w, r, 409, "username_unavailable", "Username is unavailable")
		return
	}
	a.handleStoreError(w, r, err)
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: "toolhub_session", Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

var _ = strings.TrimSpace
