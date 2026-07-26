package httpapi

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/security"
)

type contextKey string

const principalKey contextKey = "principal"

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self' wss: ws:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *API) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(wrapped, r)
		a.logger.Log(r.Context(), slog.LevelInfo, "request", "method", r.Method, "path", r.URL.Path, "status", wrapped.Status(), "durationMs", time.Since(started).Milliseconds(), "requestId", middleware.GetReqID(r.Context()))
	})
}

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
			writeError(w, r, http.StatusUnauthorized, "session_expired", "The session is invalid or expired")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, principal)))
	})
}

func (a *API) verifyCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		principal := principalFrom(r.Context())
		token := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
		hash := security.TokenHash(token)
		if token == "" || subtle.ConstantTimeCompare(hash, principal.CSRFHash) != 1 {
			writeError(w, r, http.StatusForbidden, "csrf_invalid", "A valid CSRF token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) requireRoles(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !principalFrom(r.Context()).HasRole(roles...) {
				writeError(w, r, http.StatusForbidden, "forbidden", "Your role does not permit this operation")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func principalFrom(ctx context.Context) domain.Principal {
	principal, _ := ctx.Value(principalKey).(domain.Principal)
	return principal
}

func clientIP(r *http.Request) string {
	if forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ","); len(forwarded) > 0 {
		if parsed := net.ParseIP(strings.TrimSpace(forwarded[0])); parsed != nil {
			return parsed.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return ""
}

type limitEntry struct {
	count int
	start time.Time
}

type ipLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	items  map[string]limitEntry
}

func newIPLimiter(max int, window time.Duration) *ipLimiter {
	return &ipLimiter{max: max, window: window, items: map[string]limitEntry{}}
}

func (l *ipLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.items) > 10000 {
		for itemKey, item := range l.items {
			if now.Sub(item.start) > l.window {
				delete(l.items, itemKey)
			}
		}
	}
	entry := l.items[key]
	if entry.start.IsZero() || now.Sub(entry.start) > l.window {
		entry = limitEntry{start: now}
	}
	entry.count++
	l.items[key] = entry
	return entry.count <= l.max
}
