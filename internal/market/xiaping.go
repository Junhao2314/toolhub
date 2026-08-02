package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultXiapingBaseURL is the public Xiaping (虾评) marketplace origin.
const DefaultXiapingBaseURL = "https://xiaping.coze.com"

// XiapingClient is the Xiaping (虾评) marketplace source. Search and listings are
// public; downloading a skill archive requires an API key and may charge the
// account's platform coins.
type XiapingClient struct {
	baseURL     string
	apiKey      string
	http        *http.Client
	archiveHTTP *http.Client
	// validateURL guards the provider-supplied download URL against SSRF; tests may override it.
	validateURL func(ctx context.Context, rawURL string) error
}

func NewXiaping(baseURL, apiKey string) *XiapingClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultXiapingBaseURL
	}
	return &XiapingClient{
		baseURL:     baseURL,
		apiKey:      strings.TrimSpace(apiKey),
		http:        &http.Client{Timeout: 30 * time.Second},
		validateURL: validatePublicHTTPSURL,
	}
}

func (c *XiapingClient) Name() string { return "xiaping" }

// Configured reports whether an API key is available for authenticated calls (downloads).
func (c *XiapingClient) Configured() bool { return c.apiKey != "" }

// PageURL returns the human-readable skill page on the marketplace.
func (c *XiapingClient) PageURL(skillID string) string {
	return c.baseURL + "/skill/" + url.PathEscape(skillID)
}

func (c *XiapingClient) get(ctx context.Context, endpoint string, authenticated bool) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ToolHub/0.1")
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	client := *c.http
	if authenticated {
		client.CheckRedirect = func(redirect *http.Request, via []*http.Request) error {
			if len(via) == 0 || !sameOrigin(redirect.URL, via[0].URL) {
				return errors.New("Xiaping API redirect left the configured origin")
			}
			return nil
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Xiaping: %w", err)
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
		return body, fmt.Errorf("Xiaping returned HTTP %d", response.StatusCode)
	}
	return body, nil
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

type xiapingSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	OwnerName   string   `json:"owner_name"`
	Version     string   `json:"current_version"`
	Downloads   int64    `json:"downloads"`
	StarCount   int64    `json:"star_count"`
	Status      string   `json:"status"`
	Categories  []string `json:"category"`
	Tags        []string `json:"tags"`
	UpdatedAt   string   `json:"updated_at"`
}

