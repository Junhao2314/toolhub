package httpapi

import (
	"crypto/subtle"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/store"
)

const governanceBrowserBodyLimit = 64 << 10

func (a *API) relayConfiguration(w http.ResponseWriter, r *http.Request) {
	result, err := a.store.RelayConfigurationProjection(r.Context())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) saveRelayConfiguration(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision       int64             `json:"revision"`
		MCPServerIDs   []string          `json:"mcpServerIds"`
		MCPRevisionIDs map[string]string `json:"mcpRevisionIds"`
		Metadata       map[string]any    `json:"metadata"`
	}
	if err := decodeJSON(w, r, &input, governanceBrowserBodyLimit); err != nil || input.Revision < 0 || !validRelayPins(input.MCPServerIDs, input.MCPRevisionIDs) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Relay Configuration request is invalid")
		return
	}
	result, err := a.store.SaveRelayConfiguration(r.Context(), store.RelayConfigurationInput{
		Revision: input.Revision, MCPServerIDs: input.MCPServerIDs, MCPRevisionIDs: input.MCPRevisionIDs, Metadata: input.Metadata,
	})
	if errors.Is(err, store.ErrConflict) {
		a.handleStoreError(w, r, err)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Relay Configuration request is invalid")
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "relay_configuration_save", ResourceType: "relay_configuration", ResourceID: result.ID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"revision": result.Revision, "canonicalHash": result.CanonicalHash}})
	writeJSON(w, http.StatusOK, result)
}

func validRelayPins(serverIDs []string, revisions map[string]string) bool {
	if len(serverIDs) > 500 || len(revisions) != len(serverIDs) {
		return false
	}
	seen := make(map[string]struct{}, len(serverIDs))
	for _, serverID := range serverIDs {
		if uuid.Validate(serverID) != nil || uuid.Validate(revisions[serverID]) != nil {
			return false
		}
		if _, exists := seen[serverID]; exists {
			return false
		}
		seen[serverID] = struct{}{}
	}
	for serverID := range revisions {
		if _, exists := seen[serverID]; !exists {
			return false
		}
	}
	return true
}

func (a *API) globalPolicy(w http.ResponseWriter, r *http.Request) {
	result, err := a.store.GlobalPolicyProjection(r.Context())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) saveGlobalPolicy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision             int64             `json:"revision"`
		CatalogVersion       int               `json:"catalogVersion"`
		ExplicitOverrides    map[string]string `json:"explicitOverrides"`
		UnclassifiedMutating string            `json:"unclassifiedMutating"`
		ReviewedReadOnly     string            `json:"reviewedReadOnly"`
	}
	if err := decodeJSON(w, r, &input, governanceBrowserBodyLimit); err != nil || input.Revision < 0 || invalidOverrideKey(input.ExplicitOverrides) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Global Policy request is invalid")
		return
	}
	result, err := a.store.SaveGlobalPolicy(r.Context(), store.GlobalPolicyInput{
		Revision: input.Revision, CatalogVersion: input.CatalogVersion, ExplicitOverrides: input.ExplicitOverrides,
		UnclassifiedMutating: input.UnclassifiedMutating, ReviewedReadOnly: input.ReviewedReadOnly,
	})
	if errors.Is(err, store.ErrConflict) {
		a.handleStoreError(w, r, err)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Global Policy request is invalid")
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "global_policy_save", ResourceType: "global_policy", ResourceID: result.ID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"revision": result.Revision, "canonicalHash": result.CanonicalHash}})
	writeJSON(w, http.StatusOK, result)
}

func invalidOverrideKey(overrides map[string]string) bool {
	if len(overrides) > 20000 {
		return true
	}
	for key := range overrides {
		if strings.TrimSpace(key) == "" || len(key) > 256 {
			return true
		}
	}
	return false
}

type relayApplyInput struct {
	RevisionID     string   `json:"revisionId"`
	ProfileIDs     []string `json:"profileIds"`
	TargetRevision string   `json:"targetRevision,omitempty"`
	RoutingHash    string   `json:"routingHash,omitempty"`
}

