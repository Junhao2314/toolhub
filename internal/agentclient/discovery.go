package agentclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/toolhub-dev/toolhub/internal/domain"
	runtimeadapter "github.com/toolhub-dev/toolhub/internal/runtime"
)

func (e *Executor) discoverInventory(ctx context.Context) ([]domain.InventoryRuntime, error) {
	key, err := base64.StdEncoding.DecodeString(e.config.TaskKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("agent task key is invalid")
	}
	scan, err := runtimeadapter.ScanAllWithKey(e.config.Paths, key)
	if err != nil {
		return nil, err
	}
	var response struct {
		CaptureRequests []domain.MCPCaptureRequest `json:"captureRequests"`
	}
	if err := e.postAgentJSON(ctx, "/agent/v1/discoveries/descriptors", map[string]any{"runtimes": scan.Runtimes}, &response); err != nil {
		return nil, err
	}
	for _, request := range response.CaptureRequests {
		secrets, ok := scan.MCPSecrets[request.Identity]
		if !ok {
			return nil, errors.New("control plane requested an unknown MCP secret identity")
		}
		capture := struct {
			Token    string            `json:"token"`
			Runtime  string            `json:"runtime"`
			Name     string            `json:"name"`
			Identity string            `json:"identity"`
			Secrets  map[string]string `json:"secrets"`
		}{request.Token, request.Runtime, request.Name, request.Identity, secrets}
		if err := e.postAgentJSON(ctx, "/agent/v1/discoveries/capture", capture, nil); err != nil {
			return nil, err
		}
	}
	return scan.Runtimes, nil
}

func (e *Executor) postAgentJSON(ctx context.Context, endpoint string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.config.ServerURL, "/")+endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	e.authorizeAgentRequest(request)
	request.Header.Set("Content-Type", "application/json")
	response, err := e.http.Do(request)
	if err != nil {
		return fmt.Errorf("submit Agent discovery: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("control plane rejected Agent discovery with HTTP %d", response.StatusCode)
	}
	if target != nil && len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, target); err != nil {
			return errors.New("control plane returned an invalid Agent discovery response")
		}
	}
	return nil
}

func (e *Executor) authorizeAgentRequest(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+e.config.AgentToken)
	request.Header.Set("X-ToolHub-Node-ID", e.config.NodeID)
}
