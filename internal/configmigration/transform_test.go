package configmigration

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/skills"
)

const (
	testEnvironmentValue = "migration-environment-value"
	testHeaderValue      = "migration-header-value"
)

func TestPrepareNormalizesMCPAndBuildsStagingProfiles(t *testing.T) {
	snapshot, cipher := migrationSnapshotFixture(t)
	prepared, err := Prepare(snapshot, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Import.Skills) != 1 || len(prepared.Import.MCPServers) != 2 || len(prepared.Import.Secrets) != 2 || len(prepared.Import.Profiles) != 2 {
		t.Fatalf("unexpected import counts: %+v", prepared.Report)
	}
	if len(prepared.Import.RelayMCPServerIDs) != 1 || prepared.Report.RelayMCPServers != 1 {
		t.Fatalf("unexpected relay MCP selection: %+v", prepared.Report)
	}
	names := map[string]string{}
	transports := map[string]string{}
	for _, server := range prepared.Import.MCPServers {
		names[server.LegacyID] = server.Input.Name
		transports[server.LegacyID] = server.Input.Transport
	}
	desiredID := snapshot.MCPDesired["claude"][0]
	var otherID string
	for _, server := range snapshot.MCPServers {
		if server.ID != desiredID {
			otherID = server.ID
		}
	}
	if names[desiredID] != "acemcp" || names[otherID] != "acemcp-http" || transports[otherID] != "http" {
		t.Fatalf("unexpected normalized MCP definitions: names=%v transports=%v", names, transports)
	}
	if prepared.Report.TransportChanges != 1 || prepared.Report.Renames != 2 {
		t.Fatalf("unexpected normalization totals: %+v", prepared.Report)
	}
	human := string(prepared.Report.Human())
	if strings.Contains(human, testEnvironmentValue) || strings.Contains(human, testHeaderValue) {
		t.Fatalf("human report leaked a test value: %s", human)
	}
	body, err := json.Marshal(prepared.Report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(testEnvironmentValue)) || bytes.Contains(body, []byte(testHeaderValue)) {
		t.Fatalf("JSON report leaked a test value: %s", body)
	}
}