func (a *API) prepareRelayProfileUpdates(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RevisionID string `json:"revisionId"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil || uuid.Validate(input.RevisionID) != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Relay Configuration revision is invalid")
		return
	}
	ids, err := a.store.PrepareAffectedProfileUpdates(r.Context(), input.RevisionID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "relay_profile_updates_prepare", ResourceType: "relay_configuration", ResourceID: input.RevisionID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"affectedProfileIds": ids}})
	writeJSON(w, http.StatusOK, map[string]any{"items": ids})
}

func (a *API) preflightRelayConfiguration(w http.ResponseWriter, r *http.Request) {
	var input relayApplyInput
	if err := decodeJSON(w, r, &input, 16<<10); err != nil || uuid.Validate(input.RevisionID) != nil || !validUUIDList(input.ProfileIDs, 100) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Relay Configuration preflight request is invalid")
		return
	}
	prepared, err := a.store.PrepareRelayConfigurationApply(r.Context(), input.RevisionID, input.ProfileIDs)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := a.bridge.Preflight(r.Context(), key, bridgeprotocol.PreflightRequest{Target: bridgeprotocol.Target{
		ID: prepared.Target.ID, NodeID: prepared.Target.NodeID, NodeKind: prepared.Target.NodeKind, SaltMinionID: prepared.Target.SaltMinionID, Runtime: prepared.Target.Runtime, ManagedUsername: prepared.Target.ManagedUsername,
	}, Manifest: prepared.Manifest})
	if err != nil {
		writeError(w, r, http.StatusConflict, "preflight_failed", "Relay Configuration preflight failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisionId": input.RevisionID, "routingHash": prepared.RoutingHash, "result": result})
}

func (a *API) applyRelayConfiguration(w http.ResponseWriter, r *http.Request) {
	var input relayApplyInput
	if err := decodeJSON(w, r, &input, 16<<10); err != nil || uuid.Validate(input.RevisionID) != nil || !validUUIDList(input.ProfileIDs, 100) || !bridgeprotocol.IsSHA256(input.TargetRevision) || !bridgeprotocol.IsSHA256(input.RoutingHash) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Relay Configuration Apply request is invalid")
		return
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	operation, err := a.store.CreateRelayConfigurationApplyOperation(r.Context(), input.RevisionID, input.ProfileIDs, input.TargetRevision, input.RoutingHash, key)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "relay_configuration_apply", ResourceType: "relay_configuration", ResourceID: input.RevisionID, Outcome: "queued", IPAddress: clientIP(r), Metadata: map[string]any{"operationId": operation.ID, "routingHash": input.RoutingHash, "affectedProfileIds": input.ProfileIDs}})
	writeJSON(w, http.StatusAccepted, operation)
}

func (a *API) applyGlobalPolicy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RevisionID     string `json:"revisionId"`
		TargetRevision string `json:"targetRevision"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil || uuid.Validate(input.RevisionID) != nil || !bridgeprotocol.IsSHA256(input.TargetRevision) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Global Policy Apply request is invalid")
		return
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	operation, err := a.store.CreateGlobalPolicyApplyOperation(r.Context(), input.RevisionID, input.TargetRevision, key)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "global_policy_apply", ResourceType: "global_policy", ResourceID: input.RevisionID, Outcome: "queued", IPAddress: clientIP(r), Metadata: map[string]any{"operationId": operation.ID}})
	writeJSON(w, http.StatusAccepted, operation)
}

func validUUIDList(ids []string, max int) bool {
	if len(ids) > max {
		return false
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if uuid.Validate(id) != nil {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func (a *API) relayContracts(w http.ResponseWriter, r *http.Request) {
	result, err := a.store.ContractGovernanceProjection(r.Context())
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) observeRelayContracts(w http.ResponseWriter, r *http.Request) {
	if err := decodeEmptyGovernanceBody(w, r); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Contract observation request is invalid")
		return
	}
	key, err := requestIdempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	operation, err := a.store.CreateOperation(r.Context(), store.CreateOperationInput{Kind: "contract_observe", IdempotencyKey: key, Request: map[string]any{"requested": true}, Metadata: map[string]any{"requested": true}})
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "relay_contract_observe", ResourceType: "relay_contract", Outcome: "queued", IPAddress: clientIP(r), Metadata: map[string]any{"operationId": operation.ID}})
	writeJSON(w, http.StatusAccepted, operation)
}

