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

func TestPackagedRelaySupportsDynamicManagedHomeMCPs(t *testing.T) {
	unit := readPackagingFile(t, "toolhub-mcpm-relay.service")
	for _, required := range []string{
		"ProtectHome=tmpfs",
		"BindPaths=@TOOLHUB_MANAGED_HOME@",
		"Environment=HOME=@TOOLHUB_MANAGED_HOME@",
		"WorkingDirectory=/tmp",
		"ExecStartPre=/usr/libexec/toolhub-relay-port-check",
		"ExecStartPost=/usr/bin/bash -c",
		"/dev/tcp/127.0.0.1/${TOOLHUB_RELAY_PORT}",
		"GET /mcp HTTP/1.1",
		"KillMode=control-group",
		"KillSignal=SIGTERM",
		"TimeoutStopSec=20s",
		"FinalKillSignal=SIGKILL",
		"TimeoutStartSec=150s",
		"CapabilityBoundingSet=",
		"PrivateDevices=yes",
		"ProtectControlGroups=yes",
		"ProtectKernelModules=yes",
		"ProtectKernelTunables=yes",
		"SystemCallArchitectures=native",
		"StartLimitBurst=3",
	} {
		if !strings.Contains(unit, required) {
			t.Fatalf("relay unit is missing %q", required)
		}
	}
	if strings.Contains(unit, "MemoryDenyWriteExecute=yes") {
		t.Fatal("relay unit must allow executable anonymous memory required by supported Node/V8 MCPs")
	}
	if strings.Contains(unit, "ProtectHome=read-only") {
		t.Fatal("relay unit must expose the selected managed home through an explicit bind")
	}
}

func TestPackagedInstallerUsesInstallationRuntimeArguments(t *testing.T) {
	installer := readPackagingFile(t, "install-toolhub-services.sh")
	if !strings.Contains(installer, "MANAGED_USER TOOLHUB_REPOSITORY [MANAGED_GROUP] [BRIDGE_GROUP]") {
		t.Fatal("installer must require the ToolHub repository root")
	}
	for _, required := range []string{
		"toolhub_repository=\"${2:?$usage}\"",
		"mcpm_repository=\"$toolhub_repository/mcpm\"",
		"canonical_repository()",
		"readlink -f -- \"$repository\"",
		"[ \"$(stat -c %u -- \"$repository\")\" -eq 0 ]",
		"bridge_binary=\"$toolhub_repository/bin/toolhub-bridge\"",
		"mcpm_launcher=\"$mcpm_repository/.venv/bin/mcpm\"",
		"site_packages=\"$(find \"$mcpm_repository/.venv/lib\"",
		"runtime_root=\"$install_root/mcpm\"",
		"install -m 0755 \"$bridge_binary\" \"$bridge_install\"",
		"toolhub-mcpm-launcher.sh",
		"resolved_interpreter=\"$(readlink -f -- \"$mcpm_python\")\"",
		"/root/.local/share/uv/python/*",
		"/usr/bin/timeout 5s /usr/sbin/runuser -u \"$managed_user\" -- \"$mcpm_launcher\" toolhub contract --json",
		"s|@MCPM_INTERPRETER@|$resolved_interpreter|g",
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("installer is missing %q", required)
		}
	}
	for _, forbidden := range []string{"/usr/local/sbin/toolhub-bridge", "/usr/local/sbin/toolhub-relay-port-check", "/usr/bin/mcpm"} {
		if strings.Contains(installer, forbidden) {
			t.Fatalf("installer still contains legacy path %q", forbidden)
		}
	}

	bridge := readPackagingFile(t, "toolhub-bridge.service")
	if !strings.Contains(bridge, "ExecStart=/usr/libexec/toolhub-bridge") ||
		!strings.Contains(bridge, "BindReadOnlyPaths=/usr/libexec/toolhub-mcpm /var/lib/toolhub-bridge/mcpm @MCPM_INTERPRETER@") {
		t.Fatal("Bridge unit must use installation-owned executables and runtime binds")
	}
	relay := readPackagingFile(t, "toolhub-mcpm-relay.service")
	for _, required := range []string{
		"WorkingDirectory=/tmp",
		"ExecStart=/usr/libexec/toolhub-mcpm",
		"BindReadOnlyPaths=/usr/libexec/toolhub-mcpm /var/lib/toolhub-bridge/mcpm @MCPM_INTERPRETER@",
		"--toolhub-routing",
		"--toolhub-admin-socket",
		"--toolhub-observation-limit 100000",
	} {
		if !strings.Contains(relay, required) {
			t.Fatalf("relay unit is missing %q", required)
		}
	}
}

func TestPackagedInstallerRendersCanonicalRelayHome(t *testing.T) {
	installer := readPackagingFile(t, "install-toolhub-services.sh")
	if !strings.Contains(installer, `canonical_home="$(readlink -f -- "$managed_home")"`) {
		t.Fatal("installer must resolve the managed home canonically")
	}
	if strings.Count(installer, `s|@TOOLHUB_MANAGED_HOME@|$managed_home|g`) != 2 {
		t.Fatal("installer must render the canonical managed home into both packaged units")
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
