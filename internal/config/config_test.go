package config

import (
	"strings"
	"testing"
)

func TestLoadUsesConfiguredProjectHostName(t *testing.T) {
	t.Setenv("TOOLHUB_DATABASE_URL", "postgres://toolhub:test@localhost/toolhub")
	t.Setenv("TOOLHUB_MASTER_KEY", strings.Repeat("k", 32))
	t.Setenv("TOOLHUB_TIMEZONE", "UTC")
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
	t.Setenv("TOOLHUB_DATABASE_URL", "postgres://toolhub:test@localhost/toolhub")
	t.Setenv("TOOLHUB_MASTER_KEY", strings.Repeat("k", 32))
	t.Setenv("TOOLHUB_TIMEZONE", "UTC")
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
	t.Setenv("TOOLHUB_DATABASE_URL", "postgres://toolhub:test@localhost/toolhub")
	t.Setenv("TOOLHUB_MASTER_KEY", strings.Repeat("k", 32))
	t.Setenv("TOOLHUB_TIMEZONE", "UTC")
	t.Setenv("TOOLHUB_BOOTSTRAP_ADMIN_USERNAME", " LiuJH273 ")

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.BootstrapAdminUsername != "liujh273" {
		t.Fatalf("expected normalized bootstrap username, got %q", config.BootstrapAdminUsername)
	}
}

func TestLoadRejectsInvalidBootstrapUsername(t *testing.T) {
	t.Setenv("TOOLHUB_DATABASE_URL", "postgres://toolhub:test@localhost/toolhub")
	t.Setenv("TOOLHUB_MASTER_KEY", strings.Repeat("k", 32))
	t.Setenv("TOOLHUB_TIMEZONE", "UTC")
	t.Setenv("TOOLHUB_BOOTSTRAP_ADMIN_USERNAME", "bad@example.com")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TOOLHUB_BOOTSTRAP_ADMIN_USERNAME") {
		t.Fatalf("expected invalid bootstrap username error, got %v", err)
	}
}

func TestLoadRejectsInsecureXiapingBaseURL(t *testing.T) {
	t.Setenv("TOOLHUB_DATABASE_URL", "postgres://toolhub:test@localhost/toolhub")
	t.Setenv("TOOLHUB_MASTER_KEY", strings.Repeat("k", 32))
	t.Setenv("TOOLHUB_TIMEZONE", "UTC")
	t.Setenv("XIAPING_BASE_URL", "http://127.0.0.1:8080")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "XIAPING_BASE_URL") {
		t.Fatalf("expected insecure Xiaping base URL error, got %v", err)
	}
}
