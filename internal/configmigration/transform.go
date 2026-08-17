package configmigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/skills"
	"github.com/Junhao2314/toolhub/internal/store"
)

type Prepared struct {
	Import store.ConfigImportInput
	Report Report
}

type normalizedMCP struct {
	legacy   LegacyMCPServer
	input    store.MCPInput
	baseName string
	desired  bool
}

const maxMigratedSecretCiphertextBytes = 1 << 20

func Prepare(snapshot Snapshot, legacyCipher *security.Cipher) (Prepared, error) {
	if legacyCipher == nil {
		return Prepared{}, migrationError("invalid_options", "legacy master key is required", nil)
	}
	if err := validateMigrationLedger(snapshot.Migrations); err != nil {
		return Prepared{}, err
	}
	if _, err := cron.ParseStandard(strings.TrimSpace(snapshot.UpdateCron)); err != nil {
		return Prepared{}, migrationError("source_contract_mismatch", "legacy update schedule is not a valid five-field cron expression", err)
	}
	if _, err := time.LoadLocation(strings.TrimSpace(snapshot.Timezone)); err != nil {
		return Prepared{}, migrationError("source_contract_mismatch", "legacy update timezone is invalid", err)
	}

	input := store.ConfigImportInput{
		SourceDatabaseIdentity: snapshot.Identity.Hash(),
		UpdateCron:             strings.TrimSpace(snapshot.UpdateCron),
		Timezone:               strings.TrimSpace(snapshot.Timezone),
	}
	skillIDs := map[string]bool{}
	skillsByID := append([]LegacySkill(nil), snapshot.Skills...)
	sort.Slice(skillsByID, func(i, j int) bool { return skillsByID[i].ID < skillsByID[j].ID })
	slugs := map[string]string{}
	for _, legacy := range skillsByID {
		item, err := transformSkill(legacy)
		if err != nil {
			return Prepared{}, err
		}
		if previous := slugs[item.Package.Slug]; previous != "" {
			return Prepared{}, migrationError("source_contract_mismatch", fmt.Sprintf("legacy Skills %s and %s normalize to the same slug", previous, legacy.ID), nil)
		}
		slugs[item.Package.Slug] = legacy.ID
		skillIDs[legacy.ID] = true
		input.Skills = append(input.Skills, item)
	}

	secretByID := make(map[string]LegacySecret, len(snapshot.Secrets))
	for _, secret := range snapshot.Secrets {
		if uuid.Validate(secret.ID) != nil || (secret.Kind != "mcp-env" && secret.Kind != "mcp-header") || len(secret.Ciphertext) <= 40 || len(secret.Ciphertext) > maxMigratedSecretCiphertextBytes {
			return Prepared{}, migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP Secret record %s is invalid", secret.ID), nil)
		}
		if _, duplicate := secretByID[secret.ID]; duplicate {
			return Prepared{}, migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP Secret record %s is duplicated", secret.ID), nil)
		}
		secretByID[secret.ID] = secret
	}

	desiredMCP := idSet(snapshot.MCPDesired["claude"], snapshot.MCPDesired["codex"])
	normalized := make([]normalizedMCP, 0, len(snapshot.MCPServers))
	serverIDs := map[string]bool{}
	referenceUse := map[string]string{}
	for _, legacy := range snapshot.MCPServers {
		if uuid.Validate(legacy.ID) != nil || serverIDs[legacy.ID] {
			return Prepared{}, migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP definition %s has an invalid or duplicate ID", legacy.ID), nil)
		}
		serverIDs[legacy.ID] = true
		item, converted, err := normalizeMCPDefinition(legacy)
		if err != nil {
			return Prepared{}, err
		}
		if converted {
			input.TransportConversionCount++
		}
		if err := validateMCPReferences(legacy.ID, legacy.EnvRefs, "mcp-env", secretByID, referenceUse); err != nil {
			return Prepared{}, err
		}
		if err := validateMCPReferences(legacy.ID, legacy.HeaderRefs, "mcp-header", secretByID, referenceUse); err != nil {
			return Prepared{}, err
		}
		normalized = append(normalized, normalizedMCP{legacy: legacy, input: item, baseName: item.Name, desired: desiredMCP[legacy.ID]})
	}
	if len(referenceUse) != len(secretByID) {
		return Prepared{}, migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP Secret reference set is incomplete: records=%d references=%d", len(secretByID), len(referenceUse)), nil)
	}
	for _, id := range sortedKeys(secretByID) {
		secret := secretByID[id]
		plaintext, err := legacyCipher.Decrypt(secret.Ciphertext, secret.ID)
		if err != nil {
			return Prepared{}, migrationError("legacy_decrypt_failed", fmt.Sprintf("legacy MCP Secret record %s could not be authenticated", secret.ID), err)
		}
		clear(plaintext)
		input.Secrets = append(input.Secrets, store.ConfigImportSecret{LegacyID: secret.ID, Kind: secret.Kind, Ciphertext: append([]byte(nil), secret.Ciphertext...)})
	}

	normalizations, err := assignMCPNames(normalized)
	if err != nil {
		return Prepared{}, err
	}
	for _, item := range normalized {
		input.MCPServers = append(input.MCPServers, store.ConfigImportMCP{
			LegacyID:   item.legacy.ID,
			Input:      item.input,
			EnvRefs:    cloneStringMap(item.legacy.EnvRefs),
			HeaderRefs: cloneStringMap(item.legacy.HeaderRefs),
		})
	}
	sort.Slice(input.MCPServers, func(i, j int) bool { return input.MCPServers[i].LegacyID < input.MCPServers[j].LegacyID })
	input.MCPRenameCount = countRenames(normalizations)

	if err := validateDesiredMembership(snapshot, skillIDs, serverIDs); err != nil {
		return Prepared{}, err
	}
	claudeSkills := sortedUnique(snapshot.SkillDesired["claude"])
	codexSkills := sortedUnique(snapshot.SkillDesired["codex"])
	relayMCP := sortedUnique(append(append([]string(nil), snapshot.MCPDesired["claude"]...), snapshot.MCPDesired["codex"]...))
	input.Profiles = []store.ConfigImportProfile{
		{Name: "claude-skills", Description: "Claude Skill selection", LegacySkillIDs: claudeSkills},
		{Name: "codex-skills", Description: "Codex Skill selection", LegacySkillIDs: codexSkills},
	}
	input.RelayMCPServerIDs = relayMCP
	input.SourceFingerprint = sourceFingerprint(snapshot.Migrations, input)

	report := Report{
		SchemaVersion:     1,
		Status:            "validated",
		SourceFingerprint: input.SourceFingerprint,
		LegacyMigrations:  append([]int64(nil), snapshot.Migrations...),
		Skills:            len(input.Skills),
		MCPServers:        len(input.MCPServers),
		MCPSecrets:        len(input.Secrets),
		Profiles:          len(input.Profiles),
		TransportChanges:  input.TransportConversionCount,
		Renames:           input.MCPRenameCount,
		MCPNormalizations: normalizations,
		ProfileMemberships: []ProfileMembershipReport{
			{Name: "claude-skills", Skills: len(claudeSkills)},
			{Name: "codex-skills", Skills: len(codexSkills)},
		},
		RelayMCPServers: len(relayMCP),
		UpdateCron:      input.UpdateCron,
		Timezone:        input.Timezone,
	}
	return Prepared{Import: input, Report: report}, nil
}

