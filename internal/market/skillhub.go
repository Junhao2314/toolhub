package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultSkillHubBaseURL is the public SkillHub (技能广场) API origin.
const DefaultSkillHubBaseURL = "https://api.skillhub.tencent.com"

// SkillHubClient is the SkillHub (技能广场) marketplace source. Both search and
// downloads are public; no API key is required.
type SkillHubClient struct {
	baseURL     string
	http        *http.Client
	archiveHTTP *http.Client
	// validateURL guards the provider-supplied download URL against SSRF; tests may override it.
	validateURL func(ctx context.Context, rawURL string) error
}

// NewSkillHub creates a new SkillHub marketplace client.
func NewSkillHub(baseURL string) *SkillHubClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultSkillHubBaseURL
	}
	return &SkillHubClient{
		baseURL:     baseURL,
		http:        &http.Client{Timeout: 30 * time.Second},
		validateURL: validatePublicHTTPSURL,
	}
}

func (c *SkillHubClient) Name() string { return "skillhub" }

// PageURL returns the human-readable skill page on the marketplace.
func (c *SkillHubClient) PageURL(slug string) string {
	return "https://skillhub.cn/hermes/skill/" + url.PathEscape(slug)
}

// SkillHubSearchResult is a single skill from the SkillHub search API.
type skillHubSearchItem struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	DescriptionZH string `json:"description_zh"`
	OwnerName     string `json:"ownerName"`
	Downloads     int64  `json:"downloads"`
	Stars         int64  `json:"stars"`
	Version       string `json:"version"`
	Category      string `json:"category"`
	UpdatedAt     int64  `json:"updated_at"` // Unix millisecond timestamp
}

// Search queries the public SkillHub skill listing.
func (c *SkillHubClient) Search(ctx context.Context, query string, page, limit int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if (len(query) > 0 && len(query) < 2) || len(query) > 200 {
		return nil, ErrInvalidQuery
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 24
	}
	endpoint, err := url.Parse(c.baseURL + "/api/skills")
	if err != nil {
		return nil, err
	}
	values := endpoint.Query()
	if query != "" {
		values.Set("keyword", query)
	}
	values.Set("page", strconv.Itoa(page))
	values.Set("pageSize", strconv.Itoa(limit))
	endpoint.RawQuery = values.Encode()
	body, err := c.get(ctx, endpoint.String())
	if err != nil {
		return nil, fmt.Errorf("search SkillHub: %w", err)
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Skills []skillHubSearchItem `json:"skills"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse SkillHub response: %w", err)
	}
	if payload.Code != 0 {
		return nil, errors.New("SkillHub rejected the search request")
	}
	items := make([]Result, 0, len(payload.Data.Skills))
	for _, skill := range payload.Data.Skills {
		desc := skill.DescriptionZH
		if desc == "" {
			desc = skill.Name
		}
		item := Result{
			Source:      c.Name(),
			ID:          skill.Slug,
			Name:        skill.Name,
			Description: desc,
			Author:      skill.OwnerName,
			Downloads:   skill.Downloads,
			Stars:       skill.Stars,
			Version:     skill.Version,
			SourceURL:   safeExternalURL(c.PageURL(skill.Slug)),
			Categories:  []string{skill.Category},
		}
		if skill.UpdatedAt > 0 {
			item.UpdatedAt = time.UnixMilli(skill.UpdatedAt).UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}
	return items, nil
}

func (c *SkillHubClient) get(ctx context.Context, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ToolHub/0.1")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call SkillHub: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return body, ErrRateLimited
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return body, fmt.Errorf("SkillHub returned HTTP %d", response.StatusCode)
	}
	return body, nil
}

// SkillHubDownload is the result of downloading a SkillHub skill archive.
type SkillHubDownload struct {
	Archive   []byte
	Version   string
	SkillPage string
}

// Download fetches a SkillHub skill ZIP archive by slug and optional version.
func (c *SkillHubClient) Download(ctx context.Context, slug, version string, maxBytes int64) (SkillHubDownload, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" || len(slug) > 200 || strings.ContainsAny(slug, "/\\?# \t\r\n") {
		return SkillHubDownload{}, errors.New("invalid SkillHub skill slug")
	}
	if maxBytes < 1 {
		return SkillHubDownload{}, errors.New("SkillHub archive size limit must be positive")
	}
	endpoint, err := url.Parse(c.baseURL + "/api/v1/download")
	if err != nil {
		return SkillHubDownload{}, err
	}
	values := endpoint.Query()
	values.Set("slug", slug)
	if version != "" {
		values.Set("version", version)
	}
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return SkillHubDownload{}, err
	}
	request.Header.Set("User-Agent", "ToolHub/0.1")
	baseClient := c.archiveHTTP
	if baseClient == nil {
		baseClient = newPublicHTTPSClient()
	}
	client := *baseClient
	client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
		return c.validateURL(request.Context(), request.URL.String())
	}
	response, err := client.Do(request)
	if err != nil {
		return SkillHubDownload{}, fmt.Errorf("download SkillHub archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return SkillHubDownload{}, fmt.Errorf("SkillHub archive returned HTTP %d", response.StatusCode)
	}
	archive, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return SkillHubDownload{}, err
	}
	if int64(len(archive)) > maxBytes {
		return SkillHubDownload{}, errors.New("SkillHub archive exceeds the skill package size limit")
	}
	return SkillHubDownload{Archive: archive, Version: version, SkillPage: c.PageURL(slug)}, nil
}