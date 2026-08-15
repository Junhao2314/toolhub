package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/bridgeclient"
	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/store"
)

func TestProfilePreflightRejectsUnsupportedNativeClientBeforeCreatingConfirmations(t *testing.T) {
	tests := []struct {
		name       string
		inspection bridgeprotocol.NativeClientInspectionResponse
		wantCode   string
	}{
		{
			name:       "missing",
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: bridgeprotocol.RuntimeClaude, ErrorCode: "native_client_not_found"},
			wantCode:   "native_client_not_found",
		},
		{
			name:       "below version floor",
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: bridgeprotocol.RuntimeClaude, Version: "2.1.231", ErrorCode: "native_client_version_unsupported"},
			wantCode:   "native_client_version_unsupported",
		},
		{
			name:       "multiple installations",
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: bridgeprotocol.RuntimeClaude, ErrorCode: "native_client_resolution_ambiguous"},
			wantCode:   "native_client_resolution_ambiguous",
		},
		{
			name:       "supported invalid version",
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: bridgeprotocol.RuntimeClaude, Version: "fake", Supported: true},
			wantCode:   "native_client_inspection_invalid",
		},
		{
			name:       "supported below version floor",
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: bridgeprotocol.RuntimeClaude, Version: "2.1.231", Supported: true},
			wantCode:   "native_client_inspection_invalid",
		},
		{
			name:       "supported with error code",
			inspection: bridgeprotocol.NativeClientInspectionResponse{ClientKind: bridgeprotocol.RuntimeClaude, Version: "2.1.232", Supported: true, ErrorCode: "native_client_timeout"},
			wantCode:   "native_client_inspection_invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newHTTPIntegrationStore(t)
			ctx := context.Background()
			if err := st.BootstrapEnvironment(ctx, "http-host", "runner", "UTC", 6276); err != nil {
				t.Fatal(err)
			}
			var skillTargetID, relayTargetID string
			if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/claude'`).Scan(&skillTargetID); err != nil {
				t.Fatal(err)
			}
			if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/shared-relay'`).Scan(&relayTargetID); err != nil {
				t.Fatal(err)
			}
			profile, err := st.SaveProfile(ctx, uuid.NewString(), store.ProfileInput{Name: "claude-http", ClientKind: "claude", Category: "coding"})
			if err != nil {
				t.Fatal(err)
			}

			var preflightCalls atomic.Int32
			client := newHTTPTestBridgeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/native-clients/inspect":
					var input bridgeprotocol.NativeClientInspectionRequest
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Errorf("decode native inspection: %v", err)
					}
					if input.ManagedUsername != "runner" || input.ClientKind != bridgeprotocol.RuntimeClaude {
						t.Errorf("native inspection input=%+v", input)
					}
					writeJSON(w, http.StatusOK, test.inspection)
				case "/v1/targets/preflight":
					preflightCalls.Add(1)
					writeJSON(w, http.StatusOK, bridgeprotocol.PreflightResponse{TargetRevision: strings.Repeat("a", 64)})
				default:
					http.NotFound(w, r)
				}
			}))
			api := &API{store: st, bridge: client, logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
			body := `{"targetIds":["` + skillTargetID + `","` + relayTargetID + `"]}`
			request := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/"+profile.ID+"/preflight", strings.NewReader(body))
			routeContext := chi.NewRouteContext()
			routeContext.URLParams.Add("profileID", profile.ID)
			request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
			recorder := httptest.NewRecorder()

			api.profilePreflight(recorder, request)

			if recorder.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != test.wantCode || preflightCalls.Load() != 0 {
				t.Fatalf("code=%q preflight calls=%d body=%s", envelope.Error.Code, preflightCalls.Load(), recorder.Body.String())
			}
			var confirmationCount int
			if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM preflight_confirmations`).Scan(&confirmationCount); err != nil {
				t.Fatal(err)
			}
			if confirmationCount != 0 {
				t.Fatalf("unsupported native client minted %d confirmations", confirmationCount)
			}
		})
	}
}

func TestProfilePreflightRejectsInvalidBridgeResponseWithoutCreatingConfirmations(t *testing.T) {
	st := newHTTPIntegrationStore(t)
	ctx := context.Background()
	if err := st.BootstrapEnvironment(ctx, "http-host", "runner", "UTC", 6276); err != nil {
		t.Fatal(err)
	}
	var skillTargetID, relayTargetID string
	if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/claude'`).Scan(&skillTargetID); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/shared-relay'`).Scan(&relayTargetID); err != nil {
		t.Fatal(err)
	}
	profile, err := st.SaveProfile(ctx, uuid.NewString(), store.ProfileInput{Name: "claude-invalid-preflight", ClientKind: "claude", Category: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	client := newHTTPTestBridgeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/native-clients/inspect":
			writeJSON(w, http.StatusOK, bridgeprotocol.NativeClientInspectionResponse{ClientKind: "claude", Version: "2.1.232", Supported: true})
		case "/v1/targets/preflight":
			writeJSON(w, http.StatusOK, bridgeprotocol.PreflightResponse{
				TargetRevision: strings.Repeat("a", 64), ManifestHash: strings.Repeat("b", 64),
				Diff: bridgeprotocol.Diff{Add: []bridgeprotocol.DiffItem{}, Replace: []bridgeprotocol.DiffItem{}, Delete: []bridgeprotocol.DiffItem{}, Excluded: []bridgeprotocol.DiffItem{}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	api := &API{store: st, bridge: client, logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/"+profile.ID+"/preflight", strings.NewReader(
		`{"targetIds":["`+skillTargetID+`","`+relayTargetID+`"]}`,
	))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("profileID", profile.ID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()

	api.profilePreflight(recorder, request)

	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), `"code":"relay_response_invalid"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var confirmationCount int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM preflight_confirmations`).Scan(&confirmationCount); err != nil {
		t.Fatal(err)
	}
	if confirmationCount != 0 {
		t.Fatalf("invalid Bridge response minted %d confirmations", confirmationCount)
	}
}

func newHTTPTestBridgeClient(t *testing.T, handler http.Handler) *bridgeclient.Client {
	t.Helper()
	socketDir, err := os.MkdirTemp("", "toolhub-http-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "bridge.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
		_ = listener.Close()
	})
	client, err := bridgeclient.New(socketPath, []byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newHTTPIntegrationStore(t *testing.T) *store.Store {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	adminConfig, err := pgx.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	databaseName := "toolhub_http_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	databaseURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	databaseURL.Path = "/" + databaseName
	cipher, err := security.NewCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, databaseURL.String(), cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop HTTP integration database: %v", err)
		}
		_ = admin.Close(context.Background())
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}