func TestPrepareFingerprintIsOrderingIndependentAndExcludesEncryptedValues(t *testing.T) {
	snapshot, cipher := migrationSnapshotFixture(t)
	first, err := Prepare(snapshot, cipher)
	if err != nil {
		t.Fatal(err)
	}
	reordered := snapshot
	reordered.MCPServers = append([]LegacyMCPServer(nil), snapshot.MCPServers...)
	reordered.MCPServers[0], reordered.MCPServers[1] = reordered.MCPServers[1], reordered.MCPServers[0]
	reordered.Secrets = append([]LegacySecret(nil), snapshot.Secrets...)
	reordered.Secrets[0], reordered.Secrets[1] = reordered.Secrets[1], reordered.Secrets[0]
	for index := range reordered.Secrets {
		plaintext := []byte("different-valid-value-" + reordered.Secrets[index].ID)
		ciphertext, err := cipher.Encrypt(plaintext, reordered.Secrets[index].ID)
		clear(plaintext)
		if err != nil {
			t.Fatal(err)
		}
		reordered.Secrets[index].Ciphertext = ciphertext
	}
	second, err := Prepare(reordered, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if first.Import.SourceFingerprint != second.Import.SourceFingerprint {
		t.Fatalf("fingerprint depends on order or encrypted values: %s != %s", first.Import.SourceFingerprint, second.Import.SourceFingerprint)
	}
	renamedSource := snapshot
	renamedSource.Skills = append([]LegacySkill(nil), snapshot.Skills...)
	renamedSource.Skills[0].SourceName = "changed-reviewed-source-name"
	third, err := Prepare(renamedSource, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if first.Import.SourceFingerprint == third.Import.SourceFingerprint {
		t.Fatal("fingerprint did not bind retained Skill source metadata")
	}
}

func TestPrepareRejectsWrongLegacyKeyWithoutLeakingValues(t *testing.T) {
	snapshot, _ := migrationSnapshotFixture(t)
	wrong, err := security.NewCipher(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Prepare(snapshot, wrong)
	if err == nil {
		t.Fatal("wrong legacy key was accepted")
	}
	_, message := PublicError(err)
	if strings.Contains(message, testEnvironmentValue) || strings.Contains(message, testHeaderValue) || strings.Contains(strings.ToLower(message), "authorization") {
		t.Fatalf("decryption error leaked protected data: %q", message)
	}
}

func TestPrepareRejectsDuplicateAndUnreferencedSecretReferences(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		snapshot, cipher := migrationSnapshotFixture(t)
		duplicateID := snapshot.MCPServers[0].EnvRefs["TOKEN"]
		snapshot.MCPServers[1].EnvRefs = map[string]string{"OTHER": duplicateID}
		if _, err := Prepare(snapshot, cipher); err == nil {
			t.Fatal("duplicate Secret reference was accepted")
		}
	})
	t.Run("unreferenced", func(t *testing.T) {
		snapshot, cipher := migrationSnapshotFixture(t)
		snapshot.MCPServers[1].HeaderRefs = map[string]string{}
		if _, err := Prepare(snapshot, cipher); err == nil {
			t.Fatal("unreferenced Secret record was accepted")
		}
	})
	t.Run("dangling", func(t *testing.T) {
		snapshot, cipher := migrationSnapshotFixture(t)
		snapshot.MCPServers[0].EnvRefs["TOKEN"] = uuid.NewString()
		if _, err := Prepare(snapshot, cipher); err == nil {
			t.Fatal("dangling Secret reference was accepted")
		}
	})
	t.Run("wrong-kind", func(t *testing.T) {
		snapshot, cipher := migrationSnapshotFixture(t)
		referencedID := snapshot.MCPServers[0].EnvRefs["TOKEN"]
		for index := range snapshot.Secrets {
			if snapshot.Secrets[index].ID == referencedID {
				snapshot.Secrets[index].Kind = "mcp-header"
			}
		}
		if _, err := Prepare(snapshot, cipher); err == nil {
			t.Fatal("wrong-kind Secret reference was accepted")
		}
	})
}

func TestPrepareRejectsCorruptedSkillArchive(t *testing.T) {
	snapshot, cipher := migrationSnapshotFixture(t)
	snapshot.Skills[0].Archive = append([]byte(nil), snapshot.Skills[0].Archive...)
	snapshot.Skills[0].Archive[len(snapshot.Skills[0].Archive)-1] ^= 0xff
	if _, err := Prepare(snapshot, cipher); err == nil {
		t.Fatal("corrupted Skill archive was accepted")
	}
}

func TestMapSkillSourceSupportsOnlyReviewedLegacyKinds(t *testing.T) {
	base := LegacySkill{ID: uuid.NewString(), Slug: "source", SourceID: uuid.NewString(), SourceName: "source", SourceCommit: "abc123"}
	for _, test := range []struct {
		kind         string
		url          string
		subdirectory string
		wantKind     string
	}{
		{kind: "git", url: "https://example.test/repository.git", wantKind: "git"},
		{kind: "node", subdirectory: "/root/.shared/skills/source", wantKind: "local"},
		{kind: "upload", wantKind: "zip"},
	} {
		item := base
		item.SourceKind, item.SourceURL, item.Subdirectory = test.kind, test.url, test.subdirectory
		source, err := mapSkillSource(item, "fallback")
		if err != nil || source.Kind != test.wantKind {
			t.Fatalf("map %s source: %+v err=%v", test.kind, source, err)
		}
	}
	credentialed := base
	credentialed.SourceKind = "git"
	credentialed.SourceURL = "https://user:credential@example.test/repository.git"
	if _, err := mapSkillSource(credentialed, "fallback"); err == nil {
		t.Fatal("credentialed git URL was accepted")
	}
	unsupported := base
	unsupported.SourceKind = "skillsmp"
	if _, err := mapSkillSource(unsupported, "fallback"); err == nil {
		t.Fatal("unreviewed source kind was accepted")
	}
	for _, location := range []string{"relative/source", "/root/.shared/skills/other", "/root/.shared/skills/source/"} {
		invalidNode := base
		invalidNode.SourceKind = "node"
		invalidNode.Subdirectory = location
		if _, err := mapSkillSource(invalidNode, "fallback"); err == nil {
			t.Fatalf("invalid node source location %q was accepted", location)
		}
	}
}

func TestNormalizeMCPDefinitionRejectsMixedTransportFields(t *testing.T) {
	_, _, err := normalizeMCPDefinition(LegacyMCPServer{
		ID: uuid.NewString(), Name: "mixed", Transport: "stdio", Command: "/usr/bin/tool", URL: "https://example.test/mcp",
	})
	if err == nil {
		t.Fatal("mixed stdio/network definition was accepted")
	}
}

func migrationSnapshotFixture(t *testing.T) (Snapshot, *security.Cipher) {
	t.Helper()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	skill := legacySkillFixture(t, "formatter", "git", "https://example.test/formatter.git")
	envID, headerID := uuid.NewString(), uuid.NewString()
	envCiphertext, err := cipher.Encrypt([]byte(testEnvironmentValue), envID)
	if err != nil {
		t.Fatal(err)
	}
	headerCiphertext, err := cipher.Encrypt([]byte(testHeaderValue), headerID)
	if err != nil {
		t.Fatal(err)
	}
	desiredServerID, otherServerID := uuid.NewString(), uuid.NewString()
	migrations := make([]int64, LegacySchemaVersion)
	for index := range migrations {
		migrations[index] = int64(index + 1)
	}
	return Snapshot{
		Migrations: migrations,
		Identity:   DatabaseIdentity{Database: "legacy", Address: "127.0.0.1", Port: 5432},
		Skills:     []LegacySkill{skill},
		MCPServers: []LegacyMCPServer{
			{ID: desiredServerID, Name: "ACEMCP", Transport: "stdio", Command: "/usr/bin/acemcp", Args: []string{"serve"}, EnvRefs: map[string]string{"TOKEN": envID}, HeaderRefs: map[string]string{}, Source: "toolhub"},
			{ID: otherServerID, Name: "acemcp", Transport: "streamable-http", URL: "https://example.test/mcp", Args: []string{}, EnvRefs: map[string]string{}, HeaderRefs: map[string]string{"Authorization": headerID}, Source: "toolhub"},
		},
		Secrets: []LegacySecret{
			{ID: envID, Kind: "mcp-env", Ciphertext: envCiphertext},
			{ID: headerID, Kind: "mcp-header", Ciphertext: headerCiphertext},
		},
		SkillDesired: map[string][]string{"claude": {skill.ID}, "codex": {skill.ID}},
		MCPDesired:   map[string][]string{"claude": {desiredServerID}, "codex": {desiredServerID}},
		UpdateCron:   "0 2 * * *",
		Timezone:     "Asia/Shanghai",
	}, cipher
}

func legacySkillFixture(t *testing.T, name, sourceKind, sourceURL string) LegacySkill {
	t.Helper()
	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	header := &zip.FileHeader{Name: "SKILL.md", Method: zip.Deflate}
	header.SetMode(0o644)
	header.Modified = time.Unix(0, 0).UTC()
	file, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("---\nname: " + name + "\ndescription: Migration fixture\n---\nFixture body.\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	pkg, err := skills.ScanZIP(raw.Bytes(), skills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return LegacySkill{
		ID: uuid.NewString(), Slug: pkg.Slug, Name: pkg.Name, Description: pkg.Description,
		SourceID: uuid.NewString(), SourceKind: sourceKind, SourceName: name + "-source", SourceURL: sourceURL,
		SourceCommit: "0123456789abcdef", VersionID: uuid.NewString(), ArtifactID: uuid.NewString(),
		ArtifactSHA256: pkg.SHA256, VersionSHA256: pkg.SHA256, SizeBytes: int64(len(pkg.CanonicalZIP)),
		Archive: append([]byte(nil), pkg.CanonicalZIP...),
	}
}

func TestReportJSONFileIsExclusiveAndMode0600(t *testing.T) {
	snapshot, cipher := migrationSnapshotFixture(t)
	prepared, err := Prepare(snapshot, cipher)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/report.json"
	if err := prepared.Report.WriteJSON(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode=%o, want 600", info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(testEnvironmentValue)) || bytes.Contains(body, []byte(testHeaderValue)) {
		t.Fatalf("report leaked a fixture value: %s", body)
	}
	if err := prepared.Report.WriteJSON(path); err == nil {
		t.Fatal("existing report was overwritten")
	}
}