func (a *API) acceptRelayContract(w http.ResponseWriter, r *http.Request) {
	revisionID := chi.URLParam(r, "revisionID")
	if uuid.Validate(revisionID) != nil || decodeEmptyGovernanceBody(w, r) != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Contract acceptance request is invalid")
		return
	}
	revision, err := a.store.ContractRevisionView(r.Context(), revisionID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if err := a.store.AcceptContract(r.Context(), revision.Revision.ServerID, revisionID); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	_ = a.store.Audit(r.Context(), domain.AuditEvent{Action: "relay_contract_accept", ResourceType: "relay_contract", ResourceID: revisionID, Outcome: "success", IPAddress: clientIP(r), Metadata: map[string]any{"serverId": revision.Revision.ServerID, "canonicalHash": revision.Revision.CanonicalHash}})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) confirmRelayRename(w http.ResponseWriter, r *http.Request) {
	proposalID := chi.URLParam(r, "proposalID")
	if uuid.Validate(proposalID) != nil || decodeEmptyGovernanceBody(w, r) != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Tool rename confirmation request is invalid")
		return
	}
	if err := a.store.ConfirmToolRename(r.Context(), proposalID); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeEmptyGovernanceBody(w http.ResponseWriter, r *http.Request) error {
	var input struct{}
	return decodeJSON(w, r, &input, 1024)
}

func (a *API) relayConfirmations(w http.ResponseWriter, r *http.Request) {
	result, err := a.bridge.ListRelayConfirmations(r.Context())
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "relay_unavailable", "Relay confirmations are unavailable")
		return
	}
	if !validConfirmationSummaries(result.Items) {
		writeError(w, r, http.StatusBadGateway, "relay_response_invalid", "Relay confirmation response is invalid")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) approveRelayConfirmation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProfileName string `json:"profileName"`
		BindingHash string `json:"bindingHash"`
	}
	challengeID := chi.URLParam(r, "challengeID")
	if err := decodeJSON(w, r, &input, 8<<10); err != nil || !bridgeprotocol.IsSHA256(challengeID) || !bridgeprotocol.IsSHA256(input.BindingHash) || strings.TrimSpace(input.ProfileName) != input.ProfileName || input.ProfileName == "" || len(input.ProfileName) > 120 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Confirmation approval request is invalid")
		return
	}
	summary, ok, err := a.confirmationSummary(r, challengeID)
	if err != nil {
		writeConfirmationLookupError(w, r, err)
		return
	}
	if !ok || !constantTimeEqual(summary.BindingHash, input.BindingHash) || !constantTimeEqual(summary.ProfileName, input.ProfileName) {
		writeError(w, r, http.StatusConflict, "confirmation_binding_mismatch", "Confirmation binding does not match")
		return
	}
	profile, err := a.store.Profile(r.Context(), summary.ProfileID)
	if err != nil || profile.CurrentRevisionID != summary.ProfileRevisionID || !constantTimeEqual(profile.Name, summary.ProfileName) {
		writeError(w, r, http.StatusConflict, "confirmation_binding_mismatch", "Confirmation Profile is stale")
		return
	}
	a.decideRelayConfirmation(w, r, true, summary)
}

func (a *API) rejectRelayConfirmation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		BindingHash string `json:"bindingHash"`
	}
	challengeID := chi.URLParam(r, "challengeID")
	if err := decodeJSON(w, r, &input, 4<<10); err != nil || !bridgeprotocol.IsSHA256(challengeID) || !bridgeprotocol.IsSHA256(input.BindingHash) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Confirmation rejection request is invalid")
		return
	}
	summary, ok, err := a.confirmationSummary(r, challengeID)
	if err != nil {
		writeConfirmationLookupError(w, r, err)
		return
	}
	if !ok || !constantTimeEqual(summary.BindingHash, input.BindingHash) {
		writeError(w, r, http.StatusConflict, "confirmation_binding_mismatch", "Confirmation binding does not match")
		return
	}
	a.decideRelayConfirmation(w, r, false, summary)
}

var errInvalidRelayConfirmationResponse = errors.New("invalid Relay confirmation response")

func (a *API) confirmationSummary(r *http.Request, challengeID string) (bridgeprotocol.ConfirmationSummary, bool, error) {
	result, err := a.bridge.ListRelayConfirmations(r.Context())
	if err != nil {
		return bridgeprotocol.ConfirmationSummary{}, false, err
	}
	if !validConfirmationSummaries(result.Items) {
		return bridgeprotocol.ConfirmationSummary{}, false, errInvalidRelayConfirmationResponse
	}
	for _, summary := range result.Items {
		if constantTimeEqual(summary.ChallengeID, challengeID) {
			return summary, true, nil
		}
	}
	return bridgeprotocol.ConfirmationSummary{}, false, nil
}

func writeConfirmationLookupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errInvalidRelayConfirmationResponse) {
		writeError(w, r, http.StatusBadGateway, "relay_response_invalid", "Relay confirmation response is invalid")
		return
	}
	writeError(w, r, http.StatusServiceUnavailable, "relay_unavailable", "Relay confirmations are unavailable")
}

func (a *API) decideRelayConfirmation(w http.ResponseWriter, r *http.Request, approve bool, summary bridgeprotocol.ConfirmationSummary) {
	action, outcome := "reject", "rejected"
	if approve {
		action, outcome = "approve", "approved"
	}
	result, err := a.bridge.DecideRelayConfirmation(r.Context(), approve, "confirmation-"+action+"-"+summary.ChallengeID, bridgeprotocol.ConfirmationDecisionRequest{ChallengeID: summary.ChallengeID, BindingHash: summary.BindingHash})
	if err != nil {
		if confirmationDecisionOutcomeUnknown(err) {
			_ = a.auditConfirmation(r, summary, "unknown")
			writeError(w, r, http.StatusBadGateway, "confirmation_outcome_unknown", "Relay confirmation outcome is unknown")
			return
		}
		_ = a.auditConfirmation(r, summary, action+"_failed")
		writeError(w, r, http.StatusConflict, "confirmation_decision_failed", "Relay confirmation decision failed")
		return
	}
	if !validConfirmationDecisionResponse(result, summary, approve) {
		_ = a.auditConfirmation(r, summary, "unknown")
		writeError(w, r, http.StatusBadGateway, "confirmation_outcome_unknown", "Relay confirmation outcome is unknown")
		return
	}
	_ = a.auditConfirmation(r, summary, outcome)
	writeJSON(w, http.StatusOK, result)
}

func confirmationDecisionOutcomeUnknown(err error) bool {
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	return apiErr.Code == bridgeprotocol.ErrRelayUnhealthy || apiErr.Code == bridgeprotocol.ErrTargetUnavailable
}

func validConfirmationDecisionResponse(result bridgeprotocol.ConfirmationDecisionResponse, summary bridgeprotocol.ConfirmationSummary, approve bool) bool {
	if !constantTimeEqual(result.ChallengeID, summary.ChallengeID) || !constantTimeEqual(result.BindingHash, summary.BindingHash) {
		return false
	}
	if result.GrantExpiresAt == nil {
		return true
	}
	return approve && !math.IsNaN(*result.GrantExpiresAt) && !math.IsInf(*result.GrantExpiresAt, 0) && *result.GrantExpiresAt >= 0
}

func (a *API) auditConfirmation(r *http.Request, summary bridgeprotocol.ConfirmationSummary, outcome string) error {
	auditOutcome := "success"
	if outcome == "unknown" || strings.HasSuffix(outcome, "_failed") {
		auditOutcome = "failure"
	}
	return a.store.Audit(r.Context(), domain.AuditEvent{Action: "relay_confirmation_decision", ResourceType: "relay_confirmation", Outcome: auditOutcome, IPAddress: clientIP(r), Metadata: map[string]any{
		"challengeId": summary.ChallengeID, "bindingHash": summary.BindingHash, "argumentHash": summary.ArgumentHash,
		"profileId": summary.ProfileID, "profileRevisionId": summary.ProfileRevisionID, "serverId": summary.ServerID, "toolId": summary.ToolID,
		"reasonCodes": summary.ReasonCodes, "outcome": outcome,
	}})
}

func validConfirmationSummaries(items []bridgeprotocol.ConfirmationSummary) bool {
	if len(items) > 256 {
		return false
	}
	for _, item := range items {
		if !bridgeprotocol.IsSHA256(item.ChallengeID) || !bridgeprotocol.IsSHA256(item.BindingHash) || !bridgeprotocol.IsSHA256(item.ArgumentHash) ||
			math.IsNaN(item.CreatedAt) || math.IsInf(item.CreatedAt, 0) || item.CreatedAt < 0 || math.IsNaN(item.ExpiresAt) || math.IsInf(item.ExpiresAt, 0) || item.ExpiresAt < item.CreatedAt ||
			uuid.Validate(item.ProfileID) != nil || uuid.Validate(item.ProfileRevisionID) != nil || uuid.Validate(item.ServerID) != nil || uuid.Validate(item.ToolID) != nil ||
			uuid.Validate(item.MCPConfigRevisionID) != nil || uuid.Validate(item.ContractRevisionID) != nil || uuid.Validate(item.GlobalPolicyRevisionID) != nil ||
			!validSafeName(item.ProfileName, 120) || !validSafeName(item.ServerName, 128) || !validSafeName(item.ToolName, 128) || !validSafeName(item.RuntimeName, 256) ||
			(item.ClientKind != bridgeprotocol.RuntimeClaude && item.ClientKind != bridgeprotocol.RuntimeCodex) || item.Decision != "confirm" || len(item.ReasonCodes) > 32 || len(item.ArgumentSummary) > 512 {
			return false
		}
		for _, reason := range item.ReasonCodes {
			if !validSafeName(reason, 64) {
				return false
			}
		}
		for _, summary := range item.ArgumentSummary {
			if len(summary.Pointer) > 512 || !validArgumentSummary(summary) {
				return false
			}
		}
	}
	return true
}

