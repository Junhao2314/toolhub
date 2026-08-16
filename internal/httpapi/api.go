package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Junhao2314/toolhub/internal/ai"
	"github.com/Junhao2314/toolhub/internal/bridgeclient"
	"github.com/Junhao2314/toolhub/internal/config"
	"github.com/Junhao2314/toolhub/internal/market"
	"github.com/Junhao2314/toolhub/internal/store"
)

type API struct {
	store       *store.Store
	bridge      *bridgeclient.Client
	market      *market.Multi
	recommender *ai.Recommender
	config      config.Config
	logger      *slog.Logger
	limiter     *loginLimiter
}

func New(st *store.Store, bridge *bridgeclient.Client, marketClient *market.Multi, recommender *ai.Recommender, cfg config.Config, logger *slog.Logger) *API {
	return &API{store: st, bridge: bridge, market: marketClient, recommender: recommender, config: cfg, logger: logger, limiter: newLoginLimiter()}
}

func (a *API) Router() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	router.Use(middleware.Timeout(16 * time.Minute))
	router.Get("/healthz", a.health)
	router.Route("/api/v1", func(api chi.Router) {
		api.Post("/auth/login", a.login)
		api.Get("/auth/session", a.sessionProbe)
		api.Group(func(auth chi.Router) {
			auth.Use(a.authenticate, a.verifyCSRF)
			auth.Post("/auth/logout", a.logout)
			auth.Get("/auth/csrf", a.csrf)
			auth.Get("/account", a.getAccount)
			auth.Patch("/account/username", a.updateUsername)
			auth.Patch("/account/password", a.updatePassword)
			auth.Get("/overview", a.overview)
			auth.Get("/skills", a.listSkills)
			auth.Post("/skills/upload", a.uploadSkill)
			auth.Post("/skills/import", a.importSkill)
			auth.Put("/skills/{skillID}/tags", a.updateSkillTags)
			auth.Get("/market/search", a.searchMarket)
			auth.Post("/recommendations", a.recommend)
			auth.Get("/mcp/servers", a.listMCPServers)
			auth.Post("/mcp/servers", a.createMCPServer)
			auth.Post("/mcp/import", a.importLocalMCP)
			auth.Put("/mcp/servers/{serverID}", a.updateMCPServer)
			auth.Delete("/mcp/servers/{serverID}", a.deleteMCPServer)
			auth.Get("/profiles", a.listProfiles)
			auth.Post("/profiles", a.createProfile)
			auth.Post("/profiles/bundles/preview", a.previewProfileBundle)
			auth.Post("/profiles/bundles/import", a.importProfileBundle)
			auth.Put("/profiles/{profileID}", a.updateProfile)
			auth.Delete("/profiles/{profileID}", a.deleteProfile)
			auth.Get("/profiles/{profileID}", a.getProfile)
			auth.Post("/profiles/{profileID}/refresh", a.refreshProfile)
			auth.Post("/profiles/{profileID}/clone", a.cloneProfile)
			auth.Post("/profiles/{profileID}/archive", a.archiveProfile)
			auth.Post("/profiles/{profileID}/restore", a.restoreProfile)
			auth.Post("/profiles/{profileID}/purge", a.purgeProfile)
			auth.Get("/profiles/{profileID}/secret-bindings", a.profileSecretBindings)
			auth.Post("/profiles/{profileID}/secret-bindings", a.completeProfileBindings)
			auth.Post("/profiles/{profileID}/bundle/export", a.exportProfileBundle)
			auth.Post("/profiles/{profileID}/bundle/export-secrets", a.exportSecretProfileBundle)
			auth.Post("/profiles/{profileID}/preflight", a.profilePreflight)
			auth.Post("/profiles/{profileID}/apply", a.applyProfile)
			auth.Get("/profiles/{profileID}/launch", a.profileLaunch)
			auth.Get("/profiles/{profileID}/history", a.profileHistory)
			auth.Get("/nodes", a.listNodes)
			auth.Post("/nodes/refresh", a.refreshNodes)
			auth.Patch("/nodes/{nodeID}", a.updateNode)
			auth.Get("/targets", a.listTargets)
			auth.Get("/targets/{targetID}", a.getTarget)
			auth.Post("/targets/{targetID}/scan", a.scanTarget)
			auth.Post("/targets/{targetID}/skill-import", a.importLocalSkill)
			auth.Post("/targets/{targetID}/skill-imports", a.importLocalSkillBatch)
			auth.Post("/targets/{targetID}/mcp-import/preflight", a.preflightLocalMCPImport)
			auth.Get("/targets/{targetID}/backups", a.listBackups)
			auth.Post("/targets/{targetID}/restore", a.restoreTarget)
			auth.Post("/targets/{targetID}/profile-adoption", a.adoptTargetProfile)
			auth.Post("/targets/{targetID}/relay/{action}", a.relayAction)
			auth.Get("/relay/configuration", a.relayConfiguration)
			auth.Put("/relay/configuration", a.saveRelayConfiguration)
			auth.Post("/relay/configuration/preflight", a.preflightRelayConfiguration)
			auth.Post("/relay/configuration/apply", a.applyRelayConfiguration)
			auth.Post("/relay/configuration/prepare-profile-updates", a.prepareRelayProfileUpdates)
			auth.Get("/relay/contracts", a.relayContracts)
			auth.Post("/relay/contracts/observe", a.observeRelayContracts)
			auth.Post("/relay/contracts/{revisionID}/accept", a.acceptRelayContract)
			auth.Post("/relay/renames/{proposalID}/confirm", a.confirmRelayRename)
			auth.Get("/relay/confirmations", a.relayConfirmations)
			auth.Post("/relay/confirmations/{challengeID}/approve", a.approveRelayConfirmation)
			auth.Post("/relay/confirmations/{challengeID}/reject", a.rejectRelayConfirmation)
			auth.Get("/relay/observations/live", a.liveRelayObservations)
			auth.Get("/relay/observations/daily", a.dailyRelayObservations)
			auth.Get("/mcp/policy", a.globalPolicy)
			auth.Put("/mcp/policy", a.saveGlobalPolicy)
			auth.Post("/mcp/policy/apply", a.applyGlobalPolicy)
			auth.Get("/operations", a.listOperations)
			auth.Get("/operations/{operationID}", a.getOperation)
			auth.Post("/operations/{operationID}/cancel", a.cancelOperation)
			auth.Post("/operations/{operationID}/retry-failed", a.retryFailedTargets)
			auth.Get("/audit", a.listAudit)
			auth.Get("/settings", a.getSettings)
			auth.Put("/settings", a.updateSettings)
			auth.Post("/updates/check", a.checkUpdates)
		})
	})
	return router
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Pool().Ping(r.Context()); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
		return
	}
	if err := a.store.RequireSchemaGeneration(r.Context()); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "schema_generation_mismatch", err.Error())
		return
	}
	bridgeStatus := "ok"
	if err := a.bridge.Health(r.Context()); err != nil {
		bridgeStatus = "unavailable"
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "bridge": bridgeStatus})
}
