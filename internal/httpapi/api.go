package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Junhao2314/toolhub/internal/agenthub"
	"github.com/Junhao2314/toolhub/internal/ai"
	"github.com/Junhao2314/toolhub/internal/config"
	"github.com/Junhao2314/toolhub/internal/market"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/store"
)

type API struct {
	config      config.Config
	store       *store.Store
	hub         *agenthub.Hub
	market      *market.Client
	recommender *ai.Recommender
	logger      *slog.Logger
	dummyHash   string
	loginLimit  *ipLimiter
}

func New(cfg config.Config, st *store.Store, hub *agenthub.Hub, marketClient *market.Client, recommender *ai.Recommender, logger *slog.Logger) (*API, error) {
	dummyHash, err := security.HashPassword("toolhub timing equalizer password")
	if err != nil {
		return nil, err
	}
	return &API{config: cfg, store: st, hub: hub, market: marketClient, recommender: recommender, logger: logger, dummyHash: dummyHash, loginLimit: newIPLimiter(10, 10*time.Minute)}, nil
}

func (a *API) Router() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)
	router.Use(a.securityHeaders)
	router.Use(a.accessLog)
	router.Get("/healthz", a.health)
	router.Post("/agent/v1/enroll", a.enrollAgent)
	router.Get("/agent/v1/connect", a.hub.ServeConnect)
	router.Get("/agent/v1/artifacts/{versionID}", a.agentArtifact)
	router.Get("/agent/v1/secrets/{secretID}", a.agentSecret)
	router.Post("/agent/v1/discoveries/descriptors", a.agentDiscoveryDescriptors)
	router.Post("/agent/v1/discoveries/capture", a.agentDiscoveryCapture)
	router.Post("/agent/v1/discoveries/{id}/skill", a.agentSkillAdoptionUpload)
	router.Post("/api/v1/auth/login", a.login)
	router.Get("/api/v1/auth/session", a.session)

	router.Route("/api/v1", func(api chi.Router) {
		api.Use(a.authenticate)
		api.Use(a.verifyCSRF)
		api.Post("/auth/logout", a.logout)
		api.Get("/auth/me", a.me)
		api.Get("/auth/csrf", a.csrf)
		api.Patch("/account/username", a.updateOwnUsername)
		api.Patch("/account/password", a.updateOwnPassword)
		api.Get("/overview", a.overview)

		api.Group(func(read chi.Router) {
			read.Use(a.requireRoles("admin", "operator", "viewer"))
			read.Get("/nodes", a.listNodes)
			read.Get("/nodes/{id}", a.getNode)
			read.Get("/skills", a.listSkills)
			read.Get("/skills/{id}", a.getSkill)
			read.Get("/sources", a.listSources)
			read.Get("/deployments", a.listDeployments)
			read.Get("/updates", a.listUpdates)
			read.Get("/jobs", a.listJobs)
			read.Get("/market/search", a.searchMarket)
			read.Get("/mcp/servers", a.listMCPServers)
			read.Get("/mcp/profiles", a.listMCPProfiles)
			read.Get("/mcp/deployments", a.listMCPDeployments)
			read.Get("/discoveries", a.listDiscoveries)
		})

		api.Group(func(ops chi.Router) {
			ops.Use(a.requireRoles("admin", "operator"))
			ops.Post("/nodes", a.createEnrollment)
			ops.Patch("/nodes/{id}", a.updateNode)
			ops.Delete("/nodes/{id}", a.archiveNode)
			ops.Post("/nodes/{id}/scan", a.scanNode)
			ops.Post("/nodes/{id}/connections", a.createNodeConnection)
			ops.Post("/skills", a.importSkill)
			ops.Post("/skills/upload", a.uploadSkill)
			ops.Delete("/skills/{id}", a.archiveSkill)
			ops.Post("/skills/{id}/deployments", a.setSkillTargets)
			ops.Post("/deployments/{id}/rollback", a.rollbackDeployment)
			ops.Post("/updates", a.checkUpdates)
			ops.Post("/sync", a.syncNow)
			ops.Post("/reconcile", a.reconcileNow)
			ops.Post("/jobs/{id}/cancel", a.cancelJob)
			ops.Post("/recommendations", a.recommend)
			ops.Post("/mcp/servers", a.createMCPServer)
			ops.Patch("/mcp/servers/{id}", a.updateMCPServer)
			ops.Delete("/mcp/servers/{id}", a.deleteMCPServer)
			ops.Post("/mcp/servers/{id}/health", a.checkMCPHealth)
			ops.Post("/mcp/profiles", a.createMCPProfile)
			ops.Post("/mcp/deployments", a.deployMCPProfile)
		})

		api.Group(func(admin chi.Router) {
			admin.Use(a.requireRoles("admin"))
			admin.Get("/users", a.listUsers)
			admin.Post("/users", a.createUser)
			admin.Post("/users/{id}/password", a.resetUserPassword)
			admin.Get("/audit", a.listAudit)
			admin.Post("/skills/{id}/review", a.reviewSkill)
			admin.Post("/discoveries/{id}/adopt-skill", a.adoptDiscoveredSkill)
			admin.Post("/updates/{id}/approve", a.approveUpdate)
			admin.Get("/settings", a.getSettings)
			admin.Patch("/settings", a.updateSettings)
			admin.Get("/settings/ai-providers", a.listAIProviders)
			admin.Post("/settings/ai-providers", a.createAIProvider)
		})
	})
	return router
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.Pool().Ping(ctx); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeItems(w http.ResponseWriter, raw json.RawMessage) {
	writeJSON(w, http.StatusOK, struct {
		Items json.RawMessage `json:"items"`
	}{Items: raw})
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "requestId": middleware.GetReqID(r.Context())}})
}

func handleStoreError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "The requested resource was not found")
		return
	}
	writeError(w, r, http.StatusInternalServerError, "internal_error", "The operation failed")
}
