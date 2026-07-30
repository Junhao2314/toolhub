package config

import (
	"strings"
	"testing"
)

func TestLoadUsesConfiguredProjectHostName(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TOOLHUB_LOCAL_NODE_NAME", "developer-workstation")

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.LocalNodeName != "developer-workstation" {
		t.Fatalf("expected configured project host, got %q", config.LocalNodeName)
	}
}

func TestLoadDefaultsProjectHostName(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TOOLHUB_LOCAL_NODE_NAME", "")

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.LocalNodeName != "project-host" {
		t.Fatalf("expected default project host, got %q", config.LocalNodeName)
	}
}

func TestLoadNormalizesBootstrapUsername(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TOOLHUB_BOOTSTRAP_USERNAME", " LiuJH273 ")

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.BootstrapUsername != "liujh273" {
		t.Fatalf("expected normalized bootstrap username, got %q", config.BootstrapUsername)
	}
}

func TestLoadDefersInvalidBootstrapUsernameUntilFirstAccountCreation(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TOOLHUB_BOOTSTRAP_USERNAME", "bad@example.com")

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.BootstrapUsername != "bad@example.com" {
		t.Fatalf("expected deferred bootstrap username, got %q", config.BootstrapUsername)
	}
}

func TestLoadRejectsInsecureXiapingBaseURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("XIAPING_BASE_URL", "http://127.0.0.1:8080")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "XIAPING_BASE_URL") {
		t.Fatalf("expected insecure Xiaping base URL error, got %v", err)
	}
}

func TestLoadRejectsUnsafeBridgeAndManagedUser(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TOOLHUB_BRIDGE_SOCKET", "relative.sock")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TOOLHUB_BRIDGE_SOCKET") {
		t.Fatalf("expected unsafe Bridge socket error, got %v", err)
	}

	setRequiredEnv(t)
	t.Setenv("TOOLHUB_MANAGED_USERNAME", "Root User")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TOOLHUB_MANAGED_USERNAME") {
		t.Fatalf("expected invalid managed username error, got %v", err)
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TOOLHUB_DATABASE_URL", "postgres://toolhub:test@localhost/toolhub")
	t.Setenv("TOOLHUB_MASTER_KEY", strings.Repeat("k", 32))
	t.Setenv("TOOLHUB_BRIDGE_HMAC_KEY", strings.Repeat("b", 32))
	t.Setenv("TOOLHUB_TIMEZONE", "UTC")
	t.Setenv("TOOLHUB_MANAGED_USERNAME", "toolhub")
	t.Setenv("TOOLHUB_BRIDGE_SOCKET", "/run/toolhub-bridge/bridge.sock")
}
