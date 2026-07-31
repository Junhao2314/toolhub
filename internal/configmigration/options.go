package configmigration

import (
	"encoding/base64"
	"errors"
	"flag"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/security"
)

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Options struct {
	Apply                     bool
	ExpectedSourceFingerprint string
	ReportJSONPath            string
	LegacyDatabaseURL         string
	LegacyMasterKey           []byte
	Destination               DestinationOptions
}

type DestinationOptions struct {
	DatabaseURL       string
	MasterKey         []byte
	BootstrapUsername string
	BootstrapPassword string
	LocalNodeName     string
	ManagedUsername   string
	Timezone          string
	RelayPort         int
}

func LoadOptions(args []string) (Options, error) {
	return loadOptions(args, os.LookupEnv, readRootKeyFile)
}

func loadOptions(args []string, lookup func(string) (string, bool), readKeyFile func(string) ([]byte, error)) (Options, error) {
	flags := flag.NewFlagSet("toolhub-config-migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options Options
	flags.BoolVar(&options.Apply, "apply", false, "apply the reviewed migration")
	flags.StringVar(&options.ExpectedSourceFingerprint, "expect-source-fingerprint", "", "reviewed source fingerprint")
	flags.StringVar(&options.ReportJSONPath, "report-json", "", "write a mode-0600 redacted JSON report")
	if err := flags.Parse(args); err != nil {
		return Options{}, migrationError("invalid_options", "migration flags are invalid", err)
	}
	if flags.NArg() != 0 {
		return Options{}, migrationError("invalid_options", "positional arguments are not accepted", nil)
	}
	options.ExpectedSourceFingerprint = strings.TrimSpace(options.ExpectedSourceFingerprint)
	options.ReportJSONPath = strings.TrimSpace(options.ReportJSONPath)
	if options.ExpectedSourceFingerprint != "" && !sha256Pattern.MatchString(options.ExpectedSourceFingerprint) {
		return Options{}, migrationError("invalid_options", "expected source fingerprint must be a lowercase SHA-256 value", nil)
	}
	if options.Apply && options.ExpectedSourceFingerprint == "" {
		return Options{}, migrationError("invalid_options", "apply requires a reviewed source fingerprint", nil)
	}

	options.LegacyDatabaseURL = envValue(lookup, "TOOLHUB_LEGACY_DATABASE_URL")
	if options.LegacyDatabaseURL == "" {
		return Options{}, migrationError("invalid_options", "legacy database URL is required", nil)
	}
	legacyKeyValue := envValue(lookup, "TOOLHUB_LEGACY_MASTER_KEY")
	legacyKeyPath := envValue(lookup, "TOOLHUB_LEGACY_MASTER_KEY_FILE")
	if (legacyKeyValue == "") == (legacyKeyPath == "") {
		return Options{}, migrationError("invalid_options", "configure exactly one legacy master-key source", nil)
	}
	if legacyKeyPath != "" {
		key, err := readKeyFile(legacyKeyPath)
		if err != nil {
			return Options{}, migrationError("invalid_options", "legacy master-key file is not a root-only regular file", err)
		}
		legacyKeyValue = string(key)
		clear(key)
	}
	var err error
	options.LegacyMasterKey, err = parseMasterKey(legacyKeyValue)
	if err != nil {
		return Options{}, migrationError("invalid_options", "legacy master key must contain exactly 32 bytes", err)
	}

	if !options.Apply {
		return options, nil
	}
	options.Destination.DatabaseURL = envValue(lookup, "TOOLHUB_DATABASE_URL")
	destinationKeyValue := envValue(lookup, "TOOLHUB_MASTER_KEY")
	options.Destination.BootstrapUsername = envValue(lookup, "TOOLHUB_BOOTSTRAP_USERNAME")
	options.Destination.BootstrapPassword = lookupValue(lookup, "TOOLHUB_BOOTSTRAP_PASSWORD")
	options.Destination.LocalNodeName = envValue(lookup, "TOOLHUB_LOCAL_NODE_NAME")
	options.Destination.ManagedUsername = envValue(lookup, "TOOLHUB_MANAGED_USERNAME")
	options.Destination.Timezone = envValue(lookup, "TOOLHUB_TIMEZONE")
	relayPortValue := envValue(lookup, "TOOLHUB_RELAY_PORT")
	if options.Destination.DatabaseURL == "" || destinationKeyValue == "" || options.Destination.BootstrapUsername == "" || options.Destination.BootstrapPassword == "" || options.Destination.LocalNodeName == "" || options.Destination.ManagedUsername == "" || options.Destination.Timezone == "" || relayPortValue == "" {
		return Options{}, migrationError("invalid_options", "all destination bootstrap settings are required for apply", nil)
	}
	options.Destination.MasterKey, err = parseMasterKey(destinationKeyValue)
	if err != nil {
		return Options{}, migrationError("invalid_options", "destination master key must contain exactly 32 bytes", err)
	}
	options.Destination.BootstrapUsername, err = security.NormalizeUsername(options.Destination.BootstrapUsername)
	if err != nil {
		return Options{}, migrationError("invalid_options", "destination bootstrap username is invalid", err)
	}
	if err := security.ValidatePassword(options.Destination.BootstrapPassword); err != nil {
		return Options{}, migrationError("invalid_options", "destination bootstrap password is invalid", err)
	}
	if len(options.Destination.LocalNodeName) > 120 {
		return Options{}, migrationError("invalid_options", "destination local node name is invalid", nil)
	}
	if err := bridgeprotocol.ValidateManagedUsername(options.Destination.ManagedUsername); err != nil {
		return Options{}, migrationError("invalid_options", "destination managed username is invalid", err)
	}
	if _, err := time.LoadLocation(options.Destination.Timezone); err != nil {
		return Options{}, migrationError("invalid_options", "destination timezone is invalid", err)
	}
	options.Destination.RelayPort, err = strconv.Atoi(relayPortValue)
	if err != nil || options.Destination.RelayPort < 1 || options.Destination.RelayPort > 65535 {
		return Options{}, migrationError("invalid_options", "destination relay port must be between 1 and 65535", err)
	}
	return options, nil
}

func (o *Options) ClearKeys() {
	clear(o.LegacyMasterKey)
	clear(o.Destination.MasterKey)
}

func envValue(lookup func(string) (string, bool), name string) string {
	value, _ := lookup(name)
	return strings.TrimSpace(value)
}

func lookupValue(lookup func(string) (string, bool), name string) string {
	value, _ := lookup(name)
	return value
}

func parseMasterKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(value) == 32 {
		return []byte(value), nil
	}
	return nil, errors.New("invalid master key length")
}

func readRootKeyFile(path string) ([]byte, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe key file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(linkInfo, info) || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 {
		return nil, errors.New("unsafe key file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return nil, errors.New("key file is not root-owned")
	}
	body, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return nil, err
	}
	if len(body) > 4096 {
		clear(body)
		return nil, errors.New("key file is too large")
	}
	return body, nil
}
