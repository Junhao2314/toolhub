//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridge"
	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/runtime"
	"github.com/Junhao2314/toolhub/internal/saltdriver"
)

type options struct {
	Socket        string
	Group         string
	KeyFile       string
	Journal       string
	StagingRoot   string
	BackupRoot    string
	SaltStateRoot string
}

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var options options
	flag.StringVar(&options.Socket, "socket", "/run/toolhub-bridge/bridge.sock", "Unix socket path")
	flag.StringVar(&options.Group, "group", "toolhub", "shared Unix socket group")
	flag.StringVar(&options.KeyFile, "key-file", "/etc/toolhub-bridge/hmac.key", "root-only HMAC key file")
	flag.StringVar(&options.Journal, "journal", "/var/lib/toolhub-bridge/journal.db", "Bridge BoltDB journal")
	flag.StringVar(&options.StagingRoot, "staging-root", "/var/lib/toolhub-bridge/staging", "root-only Salt staging root")
	flag.StringVar(&options.BackupRoot, "backup-root", "/var/lib/toolhub-bridge/backups", "local target backup root")
	flag.StringVar(&options.SaltStateRoot, "salt-state-root", saltdriver.DefaultStateRoot, "existing Salt base state root")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(errors.New("toolhub-bridge accepts no positional arguments"))
	}
	if err := run(options); err != nil {
		fatal(err)
	}
}

func run(options options) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("toolhub-bridge starting", "version", version)
	key, err := loadKey(options.KeyFile)
	if err != nil {
		return err
	}
	journal, err := bridge.OpenJournal(options.Journal)
	if err != nil {
		return fmt.Errorf("open Bridge journal: %w", err)
	}
	defer journal.Close()
	driver := saltdriver.New(nil)
	driver.StateRoot = options.SaltStateRoot
	driver.StagingRoot = options.StagingRoot
	manager := runtime.NewManager(options.BackupRoot)
	relay := runtime.NewRelayManager(runtime.SystemdRelayController{}, options.BackupRoot)
	adapter := bridge.NewCompositeAdapter(journal, manager, relay, driver)
	server, err := bridge.NewServer(key, journal, adapter, logger)
	if err != nil {
		return err
	}
	listener, err := unixListener(options.Socket, options.Group)
	if err != nil {
		return err
	}
	defer func() {
		listener.Close()
		_ = os.Remove(options.Socket)
	}()
	httpServer := &http.Server{Handler: server.Router(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 12 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("toolhub-bridge listening", "socket", options.Socket)
	err = httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func loadKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat Bridge HMAC key: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("Bridge HMAC key must be a root-only regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := bridgeprotocol.ParseKey(strings.TrimSpace(string(body)))
	if err != nil {
		return nil, err
	}
	return key, nil
}

func unixListener(path, groupName string) (net.Listener, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return nil, errors.New("Bridge Unix socket path is unsafe")
	}
	group, err := user.LookupGroup(groupName)
	if err != nil {
		return nil, fmt.Errorf("lookup Bridge group: %w", err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return nil, errors.New("Bridge group GID is invalid")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0750); err != nil {
		return nil, err
	}
	if err := os.Chown(directory, 0, gid); err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0750); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chown(path, 0, gid); err != nil {
		listener.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0660); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func fatal(err error) {
	slog.Error("toolhub-bridge stopped", "error", err)
	os.Exit(1)
}
