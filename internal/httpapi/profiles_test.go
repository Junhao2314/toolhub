package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Junhao2314/toolhub/internal/store"
)

func TestWriteActivationConflictReturnsOnlySecretKeyNames(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/example/preflight", nil)
	writeActivationConflict(recorder, request, store.ActivationPreflight{
		Errors:           []store.ActivationIssue{{Code: "remote_secret_confirmation_required", Scope: "mcp"}},
		RemoteSecretKeys: []string{"TAVILY_API_KEY"},
		NodeName:         "remote-node",
	})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code       string                  `json:"code"`
			NodeName   string                  `json:"nodeName"`
			SecretKeys []string                `json:"secretKeys"`
			Issues     []store.ActivationIssue `json:"issues"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "remote_secret_confirmation_required" || body.Error.NodeName != "remote-node" || len(body.Error.SecretKeys) != 1 || body.Error.SecretKeys[0] != "TAVILY_API_KEY" || len(body.Error.Issues) != 1 {
		t.Fatalf("response=%+v", body)
	}
}

func TestHandleStoreErrorMapsProfileManagedTarget(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/skills/example/deployments", nil)
	(&API{}).handleStoreError(recorder, request, store.ErrTargetManagedByProfile)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "target_managed_by_profile" {
		t.Fatalf("code=%q", body.Error.Code)
	}
}
