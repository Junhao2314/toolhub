package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/skills"
	"github.com/go-chi/chi/v5"
)

const authWindow = 30 * time.Second

type Adapter interface {
	Health(context.Context) error
	RefreshNodes(context.Context) (bridgeprotocol.RefreshNodesResponse, error)
	Scan(context.Context, bridgeprotocol.ScanRequest) (bridgeprotocol.ScanResponse, error)
	ExportLocalSkill(context.Context, bridgeprotocol.LocalSkillExportRequest) (bridgeprotocol.LocalSkillExportResponse, error)
	PreviewLocalMCP(context.Context, bridgeprotocol.LocalMCPPreviewRequest) (bridgeprotocol.LocalMCPPreviewResponse, error)
	CaptureLocalMCP(context.Context, bridgeprotocol.LocalMCPCaptureRequest) (bridgeprotocol.LocalMCPCaptureResponse, error)
	Preflight(context.Context, bridgeprotocol.PreflightRequest) (bridgeprotocol.PreflightResponse, error)
	Commit(context.Context, bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error)
	Reconcile(context.Context, bridgeprotocol.ReconcileRequest) (bridgeprotocol.TargetResult, error)
	Restore(context.Context, bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error)
	RemoveBackup(context.Context, bridgeprotocol.Backup) error
	Relay(context.Context, string, bridgeprotocol.RelayActionRequest) (bridgeprotocol.RelayStatus, error)
}

type Server struct {
	key     []byte
	journal *Journal
	adapter Adapter
	logger  *slog.Logger
	now     func() time.Time
}

func NewServer(key []byte, journal *Journal, adapter Adapter, logger *slog.Logger) (*Server, error) {
	if len(key) != 32 {
		return nil, errors.New("Bridge HMAC key must be 32 bytes")
	}
	if journal == nil || adapter == nil {
		return nil, errors.New("Bridge journal and adapter are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{key: append([]byte(nil), key...), journal: journal, adapter: adapter, logger: logger, now: time.Now}
	if recoverer, ok := adapter.(interface {
		RecoverOperations(context.Context, time.Time) error
	}); ok {
		if err := recoverer.RecoverOperations(context.Background(), server.now()); err != nil {
			return nil, err
		}
	} else if err := journal.RecoverOperations(server.now()); err != nil {
		return nil, err
	}
	if err := journal.RecoverPendingIdempotency(); err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Router() http.Handler {
	router := chi.NewRouter()
	router.Use(s.authenticate)
	router.Get("/v1/health", s.health)
	router.Post("/v1/nodes/refresh", s.mutation(s.refreshNodes))
	router.Post("/v1/targets/scan", s.mutation(s.scan))
	// These are authenticated, typed, ephemeral reads. Archive and plaintext
	// capture responses deliberately bypass the idempotency journal.
	router.Post("/v1/local/skills/export", s.ephemeral(s.exportLocalSkill))
	router.Post("/v1/local/mcp/preview", s.ephemeral(s.previewLocalMCP))
	router.Post("/v1/local/mcp/capture", s.ephemeral(s.captureLocalMCP))
	router.Post("/v1/targets/preflight", s.mutation(s.preflight))
	router.Post("/v1/targets/apply", s.mutation(s.apply))
	router.Post("/v1/targets/edit", s.mutation(s.edit))
	router.Post("/v1/targets/restore", s.mutation(s.restore))
	router.Post("/v1/targets/reconcile", s.mutation(s.reconcile))
	router.Get("/v1/backups", s.backups)
	router.Post("/v1/backups/gc", s.mutation(s.gcBackups))
	for _, action := range []string{"status", "start", "stop", "restart", "health"} {
		action := action
		router.Post("/v1/relay/"+action, s.mutation(func(ctx context.Context, body []byte) (int, any, error) { return s.relay(ctx, action, body) }))
	}
	router.Get("/v1/operations/{operationID}", s.operation)
	router.Post("/v1/operations/{operationID}/cancel", s.mutation(s.cancelOperation))
	return router
}

type bodyKey struct{}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, bridgeprotocol.MaxRequestBytes+1))
		if err != nil || len(body) > bridgeprotocol.MaxRequestBytes {
			writeBridgeError(w, http.StatusRequestEntityTooLarge, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "Bridge request body is too large"})
			return
		}
		if err := bridgeprotocol.VerifyRequest(r, s.key, body, s.now(), authWindow); err != nil {
			writeBridgeError(w, http.StatusUnauthorized, asAPIError(err))
			return
		}
		if err := s.journal.AcceptNonce(r.Header.Get(bridgeprotocol.HeaderNonce), s.now(), authWindow); err != nil {
			writeBridgeError(w, http.StatusConflict, asAPIError(err))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), bodyKey{}, body)))
	})
}

