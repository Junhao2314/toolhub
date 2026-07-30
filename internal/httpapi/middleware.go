package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/store"
)

type principalKey struct{}
type sessionTokenKey struct{}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("toolhub_session")
		if err != nil || cookie.Value == "" {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication is required")
			return
		}
		principal, err := a.store.SessionPrincipal(r.Context(), cookie.Value)
		if err != nil {
			clearSessionCookie(w, a.config.SecureCookies)
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication is required")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, principal)
		ctx = context.WithValue(ctx, sessionTokenKey{}, cookie.Value)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) verifyCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-CSRF-Token")
		principal := principalFrom(r.Context())
		providedHash := security.TokenHash(provided)
		if provided == "" || len(principal.CSRFHash) != len(providedHash) || subtle.ConstantTimeCompare(principal.CSRFHash, providedHash) != 1 {
			writeError(w, r, http.StatusForbidden, "csrf_invalid", "A valid CSRF token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func principalFrom(ctx context.Context) domain.Principal {
	principal, _ := ctx.Value(principalKey{}).(domain.Principal)
	return principal
}

func sessionTokenFrom(ctx context.Context) string {
	token, _ := ctx.Value(sessionTokenKey{}).(string)
	return token
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeItems(w http.ResponseWriter, raw json.RawMessage) {
	writeJSON(w, http.StatusOK, map[string]any{"items": raw})
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "requestId": middleware.GetReqID(r.Context())}})
}

func (a *API) handleStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "Resource was not found")
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrOperationActive):
		writeError(w, r, http.StatusConflict, "state_conflict", err.Error())
	case errors.Is(err, store.ErrIdempotencyConflict):
		writeError(w, r, http.StatusConflict, "idempotency_conflict", err.Error())
	default:
		a.logger.Error("store request failed", "requestId", middleware.GetReqID(r.Context()), "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Request could not be completed")
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func requestIdempotencyKey(r *http.Request) (string, error) {
	value := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(value) < 8 || len(value) > 200 {
		return "", errors.New("Idempotency-Key must contain 8-200 characters")
	}
	return value, nil
}
