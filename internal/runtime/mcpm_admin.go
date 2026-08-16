package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

const (
	MCPMAdminSocket          = "/run/toolhub-mcpm/relay.sock"
	MCPMAdminMaxMessageBytes = 1 << 20
	mcpmAdminTimeout         = 15 * time.Second
)

var mcpmAdminErrorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type MCPMAdminClient struct {
	socketPath string
	timeout    time.Duration
}

type MCPMAdmin interface {
	Capability(context.Context) (bridgeprotocol.RelayCapabilityResponse, error)
	SessionCanary(context.Context, bridgeprotocol.RelaySessionCanaryRequest) (bridgeprotocol.RelaySessionCanaryResponse, error)
	ReloadRouting(context.Context) (bridgeprotocol.RelayAdminStatus, error)
	Status(context.Context) (bridgeprotocol.RelayAdminStatus, error)
	ObserveContracts(context.Context) (bridgeprotocol.ContractObservationResponse, error)
	ListConfirmations(context.Context) (bridgeprotocol.ConfirmationListResponse, error)
	DecideConfirmation(context.Context, bool, bridgeprotocol.ConfirmationDecisionRequest) (bridgeprotocol.ConfirmationDecisionResponse, error)
	DrainObservations(context.Context, bridgeprotocol.ObservationDrainRequest) (bridgeprotocol.ObservationDrainResponse, error)
}

func NewMCPMAdminClient() *MCPMAdminClient {
	return newMCPMAdminClient(MCPMAdminSocket, mcpmAdminTimeout)
}

func newMCPMAdminClient(socketPath string, timeout time.Duration) *MCPMAdminClient {
	return &MCPMAdminClient{socketPath: socketPath, timeout: timeout}
}

func (c *MCPMAdminClient) Capability(ctx context.Context) (bridgeprotocol.RelayCapabilityResponse, error) {
	var result bridgeprotocol.RelayCapabilityResponse
	err := c.call(ctx, map[string]any{"operation": "contract"}, &result)
	return result, err
}

func (c *MCPMAdminClient) SessionCanary(ctx context.Context, input bridgeprotocol.RelaySessionCanaryRequest) (bridgeprotocol.RelaySessionCanaryResponse, error) {
	request := struct {
		Operation         string          `json:"operation"`
		RoutingBundleHash string          `json:"routingBundleHash"`
		RoutingBundle     json.RawMessage `json:"routingBundle"`
	}{Operation: "session_canary", RoutingBundleHash: input.RoutingBundleHash, RoutingBundle: input.RoutingBundle}
	var result bridgeprotocol.RelaySessionCanaryResponse
	err := c.call(ctx, request, &result)
	return result, err
}

func (c *MCPMAdminClient) ReloadRouting(ctx context.Context) (bridgeprotocol.RelayAdminStatus, error) {
	var result bridgeprotocol.RelayAdminStatus
	err := c.call(ctx, map[string]any{"operation": "reload_routing"}, &result)
	return result, err
}

func (c *MCPMAdminClient) Status(ctx context.Context) (bridgeprotocol.RelayAdminStatus, error) {
	var result bridgeprotocol.RelayAdminStatus
	err := c.call(ctx, map[string]any{"operation": "status"}, &result)
	return result, err
}

func (c *MCPMAdminClient) ObserveContracts(ctx context.Context) (bridgeprotocol.ContractObservationResponse, error) {
	var result bridgeprotocol.ContractObservationResponse
	err := c.call(ctx, map[string]any{"operation": "observe_contracts"}, &result)
	return result, err
}

func (c *MCPMAdminClient) ListConfirmations(ctx context.Context) (bridgeprotocol.ConfirmationListResponse, error) {
	var result bridgeprotocol.ConfirmationListResponse
	err := c.call(ctx, map[string]any{"operation": "list_confirmations"}, &result)
	return result, err
}

func (c *MCPMAdminClient) DrainObservations(ctx context.Context, input bridgeprotocol.ObservationDrainRequest) (bridgeprotocol.ObservationDrainResponse, error) {
	if input.AfterSequence < 0 || input.Limit < 1 || input.Limit > 1000 {
		return bridgeprotocol.ObservationDrainResponse{}, errors.New("mcpm observation drain cursor or limit is invalid")
	}
	request := struct {
		Operation     string  `json:"operation"`
		AfterBootID   *string `json:"afterBootId"`
		AfterSequence int64   `json:"afterSequence"`
		Limit         int     `json:"limit"`
	}{Operation: "drain_observations", AfterBootID: input.AfterBootID, AfterSequence: input.AfterSequence, Limit: input.Limit}
	var result bridgeprotocol.ObservationDrainResponse
	err := c.call(ctx, request, &result)
	return result, err
}

