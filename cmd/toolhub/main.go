package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/Junhao2314/toolhub/internal/ai"
	"github.com/Junhao2314/toolhub/internal/bridgeclient"
	"github.com/Junhao2314/toolhub/internal/config"
	"github.com/Junhao2314/toolhub/internal/httpapi"
	"github.com/Junhao2314/toolhub/internal/market"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/store"
	"github.com/Junhao2314/toolhub/internal/worker"
)

//go:embed dist/*
var webAssets embed.FS

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("toolhub stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cipher, err := security.NewCipher(cfg.MasterKey)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, cfg.DatabaseURL, cipher)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	created, err := st.BootstrapAccount(ctx, cfg.BootstrapUsername, cfg.BootstrapPassword)
	if err != nil {
		return err
	}
	if created {
		logger.Info("singleton account created", "username", cfg.BootstrapUsername)
	}
	if err := st.BootstrapEnvironment(ctx, cfg.LocalNodeName, cfg.ManagedUsername, cfg.Timezone.String(), cfg.RelayPort); err != nil {
		return err
	}
	if err := st.InitializeRelayMigrationReadiness(ctx); err != nil {
		return err
	}
	bridge, err := bridgeclient.New(cfg.BridgeSocket, cfg.BridgeKey)
	if err != nil {
		return err
	}
	marketClient := market.NewMulti(
		market.New("https://skillsmp.com/api/v1", cfg.SkillsMPAPIKey),
		market.NewXiaping(cfg.XiapingBaseURL, cfg.XiapingAPIKey),
		market.NewSkillHub(cfg.SkillHubBaseURL),
	)
	operationWorker := worker.New(st, bridge, marketClient, logger)
	if err := operationWorker.Recover(ctx); err != nil {
		return err
	}
	operationWorker.Run(ctx, 4)
	scheduler := worker.NewScheduler(st, logger)
	go scheduler.Run(ctx)
	api := httpapi.New(st, bridge, marketClient, ai.New(st), cfg, logger)
	spa, err := newSPAHandler()
	if err != nil {
		return err
	}
	apiHandler := api.Router()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		spa.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("ToolHub listening", "address", cfg.ListenAddr)
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func newSPAHandler() (http.Handler, error) {
	assets, err := fs.Sub(webAssets, "dist")
	if err != nil {
		return nil, err
	}
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean != "." && clean != "" {
			if file, err := assets.Open(clean); err == nil {
				_ = file.Close()
				if strings.HasPrefix(clean, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		body, err := fs.ReadFile(assets, "index.html")
		if err != nil {
			http.Error(w, "frontend unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}), nil
}
