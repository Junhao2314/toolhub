package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr             string
	DatabaseURL            string
	MasterKey              []byte
	BootstrapAdminEmail    string
	BootstrapAdminName     string
	BootstrapAdminPassword string
	PublicURL              string
	Timezone               *time.Location
	SkillsMPAPIKey         string
	DataDir                string
	SessionTTL             time.Duration
	SecureCookies          bool
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
	cfg := Config{
		ListenAddr:             env("TOOLHUB_LISTEN_ADDR", "127.0.0.1:18480"),
		DatabaseURL:            strings.TrimSpace(os.Getenv("TOOLHUB_DATABASE_URL")),
		MasterKey:              masterKey,
		BootstrapAdminEmail:    normalizeEmail(os.Getenv("TOOLHUB_BOOTSTRAP_ADMIN_EMAIL")),
		BootstrapAdminName:     env("TOOLHUB_BOOTSTRAP_ADMIN_NAME", "ToolHub Admin"),
		BootstrapAdminPassword: os.Getenv("TOOLHUB_BOOTSTRAP_ADMIN_PASSWORD"),
		PublicURL:              strings.TrimRight(strings.TrimSpace(os.Getenv("TOOLHUB_PUBLIC_URL")), "/"),
		Timezone:               location,
		SkillsMPAPIKey:         strings.TrimSpace(os.Getenv("SKILLSMP_API_KEY")),
		DataDir:                env("TOOLHUB_DATA_DIR", "/data"),
		SessionTTL:             ttl,
		SecureCookies:          secureCookies,
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("TOOLHUB_DATABASE_URL is required")
	}
	return cfg, nil
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

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
