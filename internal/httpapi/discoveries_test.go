package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Junhao2314/toolhub/internal/store"
)

func TestWriteHermesImportErrorReturnsStructuredRedactedDetails(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		check      func(t *testing.T, details map[string]any)
	}{
		{
			name: "source changed", err: &store.SourceChangedError{ExpectedSHA256: "old", ObservedSHA256: "new", ExpectedGeneration: 1, ObservedGeneration: 2},
			wantStatus: http.StatusConflict, wantCode: "source_changed",
			check: func(t *testing.T, details map[string]any) {
				if details["expectedSha256"] != "old" || details["observedSha256"] != "new" || details["observedGeneration"] != float64(2) {
					t.Fatalf("details=%+v", details)
				}
			},
		},
		{
			name: "secret confirmation", err: &store.SecretConfirmationRequiredError{EnvKeys: []string{"TOKEN"}, HeaderKeys: []string{"Authorization"}, Targets: []store.MCPAffectedTarget{{NodeID: "node-id", NodeName: "node-a", Runtime: "codex"}}},
			wantStatus: http.StatusConflict, wantCode: "secret_confirmation_required",
			check: func(t *testing.T, details map[string]any) {
				encoded, _ := json.Marshal(details)
				if string(encoded) == "" || containsAny(string(encoded), "secret-value", "secret-id", "fingerprint") {
					t.Fatalf("secret confirmation leaked details: %s", encoded)
				}
			},
		},
		{name: "upgrade required", err: store.ErrAgentUpgradeRequired, wantStatus: http.StatusPreconditionFailed, wantCode: "agent_upgrade_required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/discoveries/id/import-mcp", nil)
			(&API{}).writeHermesImportError(recorder, request, test.err)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var body struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error["code"] != test.wantCode {
				t.Fatalf("code=%v body=%s", body.Error["code"], recorder.Body.String())
			}
			if test.check != nil {
				test.check(t, body.Error)
			}
		})
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if len(candidate) > 0 && len(value) >= len(candidate) {
			for index := 0; index+len(candidate) <= len(value); index++ {
				if value[index:index+len(candidate)] == candidate {
					return true
				}
			}
		}
	}
	return false
}