func (c *MCPMAdminClient) DecideConfirmation(ctx context.Context, approve bool, input bridgeprotocol.ConfirmationDecisionRequest) (bridgeprotocol.ConfirmationDecisionResponse, error) {
	operation := "reject_confirmation"
	if approve {
		operation = "approve_confirmation"
	}
	request := struct {
		Operation   string `json:"operation"`
		ChallengeID string `json:"challengeId"`
		BindingHash string `json:"bindingHash"`
	}{Operation: operation, ChallengeID: input.ChallengeID, BindingHash: input.BindingHash}
	var result bridgeprotocol.ConfirmationDecisionResponse
	err := c.call(ctx, request, &result)
	return result, err
}

func (c *MCPMAdminClient) call(ctx context.Context, request, output any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(body) == 0 || len(body) > MCPMAdminMaxMessageBytes {
		return errors.New("mcpm admin request exceeds the size limit")
	}
	info, err := os.Lstat(c.socketPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "mcpm admin socket is unavailable", Retryable: true}
	}
	timeout := c.timeout
	if timeout <= 0 || timeout > mcpmAdminTimeout {
		timeout = mcpmAdminTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(callCtx, "unix", c.socketPath)
	if err != nil {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "mcpm admin socket is unavailable", Retryable: true}
	}
	defer connection.Close()
	afterDial, err := os.Lstat(c.socketPath)
	if err != nil || afterDial.Mode()&os.ModeSymlink != 0 || afterDial.Mode()&os.ModeSocket == 0 || !os.SameFile(info, afterDial) {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "mcpm admin socket changed during connection", Retryable: true}
	}
	if deadline, ok := callCtx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return err
		}
	}
	if _, err := connection.Write(append(body, '\n')); err != nil {
		return adminTransportError(err)
	}
	reader := bufio.NewReader(io.LimitReader(connection, MCPMAdminMaxMessageBytes+2))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return adminTransportError(err)
	}
	if len(line) == 0 || len(line)-1 > MCPMAdminMaxMessageBytes {
		return errors.New("mcpm admin response exceeds the size limit")
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("mcpm admin response must contain exactly one line")
		}
		return adminTransportError(err)
	}
	return decodeMCPMAdminEnvelope(bytes.TrimSuffix(line, []byte{'\n'}), output)
}

