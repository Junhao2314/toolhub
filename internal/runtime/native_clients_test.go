package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeClientInspectionUsesGuardedUserBinary(t *testing.T) {
	home := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	path := filepath.Join(home, ".local", "bin", "claude")
	t.Setenv("TOOLHUB_NATIVE_TEST_LEAK", "must-not-be-inherited")
	writeNativeExecutable(t, path, fmt.Sprintf(`#!/bin/sh
test "$#" -eq 1 || exit 21
test "$1" = "--version" || exit 22
test "$(id -u)" = "%d" || exit 23
test "$(id -g)" = "%d" || exit 24
test -z "${TOOLHUB_NATIVE_TEST_LEAK+x}" || exit 25
printf '2.1.232 (Claude Code)\n'
`, user.UID, user.GID))

	result := (nativeClientInspector{timeout: time.Second, maxOutputBytes: 1024, systemPrefixes: []string{}}).Inspect(context.Background(), user, "claude")
	if result.ClientKind != "claude" || result.Version != "2.1.232" || !result.Supported || result.ErrorCode != "" {
		t.Fatalf("inspection=%+v", result)
	}
}

func TestNativeClientInspectionClearsSupplementaryGroups(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to verify credential drop")
	}
	const nobody = 65534
	home := t.TempDir()
	if err := os.Chmod(filepath.Dir(home), 0755); err != nil {
		t.Fatal(err)
	}
	user := ManagedUser{Name: "nobody", Home: home, UID: nobody, GID: nobody}
	path := filepath.Join(home, ".local", "bin", "claude")
	writeNativeExecutable(t, path, fmt.Sprintf("#!/bin/sh\ntest \"$(id -G)\" = \"%d\" || exit 31\nprintf '2.1.232 (Claude Code)\\n'\n", nobody))
	if err := filepath.Walk(home, func(path string, _ os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Chown(path, nobody, nobody)
	}); err != nil {
		t.Fatal(err)
	}

	result := (nativeClientInspector{timeout: time.Second, maxOutputBytes: 1024, systemPrefixes: []string{}}).Inspect(context.Background(), user, "claude")
	if !result.Supported {
		t.Fatalf("inspection retained Bridge supplementary groups: %+v", result)
	}
}

func TestNativeClientInspectionSupportsNVMAndContainedSymlink(t *testing.T) {
	home := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	versionRoot := filepath.Join(home, ".nvm", "versions", "node", "v22.18.0")
	prefix := filepath.Join(versionRoot, "bin")
	realPath := filepath.Join(versionRoot, "lib", "node_modules", "@openai", "codex", "bin", "codex.js")
	writeNativeExecutable(t, realPath, "#!/bin/sh\nprintf 'codex-cli 0.147.0\\n'\n")
	if err := os.MkdirAll(prefix, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "lib", "node_modules", "@openai", "codex", "bin", "codex.js"), filepath.Join(prefix, "codex")); err != nil {
		t.Fatal(err)
	}

	result := (nativeClientInspector{timeout: time.Second, maxOutputBytes: 1024, systemPrefixes: []string{}}).Inspect(context.Background(), user, "codex")
	if result.Version != "0.147.0" || !result.Supported {
		t.Fatalf("inspection=%+v", result)
	}
}

func TestNativeClientInspectionRejectsAmbiguousMultipleInstallations(t *testing.T) {
	home := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	writeNativeExecutable(t, filepath.Join(home, ".local", "bin", "claude"), "#!/bin/sh\nprintf '2.1.232 (Claude Code)\\n'\n")
	writeNativeExecutable(t, filepath.Join(home, ".volta", "bin", "claude"), "#!/bin/sh\nprintf '2.1.231 (Claude Code)\\n'\n")

	result := (nativeClientInspector{timeout: time.Second, maxOutputBytes: 1024, systemPrefixes: []string{}}).Inspect(context.Background(), user, "claude")
	if result.Supported || result.Version != "" || result.ErrorCode != "native_client_resolution_ambiguous" {
		t.Fatalf("multiple native client installations were not rejected: %+v", result)
	}
}