func transformSkill(legacy LegacySkill) (store.ConfigImportSkill, error) {
	if uuid.Validate(legacy.ID) != nil || uuid.Validate(legacy.SourceID) != nil || uuid.Validate(legacy.VersionID) != nil || uuid.Validate(legacy.ArtifactID) != nil {
		return store.ConfigImportSkill{}, migrationError("source_contract_mismatch", fmt.Sprintf("legacy Skill %s has an invalid identifier graph", legacy.ID), nil)
	}
	if int64(len(legacy.Archive)) != legacy.SizeBytes || legacy.SizeBytes <= 0 {
		return store.ConfigImportSkill{}, migrationError("skill_artifact_invalid", fmt.Sprintf("legacy Skill %s archive size does not match its record", legacy.ID), nil)
	}
	rawSum := sha256.Sum256(legacy.Archive)
	rawSHA := hex.EncodeToString(rawSum[:])
	if rawSHA != legacy.ArtifactSHA256 || legacy.VersionSHA256 != legacy.ArtifactSHA256 {
		return store.ConfigImportSkill{}, migrationError("skill_artifact_invalid", fmt.Sprintf("legacy Skill %s archive hash does not match its records", legacy.ID), nil)
	}
	pkg, err := skills.ScanZIP(legacy.Archive, skills.DefaultLimits)
	if err != nil {
		return store.ConfigImportSkill{}, migrationError("skill_artifact_invalid", fmt.Sprintf("legacy Skill %s archive failed the generation-2 scanner", legacy.ID), err)
	}
	if pkg.SHA256 != legacy.ArtifactSHA256 {
		return store.ConfigImportSkill{}, migrationError("skill_artifact_invalid", fmt.Sprintf("legacy Skill %s canonical archive hash changed during rescan", legacy.ID), nil)
	}
	if pkg.Slug != legacy.Slug {
		return store.ConfigImportSkill{}, migrationError("skill_artifact_invalid", fmt.Sprintf("legacy Skill %s slug does not match its current package", legacy.ID), nil)
	}
	source, err := mapSkillSource(legacy, pkg.Name)
	if err != nil {
		return store.ConfigImportSkill{}, err
	}
	return store.ConfigImportSkill{
		LegacyID:         legacy.ID,
		LegacySourceID:   legacy.SourceID,
		LegacyVersionID:  legacy.VersionID,
		LegacyArtifactID: legacy.ArtifactID,
		Source:           source,
		Package:          pkg,
		Provenance: map[string]any{
			"originalSkillId":    legacy.ID,
			"originalSourceId":   legacy.SourceID,
			"originalVersionId":  legacy.VersionID,
			"originalArtifactId": legacy.ArtifactID,
			"originalSHA256":     legacy.ArtifactSHA256,
		},
	}, nil
}

