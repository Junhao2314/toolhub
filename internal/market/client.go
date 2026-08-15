package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrRateLimited marks an exhausted provider quota (HTTP 429).
	ErrRateLimited = errors.New("marketplace rate limit exhausted")
	// ErrUnknownSource marks a marketplace source selector that matches no configured provider.
	ErrUnknownSource = errors.New("unknown marketplace source")
	// ErrInvalidQuery marks a search query outside the accepted length bounds.
	ErrInvalidQuery = errors.New("search query must contain 2-200 characters")
)

// Result is a normalized marketplace listing entry independent of the provider payload shape.
type Result struct {
	Source      string   `json:"source"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Stars       int64    `json:"stars,omitempty"`
	Downloads   int64    `json:"downloads,omitempty"`
	Reviews     int64    `json:"reviews,omitempty"`
	Version     string   `json:"version,omitempty"`
	Status      string   `json:"status,omitempty"`
	GitHubURL   string   `json:"githubUrl,omitempty"`
	SourceURL   string   `json:"sourceUrl,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	UpdatedAt   string   `json:"updatedAt,omitempty"`
}

// SearchResponse is the normalized, provider-agnostic search payload returned to the UI.
// Errors carries per-source failure messages when at least one source still succeeded.
type SearchResponse struct {
	Items  []Result          `json:"items"`
	Page   int               `json:"page"`
	Limit  int               `json:"limit"`
	Errors map[string]string `json:"errors,omitempty"`
}

// Source is a searchable marketplace provider.
type Source interface {
	Name() string
	Search(ctx context.Context, query string, page, limit int) ([]Result, error)
}

// Multi fans a search out over the configured marketplace sources and tolerates
// partial failures so an unreachable provider never blocks the remaining ones.
type Multi struct {
	sources []Source
}

func NewMulti(sources ...Source) *Multi {
	return &Multi{sources: sources}
}

// Lookup returns the source registered under name.
func (m *Multi) Lookup(name string) (Source, bool) {
	for _, source := range m.sources {
		if source.Name() == name {
			return source, true
		}
	}
	return nil, false
}

// Xiaping returns the configured Xiaping source, if any.
func (m *Multi) Xiaping() (*XiapingClient, bool) {
	for _, source := range m.sources {
		if xiaping, ok := source.(*XiapingClient); ok {
			return xiaping, true
		}
	}
	return nil, false
}

// SkillHub returns the configured SkillHub source, if any.
func (m *Multi) SkillHub() (*SkillHubClient, bool) {
	for _, source := range m.sources {
		if skillhub, ok := source.(*SkillHubClient); ok {
			return skillhub, true
		}
	}
	return nil, false
}

func (m *Multi) selectSources(selector string) ([]Source, error) {
	selector = strings.ToLower(strings.TrimSpace(selector))
	if selector == "" || selector == "all" {
		if len(m.sources) == 0 {
			return nil, fmt.Errorf("%w: no sources configured", ErrUnknownSource)
		}
		return m.sources, nil
	}
	source, ok := m.Lookup(selector)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownSource, selector)
	}
	return []Source{source}, nil
}

// Search queries the selected sources concurrently. A total failure (every selected
// source errored) returns a joined error; a partial failure returns the successful
// items alongside per-source error messages.
func (m *Multi) Search(ctx context.Context, selector, query string, page, limit int) (SearchResponse, error) {
	query = strings.TrimSpace(query)
	if (len(query) > 0 && len(query) < 2) || len(query) > 200 {
		return SearchResponse{}, ErrInvalidQuery
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 24
	}
	sources, err := m.selectSources(selector)
	if err != nil {
		return SearchResponse{}, err
	}
	type outcome struct {
		name  string
		items []Result
		err   error
	}
	results := make(chan outcome, len(sources))
	for _, source := range sources {
		go func(source Source) {
			items, err := source.Search(ctx, query, page, limit)
			for index := range items {
				if items[index].Source == "" {
					items[index].Source = source.Name()
				}
			}
			results <- outcome{name: source.Name(), items: items, err: err}
		}(source)
	}
	response := SearchResponse{Items: []Result{}, Page: page, Limit: limit}
	failures := make([]error, 0, len(sources))
	for range sources {
		result := <-results
		if result.err != nil {
			if response.Errors == nil {
				response.Errors = map[string]string{}
			}
			response.Errors[result.name] = publicSourceError(result.err)
			failures = append(failures, fmt.Errorf("%s: %w", result.name, result.err))
			continue
		}
		response.Items = append(response.Items, result.items...)
	}
	if len(failures) == len(sources) {
		return SearchResponse{}, errors.Join(failures...)
	}
	sort.SliceStable(response.Items, func(i, j int) bool {
		if response.Items[i].Source != response.Items[j].Source {
			return response.Items[i].Source < response.Items[j].Source
		}
		if response.Items[i].Name != response.Items[j].Name {
			return response.Items[i].Name < response.Items[j].Name
		}
		return response.Items[i].ID < response.Items[j].ID
	})
	return response, nil
}

