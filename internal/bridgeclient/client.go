package bridgeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/security"
)

type Client struct {
	key  []byte
	http *http.Client
	now  func() time.Time
}

func New(socketPath string, key []byte) (*Client, error) {
	if strings.TrimSpace(socketPath) == "" || len(key) != 32 {
		return nil, errors.New("Bridge socket path and 32-byte HMAC key are required")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableCompression: true, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second,
	}
	return &Client{key: append([]byte(nil), key...), http: &http.Client{Transport: transport, Timeout: 15 * time.Minute}, now: time.Now}, nil
}

func (c *Client) call(ctx context.Context, method, path, idempotency string, input, output any) error {
	body := []byte(nil)
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://bridge"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	nonce, err := security.RandomToken(18)
	if err != nil {
		return err
	}
	bridgeprotocol.SignRequest(request, c.key, body, c.now(), nonce)
	if idempotency != "" {
		request.Header.Set(bridgeprotocol.HeaderIdempotencyKey, idempotency)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrTargetUnavailable, Message: "ToolHub Bridge is unavailable", Retryable: true}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error *bridgeprotocol.APIError `json:"error"`
		}
		if json.Unmarshal(responseBody, &envelope) == nil && envelope.Error != nil {
			return envelope.Error
		}
		return fmt.Errorf("Bridge returned HTTP %d", response.StatusCode)
	}
	if output != nil && len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, output); err != nil {
			return fmt.Errorf("decode Bridge response: %w", err)
		}
	}
	return nil
}

func (c *Client) Health(ctx context.Context) error {
	return c.call(ctx, http.MethodGet, "/v1/health", "", nil, nil)
}

func (c *Client) RefreshNodes(ctx context.Context, key string) (bridgeprotocol.RefreshNodesResponse, error) {
	var result bridgeprotocol.RefreshNodesResponse
	err := c.call(ctx, http.MethodPost, "/v1/nodes/refresh", key, map[string]any{}, &result)
	return result, err
}

func (c *Client) Scan(ctx context.Context, key string, input bridgeprotocol.ScanRequest) (bridgeprotocol.ScanResponse, error) {
	var result bridgeprotocol.ScanResponse
	err := c.call(ctx, http.MethodPost, "/v1/targets/scan", key, input, &result)
	return result, err
}

func (c *Client) ExportLocalSkill(ctx context.Context, input bridgeprotocol.LocalSkillExportRequest) (bridgeprotocol.LocalSkillExportResponse, error) {
	var result bridgeprotocol.LocalSkillExportResponse
	err := c.call(ctx, http.MethodPost, "/v1/local/skills/export", "", input, &result)
	return result, err
}

func (c *Client) PreviewLocalMCP(ctx context.Context, input bridgeprotocol.LocalMCPPreviewRequest) (bridgeprotocol.LocalMCPPreviewResponse, error) {
	var result bridgeprotocol.LocalMCPPreviewResponse
	err := c.call(ctx, http.MethodPost, "/v1/local/mcp/preview", "", input, &result)
	return result, err
}

func (c *Client) CaptureLocalMCP(ctx context.Context, input bridgeprotocol.LocalMCPCaptureRequest) (bridgeprotocol.LocalMCPCaptureResponse, error) {
	var result bridgeprotocol.LocalMCPCaptureResponse
	err := c.call(ctx, http.MethodPost, "/v1/local/mcp/capture", "", input, &result)
	return result, err
}

func (c *Client) Preflight(ctx context.Context, key string, input bridgeprotocol.PreflightRequest) (bridgeprotocol.PreflightResponse, error) {
	var result bridgeprotocol.PreflightResponse
	err := c.call(ctx, http.MethodPost, "/v1/targets/preflight", key, input, &result)
	return result, err
}

func (c *Client) Commit(ctx context.Context, kind, key string, input bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	if kind != "apply" && kind != "edit" && kind != "restore" {
		return bridgeprotocol.TargetResult{}, errors.New("unsupported Bridge commit kind")
	}
	var result bridgeprotocol.TargetResult
	err := c.call(ctx, http.MethodPost, "/v1/targets/"+kind, key, input, &result)
	return result, err
}

func (c *Client) Reconcile(ctx context.Context, key string, input bridgeprotocol.ReconcileRequest) (bridgeprotocol.TargetResult, error) {
	var result bridgeprotocol.TargetResult
	err := c.call(ctx, http.MethodPost, "/v1/targets/reconcile", key, input, &result)
	return result, err
}

func (c *Client) Relay(ctx context.Context, action, key string, input bridgeprotocol.RelayActionRequest) (bridgeprotocol.RelayStatus, error) {
	allowed := map[string]bool{"status": true, "start": true, "stop": true, "restart": true, "health": true}
	if !allowed[action] {
		return bridgeprotocol.RelayStatus{}, errors.New("unsupported relay action")
	}
	var result bridgeprotocol.RelayStatus
	err := c.call(ctx, http.MethodPost, "/v1/relay/"+url.PathEscape(action), key, input, &result)
	return result, err
}

func (c *Client) GCBackups(ctx context.Context, key string, input bridgeprotocol.BackupGCRequest) (bridgeprotocol.BackupGCResponse, error) {
	var result bridgeprotocol.BackupGCResponse
	err := c.call(ctx, http.MethodPost, "/v1/backups/gc", key, input, &result)
	return result, err
}

func (c *Client) Operation(ctx context.Context, operationID string) (bridgeprotocol.Operation, error) {
	if strings.TrimSpace(operationID) == "" {
		return bridgeprotocol.Operation{}, errors.New("Bridge operation ID is required")
	}
	var result bridgeprotocol.Operation
	err := c.call(ctx, http.MethodGet, "/v1/operations/"+url.PathEscape(operationID), "", nil, &result)
	return result, err
}