func validSafeName(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= maximum
}

func validArgumentSummary(summary bridgeprotocol.ArgumentSummary) bool {
	switch summary.ValueType {
	case "null", "boolean", "number", "string", "array", "object":
	default:
		return false
	}
	return (summary.ArrayLength == nil || *summary.ArrayLength >= 0) && (summary.StringLength == nil || *summary.StringLength >= 0)
}

func constantTimeEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func (a *API) profileLaunch(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "profileID")
	if uuid.Validate(profileID) != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Profile ID is invalid")
		return
	}
	_, inspectionRequest, err := a.store.ProfileNativeClientInspectionRequest(r.Context(), profileID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	inspection, err := a.bridge.InspectNativeClient(r.Context(), inspectionRequest)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "native_client_inspection_failed", "Native client inspection could not be completed")
		return
	}
	if !validNativeClientInspection(inspection, inspectionRequest.ClientKind) {
		writeError(w, r, http.StatusBadGateway, "relay_response_invalid", "Native client inspection response is invalid")
		return
	}
	readiness, err := a.store.ProfileReadiness(r.Context(), profileID, inspection)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, readiness)
}

var nativeInspectionVersionPattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

var nativeInspectionErrorCodes = map[string]bool{
	"native_client_path_unsafe": true, "native_client_inspection_failed": true, "native_client_not_found": true,
	"native_client_resolution_ambiguous": true, "native_client_timeout": true, "native_client_output_invalid": true,
	"native_client_version_invalid": true, "native_client_version_unsupported": true,
}

func validNativeClientInspection(inspection bridgeprotocol.NativeClientInspectionResponse, expectedKind string) bool {
	if inspection.ClientKind != expectedKind || len(inspection.Version) > 64 || len(inspection.ErrorCode) > 64 {
		return false
	}
	validVersion, meetsFloor := nativeInspectionVersionMeetsFloor(inspection.ClientKind, inspection.Version)
	if inspection.Supported {
		return inspection.ErrorCode == "" && validVersion && meetsFloor
	}
	if !nativeInspectionErrorCodes[inspection.ErrorCode] {
		return false
	}
	if inspection.ErrorCode == "native_client_version_unsupported" {
		return validVersion && !meetsFloor
	}
	return inspection.Version == ""
}

func nativeInspectionVersionMeetsFloor(clientKind, version string) (bool, bool) {
	match := nativeInspectionVersionPattern.FindStringSubmatch(version)
	if len(match) != 6 {
		return false, false
	}
	var components [3]uint64
	for index := range components {
		if len(match[index+1]) > 1 && match[index+1][0] == '0' {
			return false, false
		}
		value, err := strconv.ParseUint(match[index+1], 10, 31)
		if err != nil {
			return false, false
		}
		components[index] = value
	}
	for _, identifier := range strings.Split(match[4], ".") {
		if len(identifier) > 1 && identifier[0] == '0' {
			if _, err := strconv.ParseUint(identifier, 10, 64); err == nil {
				return false, false
			}
		}
	}
	minimum, ok := map[string][3]uint64{bridgeprotocol.RuntimeClaude: {2, 1, 232}, bridgeprotocol.RuntimeCodex: {0, 147, 0}}[clientKind]
	if !ok {
		return false, false
	}
	for index := range components {
		if components[index] < minimum[index] {
			return true, false
		}
		if components[index] > minimum[index] {
			return true, true
		}
	}
	return true, match[4] == ""
}