func publicSourceError(err error) string {
	if errors.Is(err, ErrRateLimited) {
		return "rate limited"
	}
	return "source unavailable"
}

type cacheEntry struct {
	body      json.RawMessage
	expiresAt time.Time
}

// Client is the SkillsMP marketplace source.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	mu      sync.Mutex
	cache   map[string]cacheEntry
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  strings.TrimSpace(apiKey),
		http:    &http.Client{Timeout: 12 * time.Second},
		cache:   map[string]cacheEntry{},
	}
}

func (c *Client) Name() string { return "skillsmp" }

// Search implements Source by parsing the SkillsMP payload into normalized results.
func (c *Client) Search(ctx context.Context, query string, page, limit int) ([]Result, error) {
	body, err := c.SearchRaw(ctx, query, page, limit)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data struct {
			Skills []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Author      string `json:"author"`
				Description string `json:"description"`
				GitHubURL   string `json:"githubUrl"`
				SkillURL    string `json:"skillUrl"`
				Stars       int64  `json:"stars"`
				UpdatedAt   int64  `json:"updatedAt"`
			} `json:"skills"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse SkillsMP response: %w", err)
	}
	items := make([]Result, 0, len(payload.Data.Skills))
	for _, skill := range payload.Data.Skills {
		item := Result{
			Source:      c.Name(),
			ID:          skill.ID,
			Name:        skill.Name,
			Description: skill.Description,
			Author:      skill.Author,
			Stars:       skill.Stars,
			GitHubURL:   safeExternalURL(skill.GitHubURL),
			SourceURL:   safeExternalURL(skill.SkillURL),
		}
		if skill.UpdatedAt > 0 {
			item.UpdatedAt = time.Unix(skill.UpdatedAt, 0).UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}
	return items, nil
}

func safeExternalURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	return parsed.String()
}

// SearchRaw returns the unmodified SkillsMP response body, using a 10 minute cache.
func (c *Client) SearchRaw(ctx context.Context, query string, page, limit int) (json.RawMessage, error) {
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
	searchQ := query
	if searchQ == "" {
		searchQ = "tool"
	}
	key := fmt.Sprintf("%s:%d:%d", strings.ToLower(searchQ), page, limit)
	c.mu.Lock()
	if cached, ok := c.cache[key]; ok && time.Now().Before(cached.expiresAt) {
		body := append(json.RawMessage(nil), cached.body...)
		c.mu.Unlock()
		return body, nil
	}
	c.mu.Unlock()

	endpoint, err := url.Parse(c.baseURL + "/skills/search")
	if err != nil {
		return nil, err
	}
	values := endpoint.Query()
	values.Set("q", searchQ)
	values.Set("page", strconv.Itoa(page))
	values.Set("limit", strconv.Itoa(limit))
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ToolHub/0.1")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("search SkillsMP: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("SkillsMP returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, errors.New("SkillsMP returned invalid JSON")
	}
	c.mu.Lock()
	c.cache[key] = cacheEntry{body: append(json.RawMessage(nil), body...), expiresAt: time.Now().Add(10 * time.Minute)}
	if len(c.cache) > 500 {
		for cacheKey, value := range c.cache {
			if time.Now().After(value.expiresAt) {
				delete(c.cache, cacheKey)
			}
		}
	}
	c.mu.Unlock()
	return body, nil
}