func mapSkillSource(legacy LegacySkill, fallbackName string) (store.SourceInput, error) {
	name := strings.TrimSpace(legacy.SourceName)
	if name == "" {
		name = fallbackName
	}
	if name == "" || len(name) > 200 || containsControl(name) {
		return store.SourceInput{}, migrationError("unsupported_source_state", fmt.Sprintf("legacy Skill %s has an invalid source name", legacy.ID), nil)
	}
	commit := strings.TrimSpace(legacy.SourceCommit)
	if len(commit) > 200 || containsControl(commit) {
		return store.SourceInput{}, migrationError("unsupported_source_state", fmt.Sprintf("legacy Skill %s has invalid commit metadata", legacy.ID), nil)
	}
	source := store.SourceInput{
		Name:   name,
		Commit: commit,
		Metadata: map[string]any{
			"originalSourceId":   legacy.SourceID,
			"originalSourceKind": legacy.SourceKind,
		},
	}
	switch legacy.SourceKind {
	case "git":
		parsed, err := url.Parse(strings.TrimSpace(legacy.SourceURL))
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return store.SourceInput{}, migrationError("unsupported_source_state", fmt.Sprintf("legacy Skill %s has an invalid remote source", legacy.ID), err)
		}
		subdirectory := strings.TrimSpace(legacy.Subdirectory)
		if subdirectory != "" {
			clean := path.Clean(subdirectory)
			if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
				return store.SourceInput{}, migrationError("unsupported_source_state", fmt.Sprintf("legacy Skill %s has an invalid source subdirectory", legacy.ID), nil)
			}
			subdirectory = clean
		}
		source.Kind, source.URL, source.Subdirectory = "git", parsed.String(), subdirectory
	case "node":
		if strings.TrimSpace(legacy.SourceURL) != "" {
			return store.SourceInput{}, migrationError("unsupported_source_state", fmt.Sprintf("legacy Skill %s has unexpected node-source location metadata", legacy.ID), nil)
		}
		legacyPath := strings.TrimSpace(legacy.Subdirectory)
		if legacyPath != "" {
			clean := path.Clean(legacyPath)
			if !path.IsAbs(clean) || clean == "/" || clean != legacyPath || containsControl(legacyPath) || path.Base(clean) != legacy.Slug {
				return store.SourceInput{}, migrationError("unsupported_source_state", fmt.Sprintf("legacy Skill %s has invalid node-source location metadata", legacy.ID), nil)
			}
		}
		source.Kind = "local"
	case "upload":
		if strings.TrimSpace(legacy.SourceURL) != "" || strings.TrimSpace(legacy.Subdirectory) != "" {
			return store.SourceInput{}, migrationError("unsupported_source_state", fmt.Sprintf("legacy Skill %s has unexpected upload-source location metadata", legacy.ID), nil)
		}
		source.Kind = "zip"
	default:
		return store.SourceInput{}, migrationError("unsupported_source_state", fmt.Sprintf("legacy Skill %s uses unsupported source kind %s", legacy.ID, legacy.SourceKind), nil)
	}
	return source, nil
}