func (a *API) liveRelayObservations(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !governanceQueryAllowed(query, "afterBootId", "afterSequence", "limit") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Live observation query is invalid")
		return
	}
	afterBootID := strings.TrimSpace(query.Get("afterBootId"))
	if afterBootID != "" && uuid.Validate(afterBootID) != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Live observation cursor is invalid")
		return
	}
	afterSequence, err := parseBoundedQueryInt(query.Get("afterSequence"), 0, 0, 1<<62)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Live observation cursor is invalid")
		return
	}
	limit, err := parseBoundedQueryInt(query.Get("limit"), 100, 1, 1000)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Live observation limit is invalid")
		return
	}
	var bootID *string
	if afterBootID != "" {
		bootID = &afterBootID
	}
	drainRequest := bridgeprotocol.ObservationDrainRequest{AfterBootID: bootID, AfterSequence: int64(afterSequence), Limit: limit}
	result, err := a.bridge.DrainRelayObservations(r.Context(), drainRequest)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "relay_unavailable", "Live observations are unavailable")
		return
	}
	if !validObservationDrain(result, drainRequest) {
		writeError(w, r, http.StatusBadGateway, "relay_response_invalid", "Live observation response is invalid")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) dailyRelayObservations(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !governanceQueryAllowed(query, "days", "profileId") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Daily observation query is invalid")
		return
	}
	days, err := parseBoundedQueryInt(query.Get("days"), 30, 1, 31)
	profileID := strings.TrimSpace(query.Get("profileId"))
	if err != nil || profileID != "" && uuid.Validate(profileID) != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Daily observation query is invalid")
		return
	}
	items, err := a.store.DailyToolAggregates(r.Context(), days, profileID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "days": days})
}

func governanceQueryAllowed(query map[string][]string, allowed ...string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key, values := range query {
		if _, ok := set[key]; !ok || len(values) != 1 {
			return false
		}
	}
	return true
}

func parseBoundedQueryInt(raw string, fallback, minimum, maximum int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, errors.New("query integer is out of range")
	}
	return value, nil
}

func validObservationDrain(result bridgeprotocol.ObservationDrainResponse, request bridgeprotocol.ObservationDrainRequest) bool {
	if uuid.Validate(result.BootID) != nil || result.NextSequence < 0 || len(result.Items) > request.Limit || len(result.Items) > 1000 {
		return false
	}
	previous := int64(0)
	if request.AfterBootID != nil && *request.AfterBootID == result.BootID {
		if result.NextSequence < request.AfterSequence {
			return false
		}
		previous = request.AfterSequence
	}
	for _, item := range result.Items {
		if item.BootID != result.BootID || item.Sequence <= previous || item.Sequence > result.NextSequence || uuid.Validate(item.ProfileID) != nil || uuid.Validate(item.ProfileRevisionID) != nil || uuid.Validate(item.ServerID) != nil || uuid.Validate(item.ToolID) != nil || len(item.ReasonCodes) > 32 ||
			!governanceTelemetryDecisions[item.Decision] || !governanceTelemetryOutcomes[item.Outcome] || !governanceTelemetryErrorClasses[item.ErrorClass] || !governanceTelemetryDurationBuckets[item.DurationBucket] || !validObservationTimestamp(item) {
			return false
		}
		for _, reason := range item.ReasonCodes {
			if strings.TrimSpace(reason) == "" || len(reason) > 64 {
				return false
			}
		}
		previous = item.Sequence
	}
	return len(result.Items) == 0 || previous == result.NextSequence
}

var governanceTelemetryDecisions = map[string]bool{"allow": true, "confirm": true, "deny": true}
var governanceTelemetryOutcomes = map[string]bool{
	"confirmation_required": true, "confirmed": true, "rejected": true, "expired": true, "denied": true,
	"not_executed": true, "executed": true, "failed": true, "unknown": true,
}
var governanceTelemetryErrorClasses = map[string]bool{
	"none": true, "policy": true, "confirmation": true, "rate_limited": true, "timeout": true, "transport": true, "upstream": true, "internal": true,
}
var governanceTelemetryDurationBuckets = map[string]bool{
	"lt_10ms": true, "lt_100ms": true, "lt_1s": true, "lt_10s": true, "gte_10s": true,
}

func validObservationTimestamp(observation bridgeprotocol.Observation) bool {
	if math.IsNaN(observation.ObservedAt) || math.IsInf(observation.ObservedAt, 0) || observation.ObservedAt < 0 {
		return false
	}
	bucket, err := time.Parse("2006-01-02T15:04:00Z", observation.MinuteBucket)
	return err == nil && time.Unix(int64(observation.ObservedAt), 0).UTC().Truncate(time.Minute).Equal(bucket)
}
