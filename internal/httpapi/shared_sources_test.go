package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Junhao2314/toolhub/internal/store"
)

func TestNormalizeSharedSyncScopes(t *testing.T) {
	defaults, err := normalizeSharedSyncScopes(nil)
	if err != nil || len(defaults) != 2 || defaults[0] != "skills" || defaults[1] != "mcp" {
		t.Fatalf("default scopes = %+v err=%v", defaults, err)
	}
	normalized, err := normalizeSharedSyncScopes([]string{" MCP ", "skills", "mcp"})
	if err != nil || len(normalized) != 2 || normalized[0] != "skills" || normalized[1] != "mcp" {
		t.Fatalf("normalized scopes = %+v err=%v", normalized, err)
	}
	if _, err := normalizeSharedSyncScopes([]string{"all"}); err == nil {
		t.Fatal("invalid browser shared sync scope was accepted")
	}
}

func TestHandleStoreErrorMapsSourceFileAuthorityToConflict(t *testing.T) {
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/mcp/servers/server-1", nil)
	response := httptest.NewRecorder()
	handleStoreError(response, request, store.ErrSourceFileAuthoritative)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d", response.Code)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "source_file_authoritative" {
		t.Fatalf("error code = %q", payload.Error.Code)
	}
}