func TestNativeClientInspectionDeduplicatesAliasesOfOneExecutable(t *testing.T) {
	home := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	localPath := filepath.Join(home, ".local", "bin", "codex")
	voltaPath := filepath.Join(home, ".volta", "bin", "codex")
	writeNativeExecutable(t, localPath, "#!/bin/sh\nprintf 'codex-cli 0.147.0\\n'\n")
	if err := os.MkdirAll(filepath.Dir(voltaPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(localPath, voltaPath); err != nil {
		t.Fatal(err)
	}

	result := (nativeClientInspector{timeout: time.Second, maxOutputBytes: 1024, systemPrefixes: []string{}}).Inspect(context.Background(), user, "codex")
	if result.Version != "0.147.0" || !result.Supported || result.ErrorCode != "" {
		t.Fatalf("aliases of one executable were treated as separate installations: %+v", result)
	}
}

func TestNativeClientInspectionExecutesPinnedInodeAfterPathSwap(t *testing.T) {
	home := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	path := filepath.Join(home, ".local", "bin", "claude")
	writeNativeExecutable(t, path, "#!/bin/sh\nprintf '2.1.232 (Claude Code)\\n'\n")

	inspector := nativeClientInspector{timeout: time.Second, maxOutputBytes: 1024, systemPrefixes: []string{}}
	locations := inspector.approvedLocations(user)
	executable, err := openNativeClient(locations[0], "claude")
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()

	if err := os.Rename(path, path+".approved"); err != nil {
		t.Fatal(err)
	}
	writeNativeExecutable(t, path, "#!/bin/sh\nprintf '0.0.1 (Claude Code)\\n'\n")

	result := inspector.inspectVersion(context.Background(), user, "claude", executable)
	if result.Version != "2.1.232" || !result.Supported {
		t.Fatalf("inspection followed replacement path instead of pinned inode: %+v", result)
	}
}

func TestNativeClientInspectionRejectsSymlinkOutsideApprovedPrefix(t *testing.T) {
	home := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	outside := filepath.Join(t.TempDir(), "claude")
	writeNativeExecutable(t, outside, "#!/bin/sh\nprintf '2.9.0 (Claude Code)\\n'\n")
	prefix := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(prefix, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(prefix, "claude")); err != nil {
		t.Fatal(err)
	}

	result := (nativeClientInspector{timeout: time.Second, maxOutputBytes: 1024, systemPrefixes: []string{}}).Inspect(context.Background(), user, "claude")
	if result.Supported || result.Version != "" || result.ErrorCode != "native_client_path_unsafe" {
		t.Fatalf("escaped symlink inspection=%+v", result)
	}
}

func TestNativeClientInspectionRejectsUnsafeFileTypeAndMode(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.MkdirAll(path, 0755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "not executable", setup: func(t *testing.T, path string) {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("2.1.232\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
			path := filepath.Join(home, ".local", "bin", "claude")
			test.setup(t, path)
			result := (nativeClientInspector{timeout: time.Second, maxOutputBytes: 1024, systemPrefixes: []string{}}).Inspect(context.Background(), user, "claude")
			if result.Supported || result.Version != "" || result.ErrorCode != "native_client_path_unsafe" {
				t.Fatalf("unsafe candidate inspection=%+v", result)
			}
		})
	}
}

func TestNativeClientInspectionEnforcesVersionFloor(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantVersion string
	}{
		{name: "lower patch", output: "codex-cli 0.146.9", wantVersion: "0.146.9"},
		{name: "floor prerelease", output: "codex-cli 0.147.0-alpha.1", wantVersion: "0.147.0-alpha.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
			writeNativeExecutable(t, filepath.Join(home, ".volta", "bin", "codex"), "#!/bin/sh\nprintf '"+test.output+"\\n'\n")
			result := (nativeClientInspector{timeout: time.Second, maxOutputBytes: 1024, systemPrefixes: []string{}}).Inspect(context.Background(), user, "codex")
			if result.Version != test.wantVersion || result.Supported || result.ErrorCode != "native_client_version_unsupported" {
				t.Fatalf("inspection=%+v", result)
			}
		})
	}
}

func TestNativeClientInspectionBoundsExecution(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		timeout    time.Duration
		maxOutput  int
		wantReason string
	}{
		{name: "timeout", script: "#!/bin/sh\n/bin/sleep 1 &\nwait\n", timeout: 25 * time.Millisecond, maxOutput: 1024, wantReason: "native_client_timeout"},
		{name: "output", script: "#!/bin/sh\nhead -c 2048 /dev/zero | tr '\\000' x\n", timeout: time.Second, maxOutput: 64, wantReason: "native_client_output_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
			writeNativeExecutable(t, filepath.Join(home, ".local", "bin", "claude"), test.script)
			started := time.Now()
			result := (nativeClientInspector{timeout: test.timeout, maxOutputBytes: test.maxOutput, systemPrefixes: []string{}}).Inspect(context.Background(), user, "claude")
			if result.Supported || result.ErrorCode != test.wantReason {
				t.Fatalf("inspection=%+v", result)
			}
			if test.name == "timeout" && time.Since(started) > 500*time.Millisecond {
				t.Fatalf("inspection timeout took %s", time.Since(started))
			}
		})
	}
}

func TestNativeClientInspectionKillsTimedOutProcessGroup(t *testing.T) {
	home := t.TempDir()
	user := ManagedUser{Name: "operator", Home: home, UID: os.Getuid(), GID: os.Getgid()}
	marker := filepath.Join(home, "orphan-marker")
	writeNativeExecutable(t, filepath.Join(home, ".local", "bin", "claude"), "#!/bin/sh\n(/bin/sleep 0.3; printf orphan > \"$HOME/orphan-marker\") &\nwait\n")

	result := (nativeClientInspector{timeout: 25 * time.Millisecond, maxOutputBytes: 1024, systemPrefixes: []string{}}).Inspect(context.Background(), user, "claude")
	if result.ErrorCode != "native_client_timeout" {
		t.Fatalf("inspection=%+v", result)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("timed-out native client left a running child: stat err=%v", err)
	}
}

func writeNativeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}
