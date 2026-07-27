package market

import (
	"context"
	"errors"
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
		if _, err := client.SearchRaw(context.Background(), "deploy", 1, 24); err != nil {
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
	if _, err := client.SearchRaw(context.Background(), "deploy", 1, 24); err != ErrRateLimited {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

func TestSkillsMPSearchParsesNormalizedResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"skills":[{"id":"obra-tdd","name":"test-driven-development","author":"obra","description":"Use when implementing any feature","githubUrl":"https://github.com/obra/superpowers/tree/main/skills/tdd","skillUrl":"https://skillsmp.com/creators/obra/superpowers/skills-tdd","stars":259212,"updatedAt":1781629783}],"pagination":{"page":1,"limit":24}}}`))
	}))
	defer server.Close()
	client := New(server.URL, "")
	items, err := client.Search(context.Background(), "tdd", 1, 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one result, got %d", len(items))
	}
	item := items[0]
	if item.Source != "skillsmp" || item.ID != "obra-tdd" || item.Name != "test-driven-development" || item.Author != "obra" {
		t.Fatalf("unexpected normalized result: %+v", item)
	}
	if item.GitHubURL == "" || item.SourceURL == "" || item.Stars != 259212 || item.UpdatedAt == "" {
		t.Fatalf("missing provenance fields: %+v", item)
	}
}

func TestMultiSearchToleratesPartialFailure(t *testing.T) {
	failing := New("http://127.0.0.1:1", "")
	healthyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/skills" || r.URL.Query().Get("search") != "deploy" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"skills":[{"id":"abc","name":"部署助手","owner_name":"爪爪","current_version":"1.0.0","downloads":42,"star_count":7,"status":"official","category":["效率"],"tags":["部署"],"updated_at":"2026-01-01T00:00:00+08:00"}],"total":1,"hasMore":false}`))
	}))
	defer healthyServer.Close()
	multi := NewMulti(failing, NewXiaping(healthyServer.URL, ""))
	response, err := multi.Search(context.Background(), "all", "deploy", 1, 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 {
		t.Fatalf("expected the healthy source result, got %+v", response.Items)
	}
	item := response.Items[0]
	if item.Source != "xiaping" || item.Downloads != 42 || item.Reviews != 7 || item.Status != "official" || item.Version != "1.0.0" {
		t.Fatalf("unexpected xiaping mapping: %+v", item)
	}
	if item.SourceURL == "" || len(item.Categories) != 1 || len(item.Tags) != 1 {
		t.Fatalf("missing xiaping provenance fields: %+v", item)
	}
	if response.Errors["skillsmp"] == "" {
		t.Fatalf("expected a skillsmp failure entry, got %+v", response.Errors)
	}
	if response.Errors["skillsmp"] != "source unavailable" {
		t.Fatalf("provider internals leaked through the partial error: %+v", response.Errors)
	}
}

func TestSafeExternalURLRejectsExecutableSchemes(t *testing.T) {
	if got := safeExternalURL("javascript:alert(1)"); got != "" {
		t.Fatalf("unsafe external URL accepted: %q", got)
	}
	if got := safeExternalURL("https://example.com/skill"); got == "" {
		t.Fatal("https external URL was rejected")
	}
}

func TestMultiSearchTotalFailureJoinsErrors(t *testing.T) {
	rateLimited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	defer rateLimited.Close()
	multi := NewMulti(New(rateLimited.URL, ""))
	if _, err := multi.Search(context.Background(), "skillsmp", "deploy", 1, 24); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

func TestMultiSearchValidatesInput(t *testing.T) {
	multi := NewMulti(New("http://127.0.0.1:1", ""))
	if _, err := multi.Search(context.Background(), "nope", "deploy", 1, 24); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("expected ErrUnknownSource, got %v", err)
	}
	if _, err := multi.Search(context.Background(), "all", "x", 1, 24); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("expected ErrInvalidQuery, got %v", err)
	}
}
