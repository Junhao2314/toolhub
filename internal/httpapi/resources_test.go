package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestEnrollmentServerURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		configured string
		host       string
		proto      string
		want       string
	}{
		{name: "configured Tailnet origin", configured: "https://toolhub.example.ts.net/", want: "https://toolhub.example.ts.net"},
		{name: "request fallback", host: "127.0.0.1:18480", want: "http://127.0.0.1:18480"},
		{name: "trusted proxy scheme", host: "toolhub.example.ts.net", proto: "https", want: "https://toolhub.example.ts.net"},
		{name: "invalid proxy scheme falls back", host: "toolhub.example.ts.net", proto: "javascript", want: "http://toolhub.example.ts.net"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targetHost := test.host
			if targetHost == "" {
				targetHost = "127.0.0.1:18480"
			}
			request := httptest.NewRequest("POST", "http://"+targetHost+"/api/v1/nodes", nil)
			request.Host = test.host
			request.Header.Set("X-Forwarded-Proto", test.proto)
			got, err := enrollmentServerURL(test.configured, request)
			if err != nil {
				t.Fatalf("enrollmentServerURL() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("enrollmentServerURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEnrollmentServerURLRejectsUnsafeOrigins(t *testing.T) {
	t.Parallel()
	tests := []string{
		"javascript://toolhub.example.ts.net",
		"https://user@toolhub.example.ts.net",
		"https://toolhub.example.ts.net/path",
		"https://toolhub.example.ts.net?query=value",
		"https://toolhub.example.ts.net/$(id)",
	}
	for _, configured := range tests {
		t.Run(configured, func(t *testing.T) {
			request := httptest.NewRequest("POST", "http://127.0.0.1:18480/api/v1/nodes", nil)
			if _, err := enrollmentServerURL(configured, request); err == nil {
				t.Fatal("enrollmentServerURL() error = nil, want error")
			}
		})
	}
}
