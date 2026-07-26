package skills

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Limits struct {
	MaxArchiveBytes      int64
	MaxUncompressedBytes int64
	MaxFileBytes         int64
	MaxFiles             int
}

var DefaultLimits = Limits{MaxArchiveBytes: 20 << 20, MaxUncompressedBytes: 50 << 20, MaxFileBytes: 10 << 20, MaxFiles: 2000}

type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

type ScanReport struct {
	RiskLevel    string    `json:"riskLevel"`
	Findings     []Finding `json:"findings"`
	FileCount    int       `json:"fileCount"`
	SizeBytes    int64     `json:"sizeBytes"`
	Scripts      []string  `json:"scripts"`
	Executables  []string  `json:"executables"`
	ExternalURLs []string  `json:"externalUrls"`
	AllowedTools []string  `json:"allowedTools"`
	License      string    `json:"license"`
}

type Package struct {
	Slug         string         `json:"slug"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Manifest     map[string]any `json:"manifest"`
	SHA256       string         `json:"sha256"`
	CanonicalZIP []byte         `json:"-"`
	Report       ScanReport     `json:"scanReport"`
}

var (
	scriptExtensions = map[string]bool{".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true, ".bat": true, ".cmd": true, ".py": true, ".js": true, ".ts": true}
	urlPattern       = regexp.MustCompile(`https?://[^\s<>)\]"']+`)
	secretPattern    = regexp.MustCompile(`(?i)(?:api[_-]?key|secret|password|token|private[_-]?key)\s*[:=]\s*["']?[A-Za-z0-9_./+=-]{12,}`)
	slugPattern      = regexp.MustCompile(`[^a-z0-9]+`)
)

type entry struct {
	name string
	mode fs.FileMode
	data []byte
}

func ScanZIP(data []byte, limits Limits) (Package, error) {
	if int64(len(data)) > limits.MaxArchiveBytes {
		return Package{}, errors.New("archive exceeds compressed size limit")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Package{}, fmt.Errorf("open ZIP: %w", err)
	}
	entries := make([]entry, 0, len(reader.File))
	seen := map[string]bool{}
	var total int64
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		name, err := safeArchivePath(file.Name)
		if err != nil {
			return Package{}, err
		}
		if seen[name] {
			return Package{}, fmt.Errorf("duplicate archive path %q", name)
		}
		seen[name] = true
		if file.Mode()&os.ModeSymlink != 0 {
			return Package{}, fmt.Errorf("symlink entries are not allowed: %s", name)
		}
		if len(entries)+1 > limits.MaxFiles {
			return Package{}, errors.New("archive contains too many files")
		}
		if file.UncompressedSize64 > uint64(limits.MaxFileBytes) {
			return Package{}, fmt.Errorf("file exceeds size limit: %s", name)
		}
		body, err := readZipFile(file, limits.MaxFileBytes)
		if err != nil {
			return Package{}, fmt.Errorf("read %s: %w", name, err)
		}
		total += int64(len(body))
		if total > limits.MaxUncompressedBytes {
			return Package{}, errors.New("archive exceeds uncompressed size limit")
		}
		entries = append(entries, entry{name: name, mode: file.Mode(), data: body})
	}
	return buildPackage(entries)
}

func ScanDirectory(root string, limits Limits) (Package, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Package{}, err
	}
	var entries []entry
	var total int64
	err = filepath.WalkDir(resolvedRoot, func(current string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == resolvedRoot {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed: %s", current)
		}
		if item.IsDir() {
			return nil
		}
		if item.Name() == ".toolhub-managed.json" {
			return nil
		}
		if len(entries)+1 > limits.MaxFiles || info.Size() > limits.MaxFileBytes {
			return errors.New("package exceeds file limits")
		}
		relative, err := filepath.Rel(resolvedRoot, current)
		if err != nil {
			return err
		}
		name, err := safeArchivePath(filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		body, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		total += int64(len(body))
		if total > limits.MaxUncompressedBytes {
			return errors.New("package exceeds uncompressed size limit")
		}
		entries = append(entries, entry{name: name, mode: info.Mode(), data: body})
		return nil
	})
	if err != nil {
		return Package{}, err
	}
	return buildPackage(entries)
}

