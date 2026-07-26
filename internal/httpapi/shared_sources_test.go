package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Junhao2314/toolhub/internal/store"
)

func TestHandleStoreErrorMapsSourceFileAuthorityToConflict(t *testing.T) {
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/mcp/servers/server-1", nil)
	response := httptest.NewRecorder()
	var api *API
	api.handleStoreError(response, request, store.ErrSourceFileAuthoritative)
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

func TestHandleStoreErrorMapsManagedMCPConflicts(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{err: store.ErrManagedMCPProfile, code: "managed_mcp_profile_required"},
		{err: store.ErrMCPProfileRuntime, code: "mcp_profile_runtime_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "/api/v1/mcp/profiles/profile-1/servers", nil)
			response := httptest.NewRecorder()
			var api *API
			api.handleStoreError(response, request, test.err)
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
			if payload.Error.Code != test.code {
				t.Fatalf("error code = %q", payload.Error.Code)
			}
		})
	}
}
