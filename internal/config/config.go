package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/security"
)

type Config struct {
	ListenAddr        string
	DatabaseURL       string
	MasterKey         []byte
	BootstrapUsername string
	BootstrapPassword string
	LocalNodeName     string
	ManagedUsername   string
	BridgeSocket      string
	BridgeKey         []byte
	Timezone          *time.Location
	SkillsMPAPIKey    string
	XiapingAPIKey     string
	XiapingBaseURL    string
	SkillHubBaseURL   string
	SessionTTL        time.Duration
	SecureCookies     bool
	RelayPort         int
}

func Load() (Config, error) {
	locationName := env("TOOLHUB_TIMEZONE", "Asia/Shanghai")
	location, err := time.LoadLocation(locationName)
	if err != nil {
		return Config{}, fmt.Errorf("load timezone %q: %w", locationName, err)
	}
	masterKey, err := parseMasterKey(os.Getenv("TOOLHUB_MASTER_KEY"))
	if err != nil {
		return Config{}, err
	}
	ttl, err := time.ParseDuration(env("TOOLHUB_SESSION_TTL", "12h"))
	if err != nil || ttl < 15*time.Minute || ttl > 7*24*time.Hour {
		return Config{}, errors.New("TOOLHUB_SESSION_TTL must be between 15m and 168h")
	}
	secureCookies, err := strconv.ParseBool(env("TOOLHUB_SECURE_COOKIES", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("parse TOOLHUB_SECURE_COOKIES: %w", err)
	}
	bootstrapUsername := env("TOOLHUB_BOOTSTRAP_USERNAME", "admin")
	if normalized, normalizeErr := security.NormalizeUsername(bootstrapUsername); normalizeErr == nil {
		bootstrapUsername = normalized
	}
	managedUsername, err := validateManagedUsername(env("TOOLHUB_MANAGED_USERNAME", "toolhub"))
	if err != nil {
		return Config{}, fmt.Errorf("TOOLHUB_MANAGED_USERNAME: %w", err)
	}
	bridgeSocket, err := validateSocketPath(env("TOOLHUB_BRIDGE_SOCKET", "/run/toolhub-bridge/bridge.sock"))
	if err != nil {
		return Config{}, fmt.Errorf("TOOLHUB_BRIDGE_SOCKET: %w", err)
	}
	bridgeKey, err := bridgeprotocol.ParseKey(os.Getenv("TOOLHUB_BRIDGE_HMAC_KEY"))
	if err != nil {
		return Config{}, fmt.Errorf("TOOLHUB_BRIDGE_HMAC_KEY: %w", err)
	}
	relayPort, err := strconv.Atoi(env("TOOLHUB_RELAY_PORT", "6276"))
	if err != nil || relayPort < 1 || relayPort > 65535 {
		return Config{}, errors.New("TOOLHUB_RELAY_PORT must be between 1 and 65535")
	}
	cfg := Config{
		ListenAddr:        env("TOOLHUB_LISTEN_ADDR", "127.0.0.1:18480"),
		DatabaseURL:       strings.TrimSpace(os.Getenv("TOOLHUB_DATABASE_URL")),
		MasterKey:         masterKey,
		BootstrapUsername: bootstrapUsername,
		BootstrapPassword: os.Getenv("TOOLHUB_BOOTSTRAP_PASSWORD"),
		LocalNodeName:     env("TOOLHUB_LOCAL_NODE_NAME", "project-host"),
		ManagedUsername:   managedUsername,
		BridgeSocket:      bridgeSocket,
		BridgeKey:         bridgeKey,
		Timezone:          location,
		SkillsMPAPIKey:    strings.TrimSpace(os.Getenv("SKILLSMP_API_KEY")),
		XiapingAPIKey:     strings.TrimSpace(os.Getenv("XIAPING_API_KEY")),
		XiapingBaseURL:    strings.TrimRight(env("XIAPING_BASE_URL", "https://xiaping.coze.com"), "/"),
		SkillHubBaseURL:   strings.TrimRight(env("SKILLHUB_BASE_URL", "https://api.skillhub.tencent.com"), "/"),
		SessionTTL:        ttl,
		SecureCookies:     secureCookies,
		RelayPort:         relayPort,
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("TOOLHUB_DATABASE_URL is required")
	}
	parsedXiapingURL, err := url.Parse(cfg.XiapingBaseURL)
	if err != nil || parsedXiapingURL.Scheme != "https" || parsedXiapingURL.Hostname() == "" || parsedXiapingURL.User != nil || parsedXiapingURL.RawQuery != "" || parsedXiapingURL.Fragment != "" {
		return Config{}, errors.New("XIAPING_BASE_URL must be an https URL without embedded credentials")
	}
	parsedSkillHubURL, err := url.Parse(cfg.SkillHubBaseURL)
	if err != nil || parsedSkillHubURL.Scheme != "https" || parsedSkillHubURL.Hostname() == "" || parsedSkillHubURL.User != nil || parsedSkillHubURL.RawQuery != "" || parsedSkillHubURL.Fragment != "" {
		return Config{}, errors.New("SKILLHUB_BASE_URL must be an https URL without embedded credentials")
	}
	return cfg, nil
}

func validateManagedUsername(value string) (string, error) {
	value = strings.TrimSpace(value)
	if err := bridgeprotocol.ValidateManagedUsername(value); err != nil {
		return "", err
	}
	return value, nil
}

func validateSocketPath(value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(value) || value == string(filepath.Separator) {
		return "", errors.New("must be an absolute Unix socket path")
	}
	return value, nil
}

func parseMasterKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("TOOLHUB_MASTER_KEY is required")
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(value) == 32 {
		return []byte(value), nil
	}
	return nil, errors.New("TOOLHUB_MASTER_KEY must be exactly 32 raw bytes or base64-encoded 32 bytes")
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
