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
	"sync"
	"time"
)

var ErrRateLimited = errors.New("SkillsMP rate limit exhausted")

type cacheEntry struct {
	body      json.RawMessage
	expiresAt time.Time
}

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

func (c *Client) Search(ctx context.Context, query string, page, limit int) (json.RawMessage, error) {
	query = strings.TrimSpace(query)
	if len(query) < 2 || len(query) > 200 {
		return nil, errors.New("search query must contain 2-200 characters")
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 24
	}
	key := fmt.Sprintf("%s:%d:%d", strings.ToLower(query), page, limit)
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
	values.Set("q", query)
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