// Search queries the public Xiaping skill listing.
func (c *XiapingClient) Search(ctx context.Context, query string, page, limit int) ([]Result, error) {
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
		values.Set("search", query)
	}
	values.Set("page", strconv.Itoa(page))
	values.Set("limit", strconv.Itoa(limit))
	endpoint.RawQuery = values.Encode()
	body, err := c.get(ctx, endpoint.String(), false)
	if err != nil {
		return nil, fmt.Errorf("search Xiaping: %w", err)
	}
	var payload struct {
		Skills  []xiapingSkill `json:"skills"`
		Success *bool          `json:"success"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse Xiaping response: %w", err)
	}
	if payload.Success != nil && !*payload.Success {
		return nil, errors.New("Xiaping rejected the search request")
	}
	items := make([]Result, 0, len(payload.Skills))
	for _, skill := range payload.Skills {
		items = append(items, Result{
			Source:      c.Name(),
			ID:          skill.ID,
			Name:        skill.Name,
			Description: skill.Description,
			Author:      skill.OwnerName,
			Downloads:   skill.Downloads,
			Reviews:     skill.StarCount,
			Version:     skill.Version,
			Status:      normalizeXiapingStatus(skill.Status),
			SourceURL:   safeExternalURL(c.PageURL(skill.ID)),
			Categories:  skill.Categories,
			Tags:        skill.Tags,
			UpdatedAt:   skill.UpdatedAt,
		})
	}
	return items, nil
}

func normalizeXiapingStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "trial":
		return "trial"
	case "official":
		return "official"
	default:
		return ""
	}
}

// Download is a validated-import fetch of a Skill ZIP archive. It requires the
// configured API key; official skills charge the account's platform coins.
type Download struct {
	Archive    []byte
	Version    string
	CoinsSpent int64
	SkillPage  string
}

func (c *XiapingClient) Download(ctx context.Context, skillID string, maxBytes int64) (Download, error) {
	if !c.Configured() {
		return Download{}, errors.New("XIAPING_API_KEY is not configured on the control plane")
	}
	skillID = strings.TrimSpace(skillID)
	if skillID == "" || len(skillID) > 128 || strings.ContainsAny(skillID, "/\\?# \t\r\n") {
		return Download{}, errors.New("invalid Xiaping skill id")
	}
	if maxBytes < 1 {
		return Download{}, errors.New("Xiaping archive size limit must be positive")
	}
	body, err := c.get(ctx, c.baseURL+"/api/skills/"+url.PathEscape(skillID)+"/download", true)
	if err != nil {
		return Download{}, fmt.Errorf("request Xiaping download: %w", err)
	}
	var envelope struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Data    struct {
			DownloadURL string `json:"download_url"`
			Version     string `json:"version"`
			CoinsSpent  int64  `json:"coins_spent"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Download{}, fmt.Errorf("parse Xiaping download response: %w", err)
	}
	if !envelope.Success {
		return Download{}, errors.New("Xiaping rejected the download request")
	}
	downloadURL := strings.TrimSpace(envelope.Data.DownloadURL)
	if downloadURL == "" {
		return Download{}, errors.New("Xiaping returned no download URL")
	}
	if err := c.validateURL(ctx, downloadURL); err != nil {
		return Download{}, err
	}
	baseClient := c.archiveHTTP
	if baseClient == nil {
		baseClient = newPublicHTTPSClient()
	}
	client := *baseClient
	client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
		return c.validateURL(request.Context(), request.URL.String())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return Download{}, err
	}
	request.Header.Set("User-Agent", "ToolHub/0.1")
	response, err := client.Do(request)
	if err != nil {
		return Download{}, fmt.Errorf("download Xiaping archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Download{}, fmt.Errorf("Xiaping archive returned HTTP %d", response.StatusCode)
	}
	archive, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return Download{}, err
	}
	if int64(len(archive)) > maxBytes {
		return Download{}, errors.New("Xiaping archive exceeds the skill package size limit")
	}
	return Download{Archive: archive, Version: envelope.Data.Version, CoinsSpent: envelope.Data.CoinsSpent, SkillPage: c.PageURL(skillID)}, nil
}

func newPublicHTTPSClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialPublicAddress
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}
}

func dialPublicAddress(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := resolvePublicHost(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, resolved := range addresses {
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("connect to public download host: %w", lastErr)
}

// validatePublicHTTPSURL mirrors the Git import boundary: HTTPS only, no embedded
// credentials, and the host must not resolve to a local or private address.
func validatePublicHTTPSURL(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("download URL must be an https URL without embedded credentials")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("local download hosts are not allowed")
	}
	_, err = resolvePublicHost(ctx, host)
	return err
}

var carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")

func resolvePublicHost(ctx context.Context, host string) ([]net.IPAddr, error) {
	var addresses []net.IPAddr
	if literal := net.ParseIP(host); literal != nil {
		addresses = []net.IPAddr{{IP: literal}}
	} else {
		resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve download host: %w", err)
		}
		addresses = resolved
	}
	if len(addresses) == 0 {
		return nil, errors.New("download host resolved to no addresses")
	}
	for _, address := range addresses {
		parsed, ok := netip.AddrFromSlice(address.IP)
		if !ok {
			return nil, errors.New("download host resolved to an invalid address")
		}
		if !isPublicAddress(parsed) {
			return nil, errors.New("download host resolves to a private or local address")
		}
	}
	return addresses, nil
}

func isPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	return address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLoopback() && !address.IsLinkLocalUnicast() && !carrierGradeNAT.Contains(address)
}
