//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKeyRequiresRootOnlyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hmac.key")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 32)), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(path); err == nil {
		t.Fatal("expected world-readable key rejection")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, err := loadKey(path); err == nil {
			t.Fatal("expected non-root-owned key rejection")
		}
		return
	}
	key, err := loadKey(path)
	if err != nil || len(key) != 32 {
		t.Fatalf("key length=%d err=%v", len(key), err)
	}
}

func TestPackagedInstallerGeneratesBridgeCompatibleKey(t *testing.T) {
	installer := readPackagingFile(t, "install-toolhub-services.sh")
	if !strings.Contains(installer, "openssl rand -hex 32 > /etc/toolhub-bridge/hmac.key") {
		t.Fatal("installer must generate the 64-hex key accepted by the Bridge protocol")
	}
	if strings.Contains(installer, "openssl rand -base64") {
		t.Fatal("installer must not generate an unsupported base64 Bridge key")
	}
}

func TestPackagedBridgeAllowsMissingSaltMinionStaging(t *testing.T) {
	unit := readPackagingFile(t, "toolhub-bridge.service")
	if !strings.Contains(unit, "-/var/cache/salt/minion/toolhub-staging") {
		t.Fatal("Salt minion staging must be optional on Salt Master-only hosts")
	}
}

func TestPackagedBridgePreservesDockerMountedRuntimeDirectory(t *testing.T) {
	unit := readPackagingFile(t, "toolhub-bridge.service")
	if !strings.Contains(unit, "RuntimeDirectoryPreserve=yes") {
		t.Fatal("Bridge restarts must preserve the Docker-mounted runtime directory inode")
	}
}

func TestPackagedBridgeBindsOnlyTheManagedHomeWritable(t *testing.T) {
	unit := readPackagingFile(t, "toolhub-bridge.service")
	if !strings.Contains(unit, "ProtectHome=tmpfs") {
		t.Fatal("Bridge must hide homes that do not belong to the managed user")
	}
	if !strings.Contains(unit, "BindPaths=@TOOLHUB_MANAGED_HOME@") {
		t.Fatal("Bridge must bind the selected managed home into its private namespace")
	}
	if strings.Contains(unit, "ProtectHome=read-only") {
		t.Fatal("ProtectHome=read-only prevents the managed-home write allowlist from taking effect")
	}
}

func readPackagingFile(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "packaging", "systemd", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