type mutationHandler func(context.Context, []byte) (int, any, error)

func (s *Server) ephemeral(handler mutationHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := r.Context().Value(bodyKey{}).([]byte)
		status, result, err := handler(r.Context(), body)
		if err != nil {
			writeBridgeError(w, statusForBridgeError(asAPIError(err)), asAPIError(err))
			return
		}
		writeBridgeJSON(w, status, result)
	}
}

func (s *Server) mutation(handler mutationHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get(bridgeprotocol.HeaderIdempotencyKey))
		if len(key) < 8 || len(key) > 200 {
			writeBridgeError(w, http.StatusBadRequest, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "Mutation requires an Idempotency-Key of 8-200 characters"})
			return
		}
		body, _ := r.Context().Value(bodyKey{}).([]byte)
		hash := requestHash(r.Method, r.URL.RequestURI(), body)
		record, exists, err := s.journal.IdempotencyBegin(key, hash, operationIDFromBody(body))
		if err != nil {
			writeBridgeError(w, http.StatusConflict, asAPIError(err))
			return
		}
		if exists && record.Status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(record.Status)
			_, _ = w.Write(record.Response)
			return
		}
		if exists {
			status, recovered, ok := s.recoveredIdempotencyResponse(record.OperationID)
			if !ok {
				writeBridgeError(w, http.StatusConflict, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrIdempotencyConflict, Message: "Idempotent operation is still running"})
				return
			}
			if err := s.journal.IdempotencyPut(key, hash, status, recovered); err != nil {
				writeBridgeError(w, http.StatusInternalServerError, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "Bridge could not persist the recovered idempotent result"})
				return
			}
			writeRawJSON(w, status, recovered)
			return
		}
		status, result, err := handler(r.Context(), body)
		if err != nil {
			apiErr := asAPIError(err)
			status = statusForBridgeError(apiErr)
			result = map[string]any{"error": apiErr}
		}
		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			writeBridgeError(w, http.StatusInternalServerError, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "Bridge response encoding failed"})
			return
		}
		if err := s.journal.IdempotencyPut(key, hash, status, encoded); err != nil {
			s.logger.Error("persist Bridge idempotency response", "error", err)
			writeBridgeError(w, http.StatusInternalServerError, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "Bridge could not persist the idempotent result"})
			return
		}
		writeRawJSON(w, status, encoded)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.adapter.Health(r.Context()); err != nil {
		writeBridgeError(w, http.StatusServiceUnavailable, asAPIError(err))
		return
	}
	writeBridgeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": s.now().UTC()})
}

func (s *Server) refreshNodes(ctx context.Context, _ []byte) (int, any, error) {
	result, err := s.adapter.RefreshNodes(ctx)
	return http.StatusOK, result, err
}

