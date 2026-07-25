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