func normalizeMCPDefinition(legacy LegacyMCPServer) (store.MCPInput, bool, error) {
	if containsControl(legacy.Name) || containsControl(legacy.Transport) {
		return store.MCPInput{}, false, migrationError("mcp_definition_invalid", fmt.Sprintf("legacy MCP definition %s contains invalid control characters", legacy.ID), nil)
	}
	transport := strings.ToLower(strings.TrimSpace(legacy.Transport))
	converted := transport == "streamable-http"
	if converted {
		transport = "http"
	}
	input := store.MCPInput{
		Name:        strings.ToLower(strings.TrimSpace(legacy.Name)),
		Description: mcpServerDescription(legacy, transport),
		Transport:   transport,
		Command:     strings.TrimSpace(legacy.Command),
		Args:        append([]string(nil), legacy.Args...),
		URL:         strings.TrimSpace(legacy.URL),
	}
	normalized, err := store.NormalizeMCPInput(input)
	if err != nil {
		return store.MCPInput{}, false, migrationError("mcp_definition_invalid", fmt.Sprintf("legacy MCP definition %s is incompatible with generation 2", legacy.ID), err)
	}
	return normalized, converted, nil
}

// mcpServerDescription derives a neutral per-server description from the
// legacy definition instead of a fixed import notice.
func mcpServerDescription(legacy LegacyMCPServer, transport string) string {
	if transport == "http" || transport == "sse" {
		return "Remote " + strings.ToUpper(transport) + " MCP server at " + strings.TrimSpace(legacy.URL)
	}
	command := strings.TrimSpace(legacy.Command)
	if len(legacy.Args) > 0 {
		command += " " + strings.Join(legacy.Args, " ")
	}
	return "Local MCP server run via " + command
}

func validateMCPReferences(serverID string, refs map[string]string, wantKind string, secrets map[string]LegacySecret, used map[string]string) error {
	for key, id := range refs {
		if strings.TrimSpace(key) != key || key == "" || len(key) > 200 || containsControl(key) || uuid.Validate(id) != nil {
			return migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP definition %s has an invalid Secret reference", serverID), nil)
		}
		secret, ok := secrets[id]
		if !ok {
			return migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP definition %s references a missing Secret record", serverID), nil)
		}
		if secret.Kind != wantKind {
			return migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP definition %s references a wrong-kind Secret record", serverID), nil)
		}
		if previous := used[id]; previous != "" {
			return migrationError("source_contract_mismatch", fmt.Sprintf("legacy MCP Secret record %s is referenced more than once", id), nil)
		}
		used[id] = serverID
	}
	return nil
}

