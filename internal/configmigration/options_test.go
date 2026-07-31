package configmigration

import (
	"os"
	"strings"
	"testing"
)

func TestLoadOptionsDefaultsToDryRun(t *testing.T) {
	environment := map[string]string{
		"TOOLHUB_LEGACY_DATABASE_URL": "postgres://legacy.invalid/toolhub",
		"TOOLHUB_LEGACY_MASTER_KEY":   strings.Repeat("l", 32),
	}
	options, err := loadOptions(nil, mapLookup(environment), nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.Apply || options.LegacyDatabaseURL == "" || len(options.LegacyMasterKey) != 32 {
		t.Fatalf("unexpected dry-run options: %+v", options)
	}
}

func TestLoadOptionsRequiresReviewedFingerprintForApply(t *testing.T) {
	environment := requiredApplyEnvironment()
	_, err := loadOptions([]string{"--apply"}, mapLookup(environment), nil)
	if err == nil {
		t.Fatal("apply without a reviewed fingerprint was accepted")
	}
	code, message := PublicError(err)
	if code != "invalid_options" || strings.Contains(message, "TOOLHUB_") || strings.Contains(message, environment["TOOLHUB_MASTER_KEY"]) {
		t.Fatalf("unsafe option error: code=%s message=%q", code, message)
	}
}

func TestLoadOptionsAcceptsCompleteApplyConfiguration(t *testing.T) {
	environment := requiredApplyEnvironment()
	options, err := loadOptions([]string{"--apply", "--expect-source-fingerprint", strings.Repeat("a", 64)}, mapLookup(environment), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer options.ClearKeys()
	if !options.Apply || options.Destination.RelayPort != 6276 || options.Destination.BootstrapUsername != "admin" {
		t.Fatalf("unexpected apply options: %+v", options)
	}
}

func TestReadRootKeyFileRequiresRootOnlyPermissions(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root ownership check requires a root test process")
	}
	path := t.TempDir() + "/legacy.key"
	if err := os.WriteFile(path, []byte(strings.Repeat("k", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := readRootKeyFile(path)
	if err != nil || len(body) != 32 {
		t.Fatalf("root-only key file rejected: bytes=%d err=%v", len(body), err)
	}
	clear(body)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRootKeyFile(path); err == nil {
		t.Fatal("group/world-readable key file was accepted")
	}
	link := t.TempDir() + "/legacy-link.key"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readRootKeyFile(link); err == nil {
		t.Fatal("symbolic-link key file was accepted")
	}
}

func TestLoadOptionsRejectsTwoLegacyKeySourcesWithoutLeakingValues(t *testing.T) {
	environment := map[string]string{
		"TOOLHUB_LEGACY_DATABASE_URL":    "postgres://user:do-not-print@legacy.invalid/toolhub",
		"TOOLHUB_LEGACY_MASTER_KEY":      strings.Repeat("x", 32),
		"TOOLHUB_LEGACY_MASTER_KEY_FILE": "/root/do-not-print",
	}
	_, err := loadOptions(nil, mapLookup(environment), func(string) ([]byte, error) {
		t.Fatal("key file reader must not run when both sources are configured")
		return nil, nil
	})
	if err == nil {
		t.Fatal("two legacy key sources were accepted")
	}
	_, message := PublicError(err)
	for _, forbidden := range environment {
		if forbidden != "" && strings.Contains(message, forbidden) {
			t.Fatalf("option error leaked configured value: %q", message)
		}
	}
}

func requiredApplyEnvironment() map[string]string {
	return map[string]string{
		"TOOLHUB_LEGACY_DATABASE_URL": "postgres://legacy.invalid/toolhub",
		"TOOLHUB_LEGACY_MASTER_KEY":   strings.Repeat("l", 32),
		"TOOLHUB_DATABASE_URL":        "postgres://destination.invalid/toolhub2",
		"TOOLHUB_MASTER_KEY":          strings.Repeat("d", 32),
		"TOOLHUB_BOOTSTRAP_USERNAME":  "admin",
		"TOOLHUB_BOOTSTRAP_PASSWORD":  "correct horse battery staple",
		"TOOLHUB_LOCAL_NODE_NAME":     "migration-test",
		"TOOLHUB_MANAGED_USERNAME":    "root",
		"TOOLHUB_TIMEZONE":            "Asia/Shanghai",
		"TOOLHUB_RELAY_PORT":          "6276",
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