func (s *Server) scan(ctx context.Context, body []byte) (int, any, error) {
	var input bridgeprotocol.ScanRequest
	if err := decodeStrict(body, &input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	if err := input.Target.Validate(false); err != nil {
		return 0, nil, invalidRequest(err)
	}
	result, err := s.adapter.Scan(ctx, input)
	return http.StatusOK, result, err
}

func (s *Server) exportLocalSkill(ctx context.Context, body []byte) (int, any, error) {
	var input bridgeprotocol.LocalSkillExportRequest
	if err := decodeStrict(body, &input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	if err := input.Target.Validate(false); err != nil {
		return 0, nil, invalidRequest(err)
	}
	result, err := s.adapter.ExportLocalSkill(ctx, input)
	return http.StatusOK, result, err
}

func (s *Server) previewLocalMCP(ctx context.Context, body []byte) (int, any, error) {
	var input bridgeprotocol.LocalMCPPreviewRequest
	if err := decodeStrict(body, &input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	if err := input.Target.Validate(false); err != nil {
		return 0, nil, invalidRequest(err)
	}
	result, err := s.adapter.PreviewLocalMCP(ctx, input)
	return http.StatusOK, result, err
}

func (s *Server) captureLocalMCP(ctx context.Context, body []byte) (int, any, error) {
	var input bridgeprotocol.LocalMCPCaptureRequest
	if err := decodeStrict(body, &input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	if err := input.Target.Validate(false); err != nil {
		return 0, nil, invalidRequest(err)
	}
	result, err := s.adapter.CaptureLocalMCP(ctx, input)
	return http.StatusOK, result, err
}

func (s *Server) preflight(ctx context.Context, body []byte) (int, any, error) {
	var input bridgeprotocol.PreflightRequest
	if err := decodeStrict(body, &input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	if err := input.Manifest.Validate(true); err != nil || input.Target.ID != input.Manifest.Target.ID {
		if err == nil {
			err = errors.New("target does not match manifest")
		}
		return 0, nil, invalidRequest(err)
	}
	result, err := s.adapter.Preflight(ctx, input)
	return http.StatusOK, result, err
}

func (s *Server) apply(ctx context.Context, body []byte) (int, any, error) {
	return s.commit(ctx, "apply", body)
}
func (s *Server) edit(ctx context.Context, body []byte) (int, any, error) {
	return s.commit(ctx, "edit", body)
}

func (s *Server) commit(ctx context.Context, kind string, body []byte) (int, any, error) {
	var input bridgeprotocol.CommitRequest
	if err := decodeStrict(body, &input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	if input.OperationKind != kind {
		return 0, nil, invalidRequest(errors.New("commit operationKind does not match the typed route"))
	}
	input.OperationKind = kind
	if err := validateCommit(input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	s.persistRunningOperation(input.OperationID, kind, input.Target.ID)
	result, err := s.adapter.Commit(ctx, input)
	if err != nil {
		s.persistSafeOperation(input.OperationID, kind, input.Target.ID, bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationFailed, Error: asAPIError(err)})
		return 0, nil, err
	}
	result = journalSafeTargetResult(result)
	s.persistSafeOperation(input.OperationID, kind, input.Target.ID, result)
	return http.StatusOK, result, nil
}

func (s *Server) restore(ctx context.Context, body []byte) (int, any, error) {
	var input bridgeprotocol.CommitRequest
	if err := decodeStrict(body, &input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	if input.OperationKind != "restore" {
		return 0, nil, invalidRequest(errors.New("restore operationKind does not match the typed route"))
	}
	if input.BackupID == "" || input.OperationID == "" || !bridgeprotocol.IsSHA256(input.ExpectedRevision) {
		return 0, nil, invalidRequest(errors.New("restore requires backupId, operationId, and expectedRevision"))
	}
	if err := input.Target.Validate(true); err != nil {
		return 0, nil, invalidRequest(err)
	}
	s.persistRunningOperation(input.OperationID, "restore", input.Target.ID)
	result, err := s.adapter.Restore(ctx, input)
	if err != nil {
		s.persistSafeOperation(input.OperationID, "restore", input.Target.ID, bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationFailed, Error: asAPIError(err)})
		return 0, nil, err
	}
	result = journalSafeTargetResult(result)
	s.persistSafeOperation(input.OperationID, "restore", input.Target.ID, result)
	return http.StatusOK, result, nil
}

func (s *Server) reconcile(ctx context.Context, body []byte) (int, any, error) {
	var input bridgeprotocol.ReconcileRequest
	if err := decodeStrict(body, &input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	if input.OperationID == "" || input.Target.ID != input.Manifest.Target.ID {
		return 0, nil, invalidRequest(errors.New("reconcile target and operation are required"))
	}
	if err := input.Manifest.Validate(true); err != nil {
		return 0, nil, invalidRequest(err)
	}
	s.persistRunningOperation(input.OperationID, "reconcile", input.Target.ID)
	result, err := s.adapter.Reconcile(ctx, input)
	if err != nil {
		s.persistSafeOperation(input.OperationID, "reconcile", input.Target.ID, bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationFailed, Error: asAPIError(err)})
		return 0, nil, err
	}
	result = journalSafeTargetResult(result)
	s.persistSafeOperation(input.OperationID, "reconcile", input.Target.ID, result)
	return http.StatusOK, result, nil
}

func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	items, err := s.journal.Backups(r.URL.Query().Get("targetId"))
	if err != nil {
		writeBridgeError(w, http.StatusInternalServerError, asAPIError(err))
		return
	}
	writeBridgeJSON(w, http.StatusOK, bridgeprotocol.BackupListResponse{Items: items})
}

func (s *Server) gcBackups(ctx context.Context, body []byte) (int, any, error) {
	var input bridgeprotocol.BackupGCRequest
	if err := decodeStrict(body, &input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	removed, err := s.journal.GCBackups(s.now(), input.MaxAgeDays, input.MaxPerTarget, func(backup bridgeprotocol.Backup) error { return s.adapter.RemoveBackup(ctx, backup) })
	return http.StatusOK, bridgeprotocol.BackupGCResponse{Removed: len(removed), RemovedBackupIDs: removed}, err
}

func (s *Server) relay(ctx context.Context, action string, body []byte) (int, any, error) {
	var input bridgeprotocol.RelayActionRequest
	if err := decodeStrict(body, &input); err != nil {
		return 0, nil, invalidRequest(err)
	}
	if err := input.Target.Validate(true); err != nil {
		return 0, nil, invalidRequest(err)
	}
	if input.Target.NodeKind != bridgeprotocol.NodeKindLocal || input.Target.Runtime != bridgeprotocol.RuntimeSharedRelay {
		return 0, nil, invalidRequest(errors.New("relay controls require local/shared-relay target"))
	}
	if input.Port < 1 || input.Port > 65535 {
		return 0, nil, invalidRequest(errors.New("relay controls require a valid fixed port"))
	}
	if input.Manifest != nil {
		if input.Manifest.Target != input.Target || input.Manifest.RelayPort != input.Port {
			return 0, nil, invalidRequest(errors.New("relay manifest does not match the fixed target and port"))
		}
		if err := input.Manifest.Validate(true); err != nil {
			return 0, nil, invalidRequest(err)
		}
	}
	result, err := s.adapter.Relay(ctx, action, input)
	return http.StatusOK, result, err
}

func (s *Server) operation(w http.ResponseWriter, r *http.Request) {
	operation, err := s.journal.Operation(chi.URLParam(r, "operationID"))
	if err != nil {
		writeBridgeError(w, http.StatusNotFound, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "Bridge operation was not found"})
		return
	}
	writeBridgeJSON(w, http.StatusOK, operation)
}

func (s *Server) cancelOperation(_ context.Context, _ []byte) (int, any, error) {
	return http.StatusConflict, nil, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrUnsupportedOperation, Message: "Only queued target dispatch can be cancelled by the control plane"}
}

func validateCommit(input bridgeprotocol.CommitRequest) error {
	if input.OperationID == "" || !bridgeprotocol.IsSHA256(input.ExpectedRevision) {
		return errors.New("operationId and expectedRevision are required")
	}
	if input.Target.ID != input.Manifest.Target.ID {
		return errors.New("commit target does not match manifest")
	}
	if err := input.Manifest.Validate(true); err != nil {
		return err
	}
	for _, artifact := range input.Artifacts {
		if artifact.VersionID == "" || !bridgeprotocol.IsSHA256(artifact.SHA256) || len(artifact.Archive) == 0 {
			return errors.New("commit contains an invalid artifact")
		}
		pkg, err := skills.ScanZIP(artifact.Archive, skills.DefaultLimits)
		if err != nil || pkg.SHA256 != artifact.SHA256 {
			return errors.New("commit contains a mismatched or unsafe artifact")
		}
	}
	return nil
}

func (s *Server) persistSafeOperation(operationID, kind, targetID string, result bridgeprotocol.TargetResult) {
	result = journalSafeTargetResult(result)
	status := result.Status
	if status == "" {
		status = bridgeprotocol.OperationFailed
	}
	now := s.now().UTC()
	createdAt, saltJID := now, ""
	if existing, err := s.journal.Operation(operationID); err == nil {
		createdAt = existing.CreatedAt
		if len(existing.Targets) == 1 {
			saltJID = existing.Targets[0].SaltJID
		}
	}
	targetError := result.Error
	if status == bridgeprotocol.OperationSucceeded {
		targetError = nil
	}
	operation := bridgeprotocol.Operation{ID: operationID, Kind: kind, Status: status, Targets: []bridgeprotocol.OperationTarget{{TargetID: targetID, Status: status, SaltJID: saltJID, Result: &result, Error: targetError}}, CreatedAt: createdAt, UpdatedAt: now}
	if err := s.journal.PutOperation(operation); err != nil {
		s.logger.Error("persist safe Bridge operation", "operationId", operationID, "error", err)
	}
}

func operationIDFromBody(body []byte) string {
	var identity struct {
		OperationID string `json:"operationId"`
	}
	if json.Unmarshal(body, &identity) != nil {
		return ""
	}
	return identity.OperationID
}

func (s *Server) recoveredIdempotencyResponse(operationID string) (int, []byte, bool) {
	if operationID == "" {
		return 0, nil, false
	}
	operation, err := s.journal.Operation(operationID)
	if err != nil || !bridgeprotocol.IsTerminalOperationStatus(operation.Status) || len(operation.Targets) != 1 || operation.Targets[0].Result == nil {
		return 0, nil, false
	}
	result := operation.Targets[0].Result
	if result.Error == nil {
		result.Error = operation.Targets[0].Error
	}
	status := http.StatusOK
	var response any = result
	if result.Status == bridgeprotocol.OperationFailed && result.Error != nil {
		status = statusForBridgeError(result.Error)
		response = map[string]any{"error": result.Error}
	}
	encoded, err := json.Marshal(response)
	if err != nil || containsSensitiveJSON(encoded) {
		return 0, nil, false
	}
	return status, encoded, true
}

func (s *Server) persistRunningOperation(operationID, kind, targetID string) {
	now := s.now().UTC()
	operation := bridgeprotocol.Operation{ID: operationID, Kind: kind, Status: bridgeprotocol.OperationRunning, Targets: []bridgeprotocol.OperationTarget{{TargetID: targetID, Status: bridgeprotocol.OperationRunning}}, CreatedAt: now, UpdatedAt: now}
	if err := s.journal.PutOperation(operation); err != nil {
		s.logger.Error("persist running Bridge operation", "operationId", operationID, "error", err)
	}
}

func decodeStrict(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func invalidRequest(err error) *bridgeprotocol.APIError {
	return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: err.Error()}
}
func asAPIError(err error) *bridgeprotocol.APIError {
	var target *bridgeprotocol.APIError
	if errors.As(err, &target) {
		return target
	}
	return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "Bridge operation failed"}
}
func statusForBridgeError(err *bridgeprotocol.APIError) int {
	switch err.Code {
	case bridgeprotocol.ErrRevisionConflict, bridgeprotocol.ErrReplay, bridgeprotocol.ErrIdempotencyConflict, bridgeprotocol.ErrRelayPortConflict:
		return http.StatusConflict
	case bridgeprotocol.ErrTargetUnavailable, bridgeprotocol.ErrRelayUnhealthy:
		return http.StatusServiceUnavailable
	case bridgeprotocol.ErrUnsupportedOperation:
		return http.StatusNotImplemented
	default:
		return http.StatusBadRequest
	}
}
func writeBridgeError(w http.ResponseWriter, status int, err *bridgeprotocol.APIError) {
	writeBridgeJSON(w, status, map[string]any{"error": err})
}
func writeBridgeJSON(w http.ResponseWriter, status int, value any) {
	encoded, _ := json.Marshal(value)
	writeRawJSON(w, status, encoded)
}
func writeRawJSON(w http.ResponseWriter, status int, encoded []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}
