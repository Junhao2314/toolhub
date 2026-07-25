package agentclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/toolhub-dev/toolhub/internal/domain"
	"github.com/toolhub-dev/toolhub/internal/protocol"
	runtimeadapter "github.com/toolhub-dev/toolhub/internal/runtime"
	"github.com/toolhub-dev/toolhub/internal/security"
)

type Executor struct {
	config  Config
	http    *http.Client
	history *taskHistory
}

func NewExecutor(config Config) *Executor {
	return &Executor{config: config, http: &http.Client{Timeout: 45 * time.Second}, history: loadHistory(config.DataDir)}
}

func (e *Executor) Execute(ctx context.Context, task domain.AgentTask) (string, json.RawMessage) {
	if record, ok := e.history.get(task.ID); ok {
		return record.Status, record.Result
	}
	if err := e.verify(task); err != nil {
		return "failed", marshalResult(map[string]any{"error": err.Error(), "code": "signature_invalid"})
	}
	var result any
	var err error
	switch task.Kind {
	case "scan_inventory":
		var runtimes []domain.InventoryRuntime
		runtimes, err = runtimeadapter.ScanAll(e.config.Paths)
		result = map[string]any{"runtimes": runtimes}
	case "deploy_skill":
		result, err = e.deploySkill(ctx, task.Payload)
	case "apply_mcp":
		result, err = e.applyMCP(ctx, task.Payload)
	default:
		err = fmt.Errorf("unsupported task kind %q", task.Kind)
	}
	status := "succeeded"
	if err != nil {
		status = "failed"
		result = map[string]any{"error": err.Error()}
	}
	encoded := marshalResult(result)
	_ = e.history.put(task.ID, taskRecord{Status: status, Result: encoded, CompletedAt: time.Now().UTC()})
	return status, encoded
}

func (e *Executor) verify(task domain.AgentTask) error {
	key, err := base64.StdEncoding.DecodeString(e.config.TaskKey)
	if err != nil || len(key) != 32 {
		return errors.New("agent task key is invalid")
	}
	signingBytes, err := protocol.TaskSigningBytes(task.ID, task.Kind, task.Payload)
	if err != nil {
		return err
	}
	if !security.VerifyPayload(key, signingBytes, task.Signature) {
		return errors.New("task signature verification failed")
	}
	return nil
}

func (e *Executor) deploySkill(ctx context.Context, raw json.RawMessage) (any, error) {
	var request struct {
		Runtime   string `json:"runtime"`
		SkillSlug string `json:"skillSlug"`
		VersionID string `json:"versionId"`
		SHA256    string `json:"sha256"`
		Enabled   bool   `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	var artifact []byte
	var err error
	if request.Enabled {
		artifact, err = e.fetchBytes(ctx, "/agent/v1/artifacts/"+request.VersionID, 20<<20)
		if err != nil {
			return nil, err
		}
	}
	deployer := runtimeadapter.Deployer{DataDir: e.config.DataDir, Paths: e.config.Paths}
	return deployer.Deploy(runtimeadapter.DeployRequest{Runtime: request.Runtime, SkillSlug: request.SkillSlug, VersionID: request.VersionID, SHA256: request.SHA256, Enabled: request.Enabled, Artifact: artifact})
}

func (e *Executor) applyMCP(ctx context.Context, raw json.RawMessage) (any, error) {
	var profile map[string]any
	if err := json.Unmarshal(raw, &profile); err != nil {
		return nil, err
	}
	runtimeKind, _ := profile["runtime"].(string)
	resolver := func(ctx context.Context, id string) (string, error) {
		body, err := e.fetchBytes(ctx, "/agent/v1/secrets/"+id, 1<<20)
		if err != nil {
			return "", err
		}
		var response struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return "", err
		}
		return response.Value, nil
	}
	return runtimeadapter.ApplyMCP(ctx, e.config.Paths, e.config.DataDir, runtimeKind, profile, resolver)
}

func (e *Executor) fetchBytes(ctx context.Context, endpoint string, max int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.config.ServerURL, "/")+endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+e.config.AgentToken)
	request.Header.Set("X-ToolHub-Node-ID", e.config.NodeID)
	response, err := e.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control plane returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, errors.New("control plane response exceeds size limit")
	}
	return body, nil
}

func marshalResult(value any) json.RawMessage {
	body, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"error":"encode task result"}`)
	}
	return body
}
