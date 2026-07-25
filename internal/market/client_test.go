package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSearchCachesProviderResponse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/skills/search" || r.URL.Query().Get("q") != "deploy" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"name":"Deploy"}]}`))
	}))
	defer server.Close()
	client := New(server.URL, "")
	for index := 0; index < 2; index++ {
		if _, err := client.Search(context.Background(), "deploy", 1, 24); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one provider call, got %d", calls.Load())
	}
}

func TestSearchReportsRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	defer server.Close()
	client := New(server.URL, "")
	if _, err := client.Search(context.Background(), "deploy", 1, 24); err != ErrRateLimited {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}
