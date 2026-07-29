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

	"github.com/Junhao2314/toolhub/internal/domain"
	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
)

func (e *Executor) discoverInventory(ctx context.Context) (domain.AgentInventory, error) {
	key, err := base64.StdEncoding.DecodeString(e.config.TaskKey)
	if err != nil || len(key) != 32 {
		return domain.AgentInventory{}, errors.New("agent task key is invalid")
	}
	scan, err := runtimeadapter.ScanAllConfigured(e.config.Paths, e.config.SharedSources, e.config.DataDir, key)
	if err != nil {
		return domain.AgentInventory{}, err
	}
	publishHermesReadOnlyCapability(scan.Runtimes)
	inventory := domain.AgentInventory{
		Runtimes: scan.Runtimes, SharedSources: scan.SharedSources, MCPImports: scan.MCPImports,
	}
	var response struct {
		CaptureRequests []domain.MCPCaptureRequest `json:"captureRequests"`
	}
	if err := e.postAgentJSON(ctx, "/agent/v1/discoveries/descriptors", inventory, &response); err != nil {
		return domain.AgentInventory{}, err
	}
	for _, request := range response.CaptureRequests {
		secrets, ok := scan.MCPSecrets[request.Identity]
		if !ok {
			return domain.AgentInventory{}, errors.New("control plane requested an unknown MCP secret identity")
		}
		capture := struct {
			Token    string            `json:"token"`
			Runtime  string            `json:"runtime"`
			Name     string            `json:"name"`
			Identity string            `json:"identity"`
			Env      map[string]string `json:"env"`
			Headers  map[string]string `json:"headers"`
		}{request.Token, request.Runtime, request.Name, request.Identity, secrets.Env, secrets.Headers}
		if err := e.postAgentJSON(ctx, "/agent/v1/discoveries/capture", capture, nil); err != nil {
			return domain.AgentInventory{}, err
		}
	}
	return inventory, nil
}

// Runtime config is an open object understood by older control planes. Keeping
// the capability there preserves rolling-upgrade compatibility.
func publishHermesReadOnlyCapability(runtimes []domain.InventoryRuntime) {
	for index := range runtimes {
		if runtimes[index].Kind != domain.RuntimeHermes {
			continue
		}
		if runtimes[index].Config == nil {
			runtimes[index].Config = map[string]any{}
		}
		capabilities := []string{}
		switch configured := runtimes[index].Config["capabilities"].(type) {
		case []string:
			capabilities = append(capabilities, configured...)
		case []any:
			for _, value := range configured {
				if capability, ok := value.(string); ok {
					capabilities = append(capabilities, capability)
				}
			}
		}
		seen := map[string]bool{}
		result := make([]string, 0, len(capabilities)+1)
		for _, capability := range append(capabilities, domain.CapabilityHermesReadOnlyImportV1) {
			capability = strings.TrimSpace(capability)
			if capability != "" && !seen[capability] {
				seen[capability] = true
				result = append(result, capability)
			}
		}
		runtimes[index].Config["capabilities"] = result
	}
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