func buildPackage(entries []entry) (Package, error) {
	entries, err := normalizeRoot(entries)
	if err != nil {
		return Package{}, err
	}
	var skillMD []byte
	for _, item := range entries {
		if item.name == "SKILL.md" {
			skillMD = item.data
			break
		}
	}
	manifest, err := parseFrontmatter(skillMD)
	if err != nil {
		return Package{}, err
	}
	name, _ := manifest["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return Package{}, errors.New("SKILL.md frontmatter requires a name of at most 120 characters")
	}
	description, _ := manifest["description"].(string)
	report := inspect(entries, manifest)
	canonical, err := canonicalZIP(entries)
	if err != nil {
		return Package{}, err
	}
	sum := sha256.Sum256(canonical)
	return Package{Slug: slugify(name), Name: name, Description: strings.TrimSpace(description), Manifest: manifest, SHA256: hex.EncodeToString(sum[:]), CanonicalZIP: canonical, Report: report}, nil
}

func normalizeRoot(entries []entry) ([]entry, error) {
	var skillPaths []string
	for _, item := range entries {
		if path.Base(item.name) == "SKILL.md" {
			skillPaths = append(skillPaths, item.name)
		}
	}
	if len(skillPaths) != 1 {
		return nil, fmt.Errorf("package must contain exactly one SKILL.md, found %d", len(skillPaths))
	}
	root := path.Dir(skillPaths[0])
	if root == "." {
		return entries, nil
	}
	prefix := root + "/"
	result := make([]entry, 0, len(entries))
	for _, item := range entries {
		if !strings.HasPrefix(item.name, prefix) {
			return nil, errors.New("files outside the SKILL.md package root are not allowed")
		}
		item.name = strings.TrimPrefix(item.name, prefix)
		result = append(result, item)
	}
	return result, nil
}

func parseFrontmatter(body []byte) (map[string]any, error) {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, errors.New("SKILL.md must start with YAML frontmatter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return nil, errors.New("SKILL.md frontmatter is not terminated")
	}
	manifest := map[string]any{}
	if err := yaml.Unmarshal([]byte(text[4:4+end]), &manifest); err != nil {
		return nil, fmt.Errorf("parse SKILL.md frontmatter: %w", err)
	}
	return manifest, nil
}

func inspect(entries []entry, manifest map[string]any) ScanReport {
	report := ScanReport{RiskLevel: "low", FileCount: len(entries)}
	urls := map[string]bool{}
	for _, item := range entries {
		report.SizeBytes += int64(len(item.data))
		extension := strings.ToLower(path.Ext(item.name))
		if scriptExtensions[extension] {
			report.Scripts = append(report.Scripts, item.name)
		}
		if item.mode&0111 != 0 {
			report.Executables = append(report.Executables, item.name)
		}
		for _, found := range urlPattern.FindAllString(string(item.data), -1) {
			if parsed, err := url.Parse(strings.TrimRight(found, ".,")); err == nil && parsed.Host != "" {
				urls[parsed.String()] = true
			}
		}
		if secretPattern.Match(item.data) {
			report.Findings = append(report.Findings, Finding{Code: "possible-secret", Severity: "high", Path: item.name, Message: "Possible embedded credential requires manual review"})
		}
	}
	for value := range urls {
		report.ExternalURLs = append(report.ExternalURLs, value)
	}
	sort.Strings(report.ExternalURLs)
	report.AllowedTools = stringList(manifest["allowed-tools"])
	report.License, _ = manifest["license"].(string)
	if report.License == "" {
		for _, item := range entries {
			if strings.HasPrefix(strings.ToUpper(path.Base(item.name)), "LICENSE") {
				report.License = "file:" + item.name
				break
			}
		}
	}
	if report.License == "" {
		report.Findings = append(report.Findings, Finding{Code: "license-missing", Severity: "medium", Message: "No license declaration was found"})
	}
	if len(report.Executables) > 0 || hasSeverity(report.Findings, "high") {
		report.RiskLevel = "high"
	} else if len(report.Scripts) > 0 || len(report.ExternalURLs) > 0 || len(report.AllowedTools) > 0 || report.License == "" {
		report.RiskLevel = "medium"
	}
	return report
}

func canonicalZIP(entries []entry) ([]byte, error) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
		header.SetMode(item.mode.Perm())
		header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		fileWriter, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := fileWriter.Write(item.data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func safeArchivePath(name string) (string, error) {
	if name == "" || strings.Contains(name, "\\") || strings.ContainsRune(name, '\x00') || path.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != name {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return clean, nil
}

func readZipFile(file *zip.File, limit int64) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return body, nil
}

func slugify(name string) string {
	value := slugPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "skill"
	}
	return value
}

func stringList(value any) []string {
	switch typed := value.(type) {
	case string:
		return strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	case []any:
		var result []string
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, strings.TrimSpace(text))
			}
		}
		return result
	default:
		return nil
	}
}

func hasSeverity(findings []Finding, severity string) bool {
	for _, finding := range findings {
		if finding.Severity == severity {
			return true
		}
	}
	return false
}
