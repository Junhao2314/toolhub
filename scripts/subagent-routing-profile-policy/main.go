package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/skills"
)

const (
	policySkillSlug = "subagent-routing"
	legacySkillSlug = "same-model-subagents"
	maxResponseSize = 8 << 20
)

var retainedLibrarySlugs = []string{
	"dispatching-parallel-agents",
	"subagent-driven-development",
	"requesting-code-review",
}

var codingProfiles = map[string]bool{
	"claude-coding": true,
	"codex-coding":  true,
}

var frontendProfiles = map[string]bool{
	"claude-frontend-ui": true,
	"codex-frontend-ui":  true,
}

var orchestrationSkillSlugs = []string{
	"dispatching-parallel-agents",
	"subagent-driven-development",
}

// These are the small cross-project Kimi frontend baseline. Project-specific
// constraints (for example ToolHub's toolhub-frontend Skill) are overlays and
// must not become mandatory members of every frontend Profile.
var frontendBundleSkillSlugs = []string{
	"ui-ux-pro-max-cn",
	"responsive-check",
	"performance-audit",
	"browser-ui-verification",
}

type skill struct {
	ID               string   `json:"id"`
	Slug             string   `json:"slug"`
	CurrentVersionID string   `json:"currentVersionId"`
	CurrentSHA256    string   `json:"currentSha256"`
	Tags             []string `json:"tags"`
}

type skillPin struct {
	SkillID   string `json:"skillId"`
	VersionID string `json:"versionId"`
	Slug      string `json:"slug"`
}

type profile struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Revision      int64      `json:"revision"`
	CanonicalHash string     `json:"canonicalHash"`
	SkillIDs      []string   `json:"skillIds"`
	Skills        []skillPin `json:"skills"`
}

type profileInput struct {
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Revision        int64             `json:"revision"`
	SkillIDs        []string          `json:"skillIds"`
	SkillVersionIDs map[string]string `json:"skillVersionIds"`
}

type commandMode string

const (
	modePlan  commandMode = "plan"
	modeApply commandMode = "apply"
	modeCheck commandMode = "check"
)

type commandConfig struct {
	BaseURL  string
	Username string
	Password string
	SkillDir string
	Mode     commandMode
}

type report struct {
	Mode     commandMode     `json:"mode"`
	Skill    skillChange     `json:"skill"`
	Profiles []profileChange `json:"profiles"`
	Verified bool            `json:"verified"`
}

type skillChange struct {
	Action string `json:"action"`
	SHA256 string `json:"sha256"`
}

type profileChange struct {
	Name           string   `json:"name"`
	BeforeRevision int64    `json:"beforeRevision"`
	AfterRevision  int64    `json:"afterRevision"`
	Add            []string `json:"add"`
	Remove         []string `json:"remove"`
}

type apiClient struct {
	baseURL *url.URL
	http    *http.Client
	csrf    string
}

type apiErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func main() {
	config, err := parseConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	result, err := execute(context.Background(), config)
	if result.Mode != "" {
		if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
			fmt.Fprintln(os.Stderr, encodeErr)
			os.Exit(1)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseConfig() (commandConfig, error) {
	apply := flag.Bool("apply", false, "upload the Skill and update Profiles")
	check := flag.Bool("check", false, "strictly verify policy state without mutation")
	flag.Parse()
	if *apply && *check {
		return commandConfig{}, errors.New("--apply and --check are mutually exclusive")
	}
	mode := modePlan
	if *apply {
		mode = modeApply
	}
	if *check {
		mode = modeCheck
	}
	config := commandConfig{
		BaseURL:  envOrDefault("TOOLHUB_POLICY_URL", "http://127.0.0.1:18480"),
		Username: strings.TrimSpace(os.Getenv("TOOLHUB_POLICY_USERNAME")),
		Password: os.Getenv("TOOLHUB_POLICY_PASSWORD"),
		SkillDir: envOrDefault("TOOLHUB_POLICY_SKILL_DIR", ".agents/skills/subagent-routing"),
		Mode:     mode,
	}
	if config.Username == "" {
		return commandConfig{}, errors.New("TOOLHUB_POLICY_USERNAME is required")
	}
	if config.Password == "" {
		return commandConfig{}, errors.New("TOOLHUB_POLICY_PASSWORD is required")
	}
	return config, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func execute(ctx context.Context, config commandConfig) (report, error) {
	result := report{Mode: config.Mode, Profiles: []profileChange{}}
	if config.Mode != modePlan && config.Mode != modeApply && config.Mode != modeCheck {
		return result, fmt.Errorf("unsupported mode %q", config.Mode)
	}
	if strings.TrimSpace(config.Username) == "" || config.Password == "" {
		return result, errors.New("policy credentials are required")
	}

	pkg, err := skills.ScanDirectory(config.SkillDir, skills.DefaultLimits)
	if err != nil {
		return result, fmt.Errorf("scan policy Skill: %w", err)
	}
	if pkg.Slug != policySkillSlug {
		return result, fmt.Errorf("policy Skill slug is %q, want %q", pkg.Slug, policySkillSlug)
	}
	result.Skill.SHA256 = pkg.SHA256

	client, err := newAPIClient(config.BaseURL)
	if err != nil {
		return result, err
	}
	if err := client.login(ctx, config.Username, config.Password); err != nil {
		return result, fmt.Errorf("login: %w", err)
	}

	library, err := client.listSkills(ctx)
	if err != nil {
		return result, err
	}
	policySkill, found, err := uniqueSkillBySlug(library, policySkillSlug)
	if err != nil {
		return result, err
	}
	switch {
	case !found:
		result.Skill.Action = "create"
	case policySkill.CurrentSHA256 == pkg.SHA256:
		result.Skill.Action = "reuse"
	case policySkill.CurrentSHA256 != pkg.SHA256:
		result.Skill.Action = "collision"
		return result, fmt.Errorf(
			"subagent-routing collision: Library SHA %s differs from local SHA %s",
			policySkill.CurrentSHA256,
			pkg.SHA256,
		)
	}

	profiles, err := client.listProfiles(ctx)
	if err != nil {
		return result, err
	}
	profilesByName, err := validateBaseline(library, profiles)
	if err != nil {
		return result, err
	}

	if config.Mode == modePlan {
		result.Profiles = buildReport(profilesByName, policySkill, found, library)
		return result, nil
	}
	if config.Mode == modeCheck {
		result.Profiles = buildReport(profilesByName, policySkill, found, library)
		if !found {
			return result, errors.New("subagent-routing is missing from the Library")
		}
		finalProfiles, err := client.listProfiles(ctx)
		if err != nil {
			return result, err
		}
		finalByName, err := validateBaseline(library, finalProfiles)
		if err != nil {
			return result, err
		}
		if err := verifyState(library, finalByName, policySkill); err != nil {
			return result, err
		}
		result.Verified = true
		return result, nil
	}

	if !found {
		policySkill, err = client.uploadSkill(ctx, pkg)
		if err != nil {
			return result, err
		}
		if policySkill.Slug != policySkillSlug || policySkill.CurrentSHA256 != pkg.SHA256 {
			return result, fmt.Errorf(
				"uploaded Skill mismatch: slug=%q sha256=%q, want slug=%q sha256=%q",
				policySkill.Slug,
				policySkill.CurrentSHA256,
				policySkillSlug,
				pkg.SHA256,
			)
		}
		if policySkill.ID == "" || policySkill.CurrentVersionID == "" {
			return result, errors.New("uploaded Skill response omitted its ID or current version ID")
		}
		library = append(library, policySkill)
		found = true
	}

	result.Profiles = buildReport(profilesByName, policySkill, found, library)
	for index := range result.Profiles {
		result.Profiles[index].AfterRevision = result.Profiles[index].BeforeRevision
	}
	targetNames := profileNames(profilesByName)
	for index, name := range targetNames {
		latestProfiles, err := client.listProfiles(ctx)
		if err != nil {
			return result, err
		}
		latestByName, err := validateBaseline(library, latestProfiles)
		if err != nil {
			return result, err
		}
		current := latestByName[name]
		result.Profiles[index] = buildReport(latestByName, policySkill, found, library)[index]
		result.Profiles[index].AfterRevision = result.Profiles[index].BeforeRevision
		input, changed, err := desiredProfile(current, policySkill, library)
		if err != nil {
			return result, err
		}
		if !changed {
			continue
		}
		updated, err := client.updateProfile(ctx, current.ID, input)
		if err != nil {
			return result, fmt.Errorf("update Profile %s: %w", name, err)
		}
		result.Profiles[index].AfterRevision = updated.Revision
	}

	finalLibrary, err := client.listSkills(ctx)
	if err != nil {
		return result, err
	}
	finalProfiles, err := client.listProfiles(ctx)
	if err != nil {
		return result, err
	}
	finalByName, err := validateBaseline(finalLibrary, finalProfiles)
	if err != nil {
		return result, err
	}
	finalPolicySkill, found, err := uniqueSkillBySlug(finalLibrary, policySkillSlug)
	if err != nil {
		return result, err
	}
	if !found || finalPolicySkill.ID != policySkill.ID || finalPolicySkill.CurrentVersionID != policySkill.CurrentVersionID || finalPolicySkill.CurrentSHA256 != pkg.SHA256 {
		return result, errors.New("subagent-routing Library state changed during migration")
	}
	if err := verifyState(finalLibrary, finalByName, finalPolicySkill); err != nil {
		return result, err
	}
	result.Verified = true
	return result, nil
}

func newAPIClient(rawBaseURL string) (*apiClient, error) {
	baseURL, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawBaseURL), "/"))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, errors.New("TOOLHUB_POLICY_URL must be an absolute HTTP(S) URL")
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, errors.New("TOOLHUB_POLICY_URL must use HTTP or HTTPS")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &apiClient{
		baseURL: baseURL,
		http: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (c *apiClient) login(ctx context.Context, username, password string) error {
	body := map[string]string{"username": username, "password": password}
	var response struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/auth/login", body, &response, false); err != nil {
		return err
	}
	c.csrf = strings.TrimSpace(response.CSRFToken)
	if c.csrf == "" {
		return errors.New("login response omitted the CSRF token")
	}
	return nil
}

func (c *apiClient) listSkills(ctx context.Context) ([]skill, error) {
	var response struct {
		Items []skill `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/skills", nil, &response, true); err != nil {
		return nil, fmt.Errorf("list Skills: %w", err)
	}
	if response.Items == nil {
		response.Items = []skill{}
	}
	return response.Items, nil
}

func (c *apiClient) listProfiles(ctx context.Context) ([]profile, error) {
	var response struct {
		Items []profile `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/profiles", nil, &response, true); err != nil {
		return nil, fmt.Errorf("list Profiles: %w", err)
	}
	if response.Items == nil {
		response.Items = []profile{}
	}
	return response.Items, nil
}

func (c *apiClient) uploadSkill(ctx context.Context, pkg skills.Package) (skill, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", policySkillSlug+".zip")
	if err != nil {
		return skill{}, err
	}
	if _, err := part.Write(pkg.CanonicalZIP); err != nil {
		return skill{}, err
	}
	if err := writer.Close(); err != nil {
		return skill{}, err
	}

	request, err := c.request(ctx, http.MethodPost, "/api/v1/skills/upload", &body)
	if err != nil {
		return skill{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", c.csrf)
	var response skill
	if err := c.send(request, &response); err != nil {
		return skill{}, fmt.Errorf("upload Skill: %w", err)
	}
	return response, nil
}

func (c *apiClient) updateProfile(ctx context.Context, profileID string, input profileInput) (profile, error) {
	var response profile
	endpoint := "/api/v1/profiles/" + url.PathEscape(profileID)
	if err := c.doJSON(ctx, http.MethodPut, endpoint, input, &response, true); err != nil {
		return profile{}, err
	}
	return response, nil
}

func (c *apiClient) doJSON(ctx context.Context, method, endpoint string, body, output any, authenticated bool) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := c.request(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated && method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	return c.send(request, output)
}

func (c *apiClient) request(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	requestURL := *c.baseURL
	requestURL.Path = path.Join(c.baseURL.Path, endpoint)
	requestURL.RawQuery = ""
	return http.NewRequestWithContext(ctx, method, requestURL.String(), body)
}

func (c *apiClient) send(request *http.Request, output any) error {
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return err
	}
	if len(body) > maxResponseSize {
		return errors.New("ToolHub response exceeded the size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var envelope apiErrorEnvelope
		_ = json.Unmarshal(body, &envelope)
		code := strings.TrimSpace(envelope.Error.Code)
		message := strings.TrimSpace(envelope.Error.Message)
		if code == "" {
			code = "request_failed"
		}
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("HTTP %d %s: %s", response.StatusCode, code, message)
	}
	if output == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode ToolHub response: %w", err)
	}
	return nil
}

func validateBaseline(library []skill, profiles []profile) (map[string]profile, error) {
	for _, slug := range retainedLibrarySlugs {
		if _, found, err := uniqueSkillBySlug(library, slug); err != nil {
			return nil, err
		} else if !found {
			return nil, fmt.Errorf("required Library Skill %s is missing", slug)
		}
	}
	for _, slug := range frontendBundleSkillSlugs {
		if _, found, err := uniqueSkillBySlug(library, slug); err != nil {
			return nil, err
		} else if !found {
			return nil, fmt.Errorf("frontend Kimi Library Skill %s is missing", slug)
		}
	}

	counts := map[string]int{}
	profilesByName := map[string]profile{}
	for _, item := range profiles {
		if strings.TrimSpace(item.Name) == "" {
			return nil, errors.New("active Profile has an empty name")
		}
		counts[item.Name]++
		profilesByName[item.Name] = item
	}
	for name, count := range counts {
		if count != 1 {
			return nil, fmt.Errorf("active Profile %s appears %d times", name, count)
		}
		if err := validateProfileProjection(profilesByName[name]); err != nil {
			return nil, fmt.Errorf("Profile %s: %w", name, err)
		}
	}
	return profilesByName, nil
}

func validateProfileProjection(item profile) error {
	if item.ID == "" || item.Name == "" || item.Revision < 1 {
		return errors.New("response omitted identity or revision")
	}
	skillIDs := make([]string, 0, len(item.Skills))
	seenSkills := map[string]bool{}
	for _, pin := range item.Skills {
		if pin.SkillID == "" || pin.VersionID == "" || pin.Slug == "" {
			return errors.New("Skill pin omitted ID, version, or slug")
		}
		if seenSkills[pin.SkillID] {
			return fmt.Errorf("duplicate Skill ID %s", pin.SkillID)
		}
		seenSkills[pin.SkillID] = true
		skillIDs = append(skillIDs, pin.SkillID)
	}
	if !reflect.DeepEqual(skillIDs, item.SkillIDs) {
		return errors.New("skillIds do not match ordered Skill pins")
	}
	return nil
}

func uniqueSkillBySlug(library []skill, slug string) (skill, bool, error) {
	var result skill
	count := 0
	for _, item := range library {
		if item.Slug != slug {
			continue
		}
		result = item
		count++
	}
	if count > 1 {
		return skill{}, false, fmt.Errorf("Library contains %d active Skills with slug %s", count, slug)
	}
	if count == 0 {
		return skill{}, false, nil
	}
	if result.ID == "" || result.CurrentVersionID == "" || result.CurrentSHA256 == "" {
		return skill{}, false, fmt.Errorf("Library Skill %s omitted current identity fields", slug)
	}
	return result, true, nil
}

func buildReport(profilesByName map[string]profile, policySkill skill, skillExists bool, library []skill) []profileChange {
	names := profileNames(profilesByName)
	result := make([]profileChange, 0, len(names))
	for _, name := range names {
		current := profilesByName[name]
		change := profileChange{
			Name:           name,
			BeforeRevision: current.Revision,
			AfterRevision:  current.Revision,
			Add:            []string{},
			Remove:         []string{},
		}
		if !profileHasSlug(current, "requesting-code-review") {
			change.Add = append(change.Add, "requesting-code-review")
		}
		if codingProfiles[name] {
			if !profileHasSlug(current, policySkillSlug) {
				change.Add = append(change.Add, policySkillSlug)
			}
			for _, slug := range orchestrationSkillSlugs {
				if !profileHasSlug(current, slug) {
					change.Add = append(change.Add, slug)
				}
			}
		} else if frontendProfiles[name] {
			for _, slug := range frontendBundleSkillSlugs {
				if !profileHasSlug(current, slug) {
					change.Add = append(change.Add, slug)
				}
			}
			for _, pin := range current.Skills {
				if !frontendProfileKeepsSlug(pin.Slug, library) {
					change.Remove = append(change.Remove, pin.Slug)
				}
			}
		} else {
			if profileHasSlug(current, policySkillSlug) {
				change.Remove = append(change.Remove, policySkillSlug)
			}
		}
		pinChanged := false
		for _, pin := range current.Skills {
			if pin.Slug == legacySkillSlug {
				change.Remove = appendUnique(change.Remove, legacySkillSlug)
			}
			if codingProfiles[name] && pin.Slug == policySkillSlug && skillExists &&
				(pin.SkillID != policySkill.ID || pin.VersionID != policySkill.CurrentVersionID) {
				pinChanged = true
			}
		}
		if len(change.Add) > 0 || len(change.Remove) > 0 || pinChanged {
			change.AfterRevision++
		}
		result = append(result, change)
	}
	return result
}

func desiredProfile(current profile, policySkill skill, library []skill) (profileInput, bool, error) {
	input := profileInput{
		Name:            current.Name,
		Description:     current.Description,
		Revision:        current.Revision,
		SkillIDs:        []string{},
		SkillVersionIDs: map[string]string{},
	}
	if !profileHasSlug(current, "requesting-code-review") {
		reviewSkill, found, err := uniqueSkillBySlug(library, "requesting-code-review")
		if err != nil {
			return profileInput{}, false, err
		}
		if !found {
			return profileInput{}, false, errors.New("required Library Skill requesting-code-review is missing")
		}
		input.SkillIDs = append(input.SkillIDs, reviewSkill.ID)
		input.SkillVersionIDs[reviewSkill.ID] = reviewSkill.CurrentVersionID
	}
	foundPolicy := false
	role := profileRoleFor(current.Name)
	for _, pin := range current.Skills {
		if !profileKeepsPin(pin.Slug, role, library) {
			continue
		}
		if pin.Slug == policySkillSlug {
			if foundPolicy {
				return profileInput{}, false, fmt.Errorf("Profile %s contains duplicate %s membership", current.Name, policySkillSlug)
			}
			foundPolicy = true
			input.SkillIDs = append(input.SkillIDs, policySkill.ID)
			input.SkillVersionIDs[policySkill.ID] = policySkill.CurrentVersionID
			continue
		}
		input.SkillIDs = append(input.SkillIDs, pin.SkillID)
		input.SkillVersionIDs[pin.SkillID] = pin.VersionID
	}
	if role == profileRoleCoding && !foundPolicy {
		if policySkill.ID == "" || policySkill.CurrentVersionID == "" {
			return profileInput{}, false, errors.New("coding Profile requires an uploaded subagent-routing Skill")
		}
		input.SkillIDs = append(input.SkillIDs, policySkill.ID)
		input.SkillVersionIDs[policySkill.ID] = policySkill.CurrentVersionID
	}
	if role == profileRoleCoding {
		for _, slug := range orchestrationSkillSlugs {
			orchestration, found, err := uniqueSkillBySlug(library, slug)
			if err != nil {
				return profileInput{}, false, err
			}
			if !found {
				return profileInput{}, false, fmt.Errorf("required Library Skill %s is missing", slug)
			}
			if !profileHasSlug(current, slug) {
				input.SkillIDs = append(input.SkillIDs, orchestration.ID)
				input.SkillVersionIDs[orchestration.ID] = orchestration.CurrentVersionID
			}
		}
	} else if role == profileRoleFrontend {
		for _, slug := range frontendBundleSkillSlugs {
			frontendSkill, found, err := uniqueSkillBySlug(library, slug)
			if err != nil {
				return profileInput{}, false, err
			}
			if !found {
				return profileInput{}, false, fmt.Errorf("required frontend Kimi Skill %s is missing", slug)
			}
			if !profileHasSlug(current, slug) {
				input.SkillIDs = append(input.SkillIDs, frontendSkill.ID)
				input.SkillVersionIDs[frontendSkill.ID] = frontendSkill.CurrentVersionID
			}
		}
	}
	currentSkillPins := map[string]string{}
	for _, pin := range current.Skills {
		currentSkillPins[pin.SkillID] = pin.VersionID
	}
	changed := !reflect.DeepEqual(current.SkillIDs, input.SkillIDs) ||
		!reflect.DeepEqual(currentSkillPins, input.SkillVersionIDs)
	return input, changed, nil
}

func verifyState(library []skill, profilesByName map[string]profile, policySkill skill) error {
	for _, slug := range retainedLibrarySlugs {
		if _, found, err := uniqueSkillBySlug(library, slug); err != nil {
			return err
		} else if !found {
			return fmt.Errorf("required Library Skill %s is missing", slug)
		}
	}
	for _, name := range profileNames(profilesByName) {
		item := profilesByName[name]
		matched := 0
		for _, pin := range item.Skills {
			if pin.Slug == policySkillSlug {
				matched++
				if pin.SkillID != policySkill.ID || pin.VersionID != policySkill.CurrentVersionID {
					return fmt.Errorf("Profile %s does not pin the exact %s version", name, policySkillSlug)
				}
			}
			if pin.Slug == legacySkillSlug {
				return fmt.Errorf("Profile %s still contains legacy Skill %s", name, legacySkillSlug)
			}
		}
		role := profileRoleFor(name)
		if role == profileRoleCoding {
			if matched != 1 {
				return fmt.Errorf("Profile %s must contain %s exactly once, found %d", name, policySkillSlug, matched)
			}
			for _, slug := range orchestrationSkillSlugs {
				if !profileHasSlug(item, slug) {
					return fmt.Errorf("Profile %s must retain orchestration Skill %s", name, slug)
				}
			}
		} else if matched != 0 {
			return fmt.Errorf("Profile %s must not contain %s", name, policySkillSlug)
		}
		if role == profileRoleFrontend {
			for _, slug := range frontendBundleSkillSlugs {
				if !profileHasSlug(item, slug) {
					return fmt.Errorf("Profile %s must contain frontend Kimi Skill %s", name, slug)
				}
			}
			for _, pin := range item.Skills {
				if !frontendProfileKeepsSlug(pin.Slug, library) {
					return fmt.Errorf("Profile %s still contains non-default frontend Skill %s", name, pin.Slug)
				}
			}
		}
		if !profileHasSlug(item, "requesting-code-review") {
			return fmt.Errorf("Profile %s is missing requesting-code-review", name)
		}
	}
	return nil
}

type profileRole string

const (
	profileRoleCoding   profileRole = "coding"
	profileRoleFrontend profileRole = "frontend"
	profileRoleOther    profileRole = "other"
)

func profileRoleFor(name string) profileRole {
	if codingProfiles[name] || strings.HasSuffix(name, "-coding") {
		return profileRoleCoding
	}
	if frontendProfiles[name] || strings.HasSuffix(name, "-frontend-ui") {
		return profileRoleFrontend
	}
	return profileRoleOther
}

func frontendProfileKeepsSlug(slug string, library []skill) bool {
	if slug == "requesting-code-review" || hasString(frontendBundleSkillSlugs, slug) {
		return true
	}
	for _, item := range library {
		if item.Slug == slug && slug != legacySkillSlug && hasString(item.Tags, "required") {
			return true
		}
	}
	return false
}

func profileKeepsPin(slug string, role profileRole, library []skill) bool {
	if slug == legacySkillSlug {
		return false
	}
	if slug == policySkillSlug && role != profileRoleCoding {
		return false
	}
	if role == profileRoleFrontend {
		if frontendProfileKeepsSlug(slug, library) {
			return true
		}
		for _, item := range library {
			if item.Slug == slug && hasString(item.Tags, "required") && slug != legacySkillSlug {
				return true
			}
		}
		return false
	}
	return true
}

func appendUnique(values []string, value string) []string {
	if hasString(values, value) {
		return values
	}
	return append(values, value)
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func profileNames(profilesByName map[string]profile) []string {
	result := make([]string, 0, len(profilesByName))
	for name := range profilesByName {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func profileHasSlug(item profile, slug string) bool {
	for _, pin := range item.Skills {
		if pin.Slug == slug {
			return true
		}
	}
	return false
}
