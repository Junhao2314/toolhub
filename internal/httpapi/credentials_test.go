package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveTemporaryPassword(t *testing.T) {
	random, returned, err := resolveTemporaryPassword("", "")
	if err != nil || random == "" || random != returned || len(random) < 24 {
		t.Fatalf("random mode failed: passwordLength=%d returned=%t err=%v", len(random), returned != "", err)
	}
	manual := "correct horse battery staple"
	password, returned, err := resolveTemporaryPassword("manual", manual)
	if err != nil || password != manual || returned != "" {
		t.Fatalf("manual mode failed: returned=%q err=%v", returned, err)
	}
	if _, _, err := resolveTemporaryPassword("manual", "too-short"); err == nil {
		t.Fatal("weak manual password was accepted")
	}
	if _, _, err := resolveTemporaryPassword("random", manual); err == nil {
		t.Fatal("random mode accepted a manual password field")
	}
	if _, _, err := resolveTemporaryPassword("unknown", ""); err == nil {
		t.Fatal("unknown password mode was accepted")
	}
}

func TestCredentialJSONRejectsUnknownFields(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"identifier":"admin","password":"correct horse battery staple","extra":true}`))
	response := httptest.NewRecorder()
	var input struct {
		Identifier string `json:"identifier"`
		Password   string `json:"password"`
	}
	if err := decodeJSON(response, request, &input, 64<<10); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field rejection, got %v", err)
	}
}
