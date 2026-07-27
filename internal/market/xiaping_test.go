package market

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestXiapingSearchDoesNotSendDownloadKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("public search leaked the download key: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"skills":[],"total":0,"hasMore":false}`))
	}))
	defer server.Close()
	client := NewXiaping(server.URL, "sk_test")
	if !client.Configured() {
		t.Fatal("expected the client to report configured")
	}
	if _, err := client.Search(context.Background(), "部署", 1, 24); err != nil {
		t.Fatal(err)
	}
}

func TestXiapingSearchErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"error":"boom"}`))
	}))
	defer server.Close()
	client := NewXiaping(server.URL, "")
	if _, err := client.Search(context.Background(), "部署", 1, 24); err == nil || strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected a sanitized provider error, got %v", err)
	}
}

func TestXiapingDownloadFetchesArchive(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/skills/skl_1/download":
			if got := r.Header.Get("Authorization"); got != "Bearer sk_test" {
				t.Fatalf("expected bearer key, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"download_url":"` + server.URL + `/pkg.zip","version":"1.2.3","coins_spent":2}}`))
		case "/pkg.zip":
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("the API key must not leak to the download host, got %q", got)
			}
			_, _ = w.Write([]byte("PK\x03\x04archive-bytes"))
		default:
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
	}))
	defer server.Close()
	client := NewXiaping(server.URL, "sk_test")
	client.archiveHTTP = server.Client()
	var validated string
	client.validateURL = func(_ context.Context, rawURL string) error {
		validated = rawURL
		return nil
	}
	download, err := client.Download(context.Background(), "skl_1", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if validated == "" {
		t.Fatal("expected the download URL to pass through SSRF validation")
	}
	if download.Version != "1.2.3" || download.CoinsSpent != 2 || download.SkillPage == "" {
		t.Fatalf("unexpected download metadata: %+v", download)
	}
	if string(download.Archive) != "PK\x03\x04archive-bytes" {
		t.Fatalf("unexpected archive bytes: %q", string(download.Archive))
	}
}

func TestXiapingDownloadRejectsCrossOriginAPIKeyRedirect(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { redirected = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer source.Close()
	client := NewXiaping(source.URL, "sk_test")
	if _, err := client.Download(context.Background(), "skl_1", 1<<20); err == nil {
		t.Fatal("expected cross-origin authenticated redirect to be rejected")
	}
	if redirected {
		t.Fatal("cross-origin redirect reached the target")
	}
}

func TestXiapingDownloadRequiresKeyAndValidID(t *testing.T) {
	client := NewXiaping("https://xiaping.coze.com", "")
	if _, err := client.Download(context.Background(), "skl_1", 1<<20); err == nil {
		t.Fatal("expected an error without an API key")
	}
	keyed := NewXiaping("https://xiaping.coze.com", "sk_test")
	if _, err := keyed.Download(context.Background(), "../escape", 1<<20); err == nil {
		t.Fatal("expected an invalid skill id error")
	}
}

func TestXiapingDownloadProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"error":"虾米余额不足"}`))
	}))
	defer server.Close()
	client := NewXiaping(server.URL, "sk_test")
	if _, err := client.Download(context.Background(), "skl_1", 1<<20); err == nil || strings.Contains(err.Error(), "虾米余额不足") {
		t.Fatalf("expected a sanitized provider error, got %v", err)
	}
}

func TestValidatePublicHTTPSURL(t *testing.T) {
	if err := validatePublicHTTPSURL(context.Background(), "http://example.com/pkg.zip"); err == nil {
		t.Fatal("expected http URLs to be rejected")
	}
	if err := validatePublicHTTPSURL(context.Background(), "https://user:pass@example.com/pkg.zip"); err == nil {
		t.Fatal("expected embedded credentials to be rejected")
	}
	if err := validatePublicHTTPSURL(context.Background(), "https://localhost/pkg.zip"); err == nil {
		t.Fatal("expected localhost to be rejected")
	}
	if err := validatePublicHTTPSURL(context.Background(), "https://127.0.0.1/pkg.zip"); err == nil {
		t.Fatal("expected loopback IPs to be rejected")
	}
	if isPublicAddress(netip.MustParseAddr("100.64.0.1")) {
		t.Fatal("expected carrier-grade NAT space to be rejected")
	}
	if err := validatePublicHTTPSURL(context.Background(), "https://1.1.1.1/pkg.zip"); err != nil {
		t.Fatalf("expected a public https URL to pass, got %v", err)
	}
}

func TestXiapingDownloadRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"success":false,"error":"secret provider detail"}`))
	}))
	defer server.Close()
	client := NewXiaping(server.URL, "sk_test")
	if _, err := client.Download(context.Background(), "skl_1", 1<<20); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}