func decodeMCPMAdminEnvelope(body []byte, output any) error {
	var envelope struct {
		OK    *bool           `json:"ok"`
		Data  json.RawMessage `json:"data,omitempty"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := decodeStrictAdminJSON(body, &envelope); err != nil {
		return fmt.Errorf("decode mcpm admin response: %w", err)
	}
	if envelope.OK == nil {
		return errors.New("mcpm admin response omitted status")
	}
	if *envelope.OK {
		if envelope.Error != nil || len(envelope.Data) == 0 {
			return errors.New("mcpm admin success response is invalid")
		}
		if err := bridgeprotocol.ValidateGovernanceBody(envelope.Data); err != nil {
			return fmt.Errorf("validate mcpm admin result: %w", err)
		}
		if output == nil {
			return nil
		}
		if err := decodeStrictAdminJSON(envelope.Data, output); err != nil {
			return fmt.Errorf("decode mcpm admin result: %w", err)
		}
		return nil
	}
	if envelope.Error == nil || len(envelope.Data) != 0 || !mcpmAdminErrorCodePattern.MatchString(envelope.Error.Code) {
		return errors.New("mcpm admin error response is invalid")
	}
	return boundedMCPMAdminError(envelope.Error.Code)
}

func boundedMCPMAdminError(code string) *bridgeprotocol.APIError {
	switch code {
	case "challenge_expired", "challenge_unknown", "binding_mismatch":
		return bridgeprotocol.BoundedAPIError(&bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict}, bridgeprotocol.ErrRevisionConflict)
	case "request_invalid", "operation_invalid", "request_fields_invalid", "challenge_id_invalid", "binding_hash_invalid",
		"observation_cursor_invalid", "observation_limit_invalid", "observation_request_invalid":
		return bridgeprotocol.BoundedAPIError(&bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest}, bridgeprotocol.ErrInvalidRequest)
	default:
		return bridgeprotocol.BoundedAPIError(&bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy}, bridgeprotocol.ErrRelayUnhealthy)
	}
}

func decodeStrictAdminJSON(body []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("mcpm admin response contains trailing JSON")
	}
	return nil
}

func adminTransportError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "mcpm admin request timed out", Retryable: true}
	}
	return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "mcpm admin request failed", Retryable: true}
}

func (r *RelayManager) RelayCapability(ctx context.Context) (bridgeprotocol.RelayCapabilityResponse, error) {
	return r.adminClient().Capability(ctx)
}

func (r *RelayManager) RelaySessionCanary(ctx context.Context, input bridgeprotocol.RelaySessionCanaryRequest) (bridgeprotocol.RelaySessionCanaryResponse, error) {
	bundle, err := bridgeprotocol.ValidateRelaySessionCanaryRequest(input)
	if err != nil {
		return bridgeprotocol.RelaySessionCanaryResponse{}, err
	}
	response, err := r.adminClient().SessionCanary(ctx, input)
	if err != nil {
		return bridgeprotocol.RelaySessionCanaryResponse{}, err
	}
	if err := bridgeprotocol.ValidateRelaySessionCanaryResponse(bundle, input.RoutingBundleHash, response); err != nil {
		return bridgeprotocol.RelaySessionCanaryResponse{}, incompatibleMCPMError()
	}
	return response, nil
}

func (r *RelayManager) ReloadRelayGovernance(ctx context.Context, input bridgeprotocol.RelayReloadRequest) (bridgeprotocol.RelayReloadResponse, error) {
	var bundle bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(input.RoutingBundle, &bundle); err != nil {
		return bridgeprotocol.RelayReloadResponse{}, err
	}
	_, hash, err := bundle.Canonical()
	if err != nil {
		return bridgeprotocol.RelayReloadResponse{}, err
	}
	if hash != input.RoutingBundleHash || bundle.RelayConfigurationRevisionID != input.RelayConfigurationRevisionID {
		return bridgeprotocol.RelayReloadResponse{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Relay routing bundle binding does not match"}
	}
	status, err := r.adminClient().ReloadRouting(ctx)
	if err != nil {
		return bridgeprotocol.RelayReloadResponse{}, err
	}
	if status.RoutingBundleHash != hash || status.RelayConfigurationRevisionID != input.RelayConfigurationRevisionID {
		return bridgeprotocol.RelayReloadResponse{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Relay reloaded a different routing bundle"}
	}
	return bridgeprotocol.RelayReloadResponse{Reloaded: true, RoutingBundleHash: hash}, nil
}

func (r *RelayManager) ObserveRelayContracts(ctx context.Context) (bridgeprotocol.ContractObservationResponse, error) {
	return r.adminClient().ObserveContracts(ctx)
}

func (r *RelayManager) ListRelayConfirmations(ctx context.Context) (bridgeprotocol.ConfirmationListResponse, error) {
	return r.adminClient().ListConfirmations(ctx)
}

func (r *RelayManager) DecideRelayConfirmation(ctx context.Context, approve bool, input bridgeprotocol.ConfirmationDecisionRequest) (bridgeprotocol.ConfirmationDecisionResponse, error) {
	if !bridgeprotocol.IsSHA256(input.ChallengeID) || !bridgeprotocol.IsSHA256(input.BindingHash) {
		return bridgeprotocol.ConfirmationDecisionResponse{}, errors.New("confirmation challenge and binding hashes are invalid")
	}
	return r.adminClient().DecideConfirmation(ctx, approve, input)
}

func (r *RelayManager) DrainRelayObservations(ctx context.Context, input bridgeprotocol.ObservationDrainRequest) (bridgeprotocol.ObservationDrainResponse, error) {
	return r.adminClient().DrainObservations(ctx, input)
}

func (r *RelayManager) adminClient() MCPMAdmin {
	if r.Admin == nil {
		return NewMCPMAdminClient()
	}
	return r.Admin
}
