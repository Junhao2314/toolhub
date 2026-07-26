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
	"path/filepath"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/skills"
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
		result, err = e.discoverInventory(ctx)
	case "deploy_skill":
		result, err = e.deploySkill(ctx, task.Payload)
	case "apply_mcp":
		result, err = e.applyMCP(ctx, task.Payload)
	case "adopt_skill":
		result, err = e.adoptSkill(ctx, task.ID, task.Payload)
	case "sync_shared":
		result, err = e.syncShared(ctx, task.Payload)
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

func (e *Executor) syncShared(ctx context.Context, raw json.RawMessage) (any, error) {
	var payload protocol.SyncSharedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.SourceID == "" || payload.SourceName == "" {
		return nil, errors.New("shared sync task requires sourceId and sourceName")
	}
	key, err := base64.StdEncoding.DecodeString(e.config.TaskKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("agent task key is invalid")
	}
	reconciler := runtimeadapter.SharedReconciler{DataDir: e.config.DataDir, Sources: e.config.SharedSources, FingerprintKey: key}
	return reconciler.Reconcile(ctx, runtimeadapter.SharedSyncRequest{
		SourceName:                payload.SourceName,
		Scopes:                    payload.Scopes,
		DryRun:                    payload.DryRun,
		ExpectedSourceFingerprint: payload.ExpectedSourceFingerprint,
	})
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
		Runtime    string `json:"runtime"`
		SourceName string `json:"sourceName"`
		SkillSlug  string `json:"skillSlug"`
		VersionID  string `json:"versionId"`
		SHA256     string `json:"sha256"`
		Enabled    bool   `json:"enabled"`
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
	deployer := runtimeadapter.Deployer{DataDir: e.config.DataDir, Paths: e.config.Paths, SharedSources: e.config.SharedSources}
	result, err := deployer.Deploy(runtimeadapter.DeployRequest{Runtime: request.Runtime, SourceName: request.SourceName, SkillSlug: request.SkillSlug, VersionID: request.VersionID, SHA256: request.SHA256, Enabled: request.Enabled, Artifact: artifact})
	if err != nil || request.Runtime != domain.RuntimeShared {
		return result, err
	}
	key, decodeErr := base64.StdEncoding.DecodeString(e.config.TaskKey)
	if decodeErr != nil || len(key) != 32 {
		return nil, errors.New("agent task key is invalid")
	}
	syncResult, syncErr := (runtimeadapter.SharedReconciler{DataDir: e.config.DataDir, Sources: e.config.SharedSources, FingerprintKey: key}).Reconcile(ctx, runtimeadapter.SharedSyncRequest{SourceName: request.SourceName, Scopes: []string{"skills"}})
	if syncErr != nil {
		return nil, syncErr
	}
	return map[string]any{"deployment": result, "sharedSync": syncResult}, nil
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
	e.authorizeAgentRequest(request)
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

func (e *Executor) adoptSkill(ctx context.Context, taskID string, raw json.RawMessage) (any, error) {
	var request struct {
		DiscoveryID string `json:"discoveryId"`
		Runtime     string `json:"runtime"`
		Path        string `json:"path"`
		SHA256      string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	paths := e.config.Paths
	var pkg skills.Package
	var err error
	if request.Runtime == domain.RuntimeShared {
		var sharedSource *runtimeadapter.SharedSourceConfig
		for _, source := range e.config.SharedSources {
			relative, relErr := filepath.Rel(source.SkillsRoot, request.Path)
			if relErr == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				sourceCopy := source
				sharedSource = &sourceCopy
				break
			}
		}
		if sharedSource == nil {
			return nil, errors.New("shared Skill discovery is outside configured sources")
		}
		pkg, err = runtimeadapter.PackageSharedDiscoveredSkill(*sharedSource, request.Path, request.SHA256)
	} else {
		pkg, err = runtimeadapter.PackageDiscoveredSkill(paths, request.Runtime, request.Path, request.SHA256)
	}
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.config.ServerURL, "/")+"/agent/v1/discoveries/"+request.DiscoveryID+"/skill", bytes.NewReader(pkg.CanonicalZIP))
	if err != nil {
		return nil, err
	}
	e.authorizeAgentRequest(httpRequest)
	httpRequest.Header.Set("Content-Type", "application/zip")
	httpRequest.Header.Set("X-ToolHub-Task-ID", taskID)
	httpRequest.Header.Set("X-Content-SHA256", pkg.SHA256)
	response, err := e.http.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("upload discovered Skill: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control plane rejected discovered Skill with HTTP %d", response.StatusCode)
	}
	var imported struct {
		SkillID   string `json:"skillId"`
		VersionID string `json:"versionId"`
		SHA256    string `json:"sha256"`
	}
	if err := json.Unmarshal(body, &imported); err != nil {
		return nil, errors.New("control plane returned an invalid Skill adoption response")
	}
	marker := runtimeadapter.AdoptedSkillMarker{SkillID: imported.SkillID, VersionID: imported.VersionID, SHA256: imported.SHA256}
	if request.Runtime != domain.RuntimeShared {
		if err := runtimeadapter.MarkAdoptedSkill(paths, request.Runtime, request.Path, request.SHA256, marker); err != nil {
			return nil, err
		}
	}
	return map[string]any{"discoveryId": request.DiscoveryID, "skillId": imported.SkillID, "versionId": imported.VersionID, "sha256": imported.SHA256, "markerWritten": request.Runtime != domain.RuntimeShared}, nil
}

func marshalResult(value any) json.RawMessage {
	body, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"error":"encode task result"}`)
	}
	return body
}
