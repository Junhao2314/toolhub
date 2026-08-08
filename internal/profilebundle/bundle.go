package profilebundle

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/skills"
)

const (
	SchemaVersion  = 1
	KindStandard   = "profile"
	KindSecrets    = "profile-secrets"
	MaxBundleBytes = 256 << 20
	MaxMembers     = 500
)

var (
	slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	shaPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Origin struct {
	Label      string    `json:"label"`
	ExportedAt time.Time `json:"exportedAt"`
}

type Provenance struct {
	Source   string `json:"source"`
	Provider string `json:"provider,omitempty"`
	URL      string `json:"url,omitempty"`
	Commit   string `json:"commit,omitempty"`
}

type Profile struct {
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	SourceRevision int64  `json:"sourceRevision"`
	CanonicalHash  string `json:"canonicalHash"`
}

type Skill struct {
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	SHA256      string     `json:"sha256"`
	ContentHash string     `json:"contentHash"`
	Provenance  Provenance `json:"provenance"`
}

type MCP struct {
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Transport   string     `json:"transport"`
	Command     string     `json:"command,omitempty"`
	Args        []string   `json:"args,omitempty"`
	URL         string     `json:"url,omitempty"`
	EnvSlots    []string   `json:"envSlots"`
	HeaderSlots []string   `json:"headerSlots"`
	Provenance  Provenance `json:"provenance"`
}

type Manifest struct {
	SchemaVersion int     `json:"schemaVersion"`
	Kind          string  `json:"kind"`
	Origin        Origin  `json:"origin"`
	Profile       Profile `json:"profile"`
	Skills        []Skill `json:"skills"`
	MCPServers    []MCP   `json:"mcpServers"`
}

type SecretMCP struct {
	Key     string            `json:"key"`
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type SecretDocument struct {
	SchemaVersion int         `json:"schemaVersion"`
	MCPServers    []SecretMCP `json:"mcpServers"`
}

type Parsed struct {
	Manifest   Manifest
	Packages   map[string]skills.Package
	Secrets    SecretDocument
	BundleHash string
}

func MCPKey(item MCP) (string, error) {
	item.Key = ""
	item.Provenance = Provenance{}
	item.EnvSlots = sortedUnique(item.EnvSlots)
	item.HeaderSlots = sortedUnique(item.HeaderSlots)
	body, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func CanonicalProfileHash(manifest Manifest) (string, error) {
	type profileCanonical struct {
		Name, Description string
		Skills            []struct{ Slug, SHA256, ContentHash string }
		MCPKeys           []string
	}
	value := profileCanonical{Name: strings.TrimSpace(manifest.Profile.Name), Description: strings.TrimSpace(manifest.Profile.Description), MCPKeys: []string{}}
	for _, item := range manifest.Skills {
		value.Skills = append(value.Skills, struct{ Slug, SHA256, ContentHash string }{item.Slug, item.SHA256, item.ContentHash})
	}
	for _, item := range manifest.MCPServers {
		key, err := MCPKey(item)
		if err != nil {
			return "", err
		}
		value.MCPKeys = append(value.MCPKeys, key)
	}
	sort.Slice(value.Skills, func(i, j int) bool { return value.Skills[i].Slug < value.Skills[j].Slug })
	sort.Strings(value.MCPKeys)
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func Encode(manifest Manifest, archives map[string][]byte, secrets *SecretDocument) ([]byte, error) {
	manifest.SchemaVersion = SchemaVersion
	if secrets == nil {
		manifest.Kind = KindStandard
	} else {
		manifest.Kind = KindSecrets
		secrets.SchemaVersion = SchemaVersion
	}
	canonicalHash, err := CanonicalProfileHash(manifest)
	if err != nil {
		return nil, err
	}
	manifest.Profile.CanonicalHash = canonicalHash
	if err := validateManifest(&manifest); err != nil {
		return nil, err
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if err := writeEntry(writer, "manifest.json", manifestBody); err != nil {
		return nil, err
	}
	sortedSkills := append([]Skill(nil), manifest.Skills...)
	sort.Slice(sortedSkills, func(i, j int) bool { return sortedSkills[i].SHA256 < sortedSkills[j].SHA256 })
	for _, item := range sortedSkills {
		body := archives[item.SHA256]
		pkg, err := skills.ScanZIP(body, skills.DefaultLimits)
		if err != nil || pkg.SHA256 != item.SHA256 || pkg.ContentHash != item.ContentHash || pkg.Slug != item.Slug {
			return nil, fmt.Errorf("Skill archive %s does not match manifest", item.Slug)
		}
		if err := writeEntry(writer, "skills/"+item.SHA256+".zip", body); err != nil {
			return nil, err
		}
	}
	if secrets != nil {
		if err := validateSecrets(manifest, *secrets); err != nil {
			return nil, err
		}
		body, err := json.Marshal(secrets)
		if err != nil {
			return nil, err
		}
		if err := writeEntry(writer, "secrets.json", body); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if buffer.Len() > MaxBundleBytes {
		return nil, errors.New("Profile Bundle exceeds size limit")
	}
	return buffer.Bytes(), nil
}

func Parse(body []byte) (Parsed, error) {
	if len(body) == 0 || len(body) > MaxBundleBytes {
		return Parsed{}, errors.New("Profile Bundle size is invalid")
	}
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return Parsed{}, errors.New("Profile Bundle is not a valid ZIP container")
	}
	files := map[string][]byte{}
	if len(reader.File) > 2+MaxMembers {
		return Parsed{}, errors.New("Profile Bundle contains too many entries")
	}
	for _, file := range reader.File {
		name := path.Clean(file.Name)
		if name != file.Name || name == "." || name == ".." || strings.HasPrefix(name, "../") || path.IsAbs(name) || strings.Contains(name, "\\") || file.FileInfo().IsDir() || file.Mode()&os.ModeType != 0 {
			return Parsed{}, errors.New("Profile Bundle contains an unsafe entry")
		}
		if _, exists := files[name]; exists {
			return Parsed{}, errors.New("Profile Bundle contains a duplicate entry")
		}
		limit := int64(2 << 20)
		if strings.HasPrefix(name, "skills/") {
			limit = skills.DefaultLimits.MaxArchiveBytes
		}
		entry, err := readEntry(file, limit)
		if err != nil {
			return Parsed{}, err
		}
		files[name] = entry
	}
	manifestBody := files["manifest.json"]
	if len(manifestBody) == 0 {
		return Parsed{}, errors.New("Profile Bundle manifest is missing")
	}
	var manifest Manifest
	if err := decodeStrict(manifestBody, &manifest); err != nil {
		return Parsed{}, fmt.Errorf("invalid Profile Bundle manifest: %w", err)
	}
	if err := validateManifest(&manifest); err != nil {
		return Parsed{}, err
	}
	canonicalHash, err := CanonicalProfileHash(manifest)
	if err != nil || canonicalHash != manifest.Profile.CanonicalHash {
		return Parsed{}, errors.New("Profile Bundle canonical hash does not match")
	}
	packages := make(map[string]skills.Package, len(manifest.Skills))
	expectedFiles := map[string]bool{"manifest.json": true}
	for _, item := range manifest.Skills {
		name := "skills/" + item.SHA256 + ".zip"
		expectedFiles[name] = true
		archive := files[name]
		pkg, err := skills.ScanZIP(archive, skills.DefaultLimits)
		if err != nil || pkg.SHA256 != item.SHA256 || pkg.ContentHash != item.ContentHash || pkg.Slug != item.Slug || pkg.Name != item.Name {
			return Parsed{}, fmt.Errorf("Profile Bundle Skill %q failed verification", item.Slug)
		}
		packages[item.SHA256] = pkg
	}
	secrets := SecretDocument{SchemaVersion: SchemaVersion, MCPServers: []SecretMCP{}}
	if manifest.Kind == KindSecrets {
		expectedFiles["secrets.json"] = true
		if err := decodeStrict(files["secrets.json"], &secrets); err != nil {
			return Parsed{}, errors.New("secret-bearing Profile Bundle is missing a valid secrets document")
		}
		if err := validateSecrets(manifest, secrets); err != nil {
			return Parsed{}, err
		}
	} else if len(files["secrets.json"]) != 0 {
		return Parsed{}, errors.New("standard Profile Bundle cannot contain Secret values")
	}
	for name := range files {
		if !expectedFiles[name] {
			return Parsed{}, fmt.Errorf("unexpected Profile Bundle entry %q", name)
		}
	}
	sum := sha256.Sum256(body)
	return Parsed{Manifest: manifest, Packages: packages, Secrets: secrets, BundleHash: hex.EncodeToString(sum[:])}, nil
}

func validateManifest(manifest *Manifest) error {
	if manifest.SchemaVersion != SchemaVersion || (manifest.Kind != KindStandard && manifest.Kind != KindSecrets) {
		return errors.New("unsupported Profile Bundle schema or kind")
	}
	manifest.Origin.Label = strings.TrimSpace(manifest.Origin.Label)
	if manifest.Origin.Label == "" || len(manifest.Origin.Label) > 120 || strings.ContainsAny(manifest.Origin.Label, "/\\\x00\r\n") || manifest.Origin.ExportedAt.IsZero() {
		return errors.New("Profile Bundle Origin is invalid")
	}
	manifest.Profile.Name = strings.TrimSpace(manifest.Profile.Name)
	if manifest.Profile.Name == "" || len(manifest.Profile.Name) > 120 || manifest.Profile.SourceRevision < 1 || !shaPattern.MatchString(manifest.Profile.CanonicalHash) {
		return errors.New("Profile Bundle Profile metadata is invalid")
	}
	if len(manifest.Skills) > MaxMembers || len(manifest.MCPServers) > MaxMembers {
		return errors.New("Profile Bundle membership exceeds safety limit")
	}
	seenSkills, seenMCP := map[string]bool{}, map[string]bool{}
	for i := range manifest.Skills {
		item := &manifest.Skills[i]
		if !slugPattern.MatchString(item.Slug) || strings.HasPrefix(item.Slug, ".") || len(strings.TrimSpace(item.Name)) == 0 || !shaPattern.MatchString(item.SHA256) || !shaPattern.MatchString(item.ContentHash) || seenSkills[item.Slug] {
			return errors.New("Profile Bundle contains invalid or duplicate Skill metadata")
		}
		seenSkills[item.Slug] = true
		if err := validateProvenance(item.Provenance); err != nil {
			return err
		}
	}
	for i := range manifest.MCPServers {
		item := &manifest.MCPServers[i]
		item.EnvSlots, item.HeaderSlots = sortedUnique(item.EnvSlots), sortedUnique(item.HeaderSlots)
		if !slugPattern.MatchString(item.Name) || strings.HasPrefix(item.Name, ".") || seenMCP[item.Name] {
			return errors.New("Profile Bundle contains invalid or duplicate MCP metadata")
		}
		seenMCP[item.Name] = true
		if err := validateMCP(*item); err != nil {
			return err
		}
		key, err := MCPKey(*item)
		if err != nil || key != item.Key {
			return fmt.Errorf("Profile Bundle MCP %q hash does not match", item.Name)
		}
		if err := validateProvenance(item.Provenance); err != nil {
			return err
		}
	}
	return nil
}

func validateMCP(item MCP) error {
	if len(item.Key) != 64 || len(item.Args) > 100 || len(item.EnvSlots) > 100 || len(item.HeaderSlots) > 100 {
		return errors.New("Profile Bundle MCP metadata exceeds safety limits")
	}
	for _, key := range append(append([]string(nil), item.EnvSlots...), item.HeaderSlots...) {
		if strings.TrimSpace(key) == "" || len(key) > 200 {
			return errors.New("Profile Bundle MCP Secret slot is invalid")
		}
	}
	switch item.Transport {
	case "stdio":
		if strings.TrimSpace(item.Command) == "" || item.URL != "" {
			return errors.New("Profile Bundle stdio MCP structure is invalid")
		}
	case "http", "sse":
		parsed, err := url.Parse(item.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || item.Command != "" || len(item.Args) != 0 {
			return errors.New("Profile Bundle network MCP structure is invalid")
		}
	default:
		return errors.New("Profile Bundle MCP transport is invalid")
	}
	return nil
}

func validateProvenance(value Provenance) error {
	allowed := map[string]bool{"claude": true, "codex": true, "hermes": true, "git": true, "skillsmp": true, "xiaping": true, "zip": true, "local": true, "bundle": true}
	if !allowed[strings.ToLower(strings.TrimSpace(value.Source))] || len(value.Provider) > 120 || len(value.Commit) > 200 {
		return errors.New("Profile Bundle member provenance is invalid")
	}
	if value.URL != "" {
		parsed, err := url.Parse(value.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return errors.New("Profile Bundle provenance URL is invalid")
		}
	}
	return nil
}

func validateSecrets(manifest Manifest, secrets SecretDocument) error {
	if secrets.SchemaVersion != SchemaVersion {
		return errors.New("Profile Bundle Secret schema is invalid")
	}
	expected := map[string]MCP{}
	for _, item := range manifest.MCPServers {
		expected[item.Key] = item
	}
	seen := map[string]bool{}
	for _, item := range secrets.MCPServers {
		mcp, ok := expected[item.Key]
		if !ok || seen[item.Key] {
			return errors.New("Profile Bundle contains an unknown or duplicate Secret binding")
		}
		seen[item.Key] = true
		if !sameKeys(mcp.EnvSlots, item.Env) || !sameKeys(mcp.HeaderSlots, item.Headers) {
			return errors.New("Profile Bundle Secret bindings do not match declared slots")
		}
		for _, values := range []map[string]string{item.Env, item.Headers} {
			for _, value := range values {
				if len(value) > 64<<10 {
					return errors.New("Profile Bundle Secret value exceeds size limit")
				}
			}
		}
	}
	for _, item := range manifest.MCPServers {
		if (len(item.EnvSlots) > 0 || len(item.HeaderSlots) > 0) && !seen[item.Key] {
			return errors.New("Profile Bundle is missing a declared Secret binding")
		}
	}
	return nil
}

func sameKeys(expected []string, values map[string]string) bool {
	if len(expected) != len(values) {
		return false
	}
	for _, key := range expected {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func writeEntry(writer *zip.Writer, name string, body []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o600)
	header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	destination, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = destination.Write(body)
	return err
}

func readEntry(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, errors.New("Profile Bundle entry exceeds size limit")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errors.New("Profile Bundle entry exceeds size limit")
	}
	return body, nil
}

func decodeStrict(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON must contain exactly one object")
	}
	return nil
}