func assignMCPNames(items []normalizedMCP) ([]MCPNormalization, error) {
	groups := map[string][]int{}
	for index := range items {
		groups[items[index].baseName] = append(groups[items[index].baseName], index)
	}
	bases := sortedKeys(groups)
	used := map[string]bool{}
	var losers []int
	for _, base := range bases {
		indexes := groups[base]
		sort.Slice(indexes, func(i, j int) bool {
			left, right := items[indexes[i]], items[indexes[j]]
			if left.desired != right.desired {
				return left.desired
			}
			return left.legacy.ID < right.legacy.ID
		})
		items[indexes[0]].input.Name = base
		used[base] = true
		losers = append(losers, indexes[1:]...)
	}
	sort.Slice(losers, func(i, j int) bool {
		left, right := items[losers[i]], items[losers[j]]
		if left.baseName != right.baseName {
			return left.baseName < right.baseName
		}
		if left.desired != right.desired {
			return left.desired
		}
		return left.legacy.ID < right.legacy.ID
	})
	for _, index := range losers {
		item := &items[index]
		candidate := suffixedMCPName(item.baseName, "-"+item.input.Transport)
		if used[candidate] {
			compactID := strings.ReplaceAll(item.legacy.ID, "-", "")
			for length := 8; ; length += 4 {
				if length > len(compactID) {
					return nil, migrationError("mcp_definition_invalid", fmt.Sprintf("legacy MCP definition %s could not receive a unique normalized name", item.legacy.ID), nil)
				}
				candidate = suffixedMCPName(item.baseName, "-"+item.input.Transport+"-"+compactID[:length])
				if !used[candidate] {
					break
				}
			}
		}
		item.input.Name = candidate
		used[candidate] = true
	}
	result := make([]MCPNormalization, 0, len(items))
	for _, item := range items {
		result = append(result, MCPNormalization{
			LegacyID:            item.legacy.ID,
			OriginalName:        item.legacy.Name,
			NormalizedName:      item.input.Name,
			OriginalTransport:   item.legacy.Transport,
			NormalizedTransport: item.input.Transport,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LegacyID < result[j].LegacyID })
	return result, nil
}

func suffixedMCPName(base, suffix string) string {
	maxBase := 128 - len(suffix)
	if len(base) > maxBase {
		base = base[:maxBase]
	}
	return base + suffix
}

func validateDesiredMembership(snapshot Snapshot, skills, servers map[string]bool) error {
	for _, runtime := range []string{"claude", "codex"} {
		for _, id := range snapshot.SkillDesired[runtime] {
			if !skills[id] {
				return migrationError("source_contract_mismatch", fmt.Sprintf("legacy %s desired selection references unavailable Skill %s", runtime, id), nil)
			}
		}
		for _, id := range snapshot.MCPDesired[runtime] {
			if !servers[id] {
				return migrationError("source_contract_mismatch", fmt.Sprintf("legacy %s desired selection references unavailable MCP definition %s", runtime, id), nil)
			}
		}
	}
	return nil
}

func sourceFingerprint(migrations []int64, input store.ConfigImportInput) string {
	type fingerprintSkill struct {
		LegacyID, LegacySourceID, LegacyVersionID, LegacyArtifactID      string
		Kind, SourceName, URL, Subdirectory, Commit, SHA256, ContentHash string
	}
	type fingerprintMCP struct {
		LegacyID, Name, Transport, Command, URL string
		Args                                    []string
		EnvRefs, HeaderRefs                     map[string]string
	}
	type fingerprintProfile struct {
		Name, Description      string
		SkillIDs, MCPServerIDs []string
	}
	canonical := struct {
		Migrations []int64
		Skills     []fingerprintSkill
		MCPServers []fingerprintMCP
		Profiles   []fingerprintProfile
		RelayMCP   []string
		UpdateCron string
		Timezone   string
	}{
		Migrations: append([]int64(nil), migrations...),
		UpdateCron: input.UpdateCron,
		Timezone:   input.Timezone,
	}
	for _, item := range input.Skills {
		canonical.Skills = append(canonical.Skills, fingerprintSkill{
			LegacyID: item.LegacyID, LegacySourceID: item.LegacySourceID, LegacyVersionID: item.LegacyVersionID, LegacyArtifactID: item.LegacyArtifactID,
			Kind: item.Source.Kind, SourceName: item.Source.Name, URL: item.Source.URL,
			Subdirectory: item.Source.Subdirectory, Commit: item.Source.Commit,
			SHA256: item.Package.SHA256, ContentHash: item.Package.ContentHash,
		})
	}
	for _, item := range input.MCPServers {
		canonical.MCPServers = append(canonical.MCPServers, fingerprintMCP{
			LegacyID: item.LegacyID, Name: item.Input.Name, Transport: item.Input.Transport,
			Command: item.Input.Command, URL: item.Input.URL, Args: append([]string(nil), item.Input.Args...),
			EnvRefs: cloneStringMap(item.EnvRefs), HeaderRefs: cloneStringMap(item.HeaderRefs),
		})
	}
	for _, profile := range input.Profiles {
		canonical.Profiles = append(canonical.Profiles, fingerprintProfile{
			Name: profile.Name, Description: profile.Description,
			SkillIDs: sortedUnique(profile.LegacySkillIDs), MCPServerIDs: sortedUnique(profile.LegacyMCPServerIDs),
		})
	}
	canonical.RelayMCP = sortedUnique(input.RelayMCPServerIDs)
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func idSet(groups ...[]string) map[string]bool {
	result := map[string]bool{}
	for _, group := range groups {
		for _, id := range group {
			result[id] = true
		}
	}
	return result
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func countRenames(items []MCPNormalization) int {
	count := 0
	for _, item := range items {
		if strings.TrimSpace(item.OriginalName) != item.NormalizedName {
			count++
		}
	}
	return count
}
