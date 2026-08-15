package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/runtime"
	"github.com/Junhao2314/toolhub/internal/saltdriver"
)

type CompositeAdapter struct {
	Journal      *Journal
	Local        *runtime.Manager
	RelayManager *runtime.RelayManager
	Salt         *saltdriver.Driver
}

type saltReconcilePayload struct {
	bridgeprotocol.ReconcileRequest
	ExpectedRevision string `json:"expectedRevision,omitempty"`
}

func NewCompositeAdapter(journal *Journal, local *runtime.Manager, relay *runtime.RelayManager, salt *saltdriver.Driver) *CompositeAdapter {
	return &CompositeAdapter{Journal: journal, Local: local, RelayManager: relay, Salt: salt}
}

func (a *CompositeAdapter) Health(ctx context.Context) error {
	if a.Journal == nil || a.Local == nil || a.RelayManager == nil || a.Salt == nil {
		return errors.New("Bridge adapter is incomplete")
	}
	return a.Salt.Health(ctx)
}

func (a *CompositeAdapter) RefreshNodes(ctx context.Context) (bridgeprotocol.RefreshNodesResponse, error) {
	nodes, err := a.Salt.AcceptedNodes(ctx)
	if err != nil {
		return bridgeprotocol.RefreshNodesResponse{}, err
	}
	result := bridgeprotocol.RefreshNodesResponse{Nodes: []bridgeprotocol.NodeInfo{{NodeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("toolhub-local-node")).String(), Name: "local", Kind: bridgeprotocol.NodeKindLocal, Status: "online"}}}
	for _, node := range nodes {
		status := "unavailable"
		if node.Online {
			status = "online"
		}
		result.Nodes = append(result.Nodes, bridgeprotocol.NodeInfo{NodeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("salt:"+node.MinionID)).String(), Name: node.MinionID, Kind: bridgeprotocol.NodeKindSalt, SaltMinionID: node.MinionID, Status: status, Version: node.Version})
	}
	return result, nil
}

func (a *CompositeAdapter) Scan(ctx context.Context, input bridgeprotocol.ScanRequest) (bridgeprotocol.ScanResponse, error) {
	if input.Target.NodeKind == bridgeprotocol.NodeKindLocal {
		user, err := runtime.LookupManagedUser(input.Target.ManagedUsername)
		if err != nil {
			return bridgeprotocol.ScanResponse{}, err
		}
		scan, err := a.Local.Scan(user, input.Target.Runtime)
		if err != nil {
			return bridgeprotocol.ScanResponse{}, err
		}
		return bridgeprotocol.ScanResponse{TargetRevision: scan.Revision, Members: scan.Members}, nil
	}
	target, err := a.prepareSaltTarget(ctx, input.Target)
	if err != nil {
		return bridgeprotocol.ScanResponse{}, err
	}
	input.Target = target
	bundle, err := a.Salt.Stage(ctx, input.Target.SaltMinionID, input)
	if err != nil {
		return bridgeprotocol.ScanResponse{}, err
	}
	defer a.Salt.CleanupRemote(input.Target.SaltMinionID, bundle)
	raw, err := a.Salt.Call(ctx, input.Target.SaltMinionID, "toolhub.scan", bundle.RemotePath)
	if err != nil {
		return bridgeprotocol.ScanResponse{}, err
	}
	var response struct {
		OK             bool                             `json:"ok"`
		TargetRevision string                           `json:"targetRevision"`
		Members        []bridgeprotocol.InventoryMember `json:"members"`
		Error          *bridgeprotocol.APIError         `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return bridgeprotocol.ScanResponse{}, errors.New("Salt scan result is invalid")
	}
	if !response.OK {
		return bridgeprotocol.ScanResponse{}, response.Error
	}
	return bridgeprotocol.ScanResponse{TargetRevision: response.TargetRevision, Members: response.Members}, nil
}

func (a *CompositeAdapter) ExportLocalSkill(_ context.Context, input bridgeprotocol.LocalSkillExportRequest) (bridgeprotocol.LocalSkillExportResponse, error) {
	user, err := runtime.LookupManagedUser(input.Target.ManagedUsername)
	if err != nil {
		return bridgeprotocol.LocalSkillExportResponse{}, err
	}
	return a.Local.ExportLocalSkill(user, input)
}

func (a *CompositeAdapter) ExportLocalSkillBatch(_ context.Context, input bridgeprotocol.LocalSkillBatchExportRequest) (bridgeprotocol.LocalSkillBatchExportResponse, error) {
	user, err := runtime.LookupManagedUser(input.Target.ManagedUsername)
	if err != nil {
		return bridgeprotocol.LocalSkillBatchExportResponse{}, err
	}
	return a.Local.ExportLocalSkillBatch(user, input)
}

func (a *CompositeAdapter) PreviewLocalMCP(_ context.Context, input bridgeprotocol.LocalMCPPreviewRequest) (bridgeprotocol.LocalMCPPreviewResponse, error) {
	user, err := runtime.LookupManagedUser(input.Target.ManagedUsername)
	if err != nil {
		return bridgeprotocol.LocalMCPPreviewResponse{}, err
	}
	return a.Local.PreviewLocalMCP(user, input.Target)
}

func (a *CompositeAdapter) CaptureLocalMCP(_ context.Context, input bridgeprotocol.LocalMCPCaptureRequest) (bridgeprotocol.LocalMCPCaptureResponse, error) {
	user, err := runtime.LookupManagedUser(input.Target.ManagedUsername)
	if err != nil {
		return bridgeprotocol.LocalMCPCaptureResponse{}, err
	}
	return a.Local.CaptureLocalMCP(user, input)
}

func (a *CompositeAdapter) Preflight(ctx context.Context, input bridgeprotocol.PreflightRequest) (bridgeprotocol.PreflightResponse, error) {
	if input.Target.NodeKind == bridgeprotocol.NodeKindLocal {
		user, err := runtime.LookupManagedUser(input.Target.ManagedUsername)
		if err != nil {
			return bridgeprotocol.PreflightResponse{}, err
		}
		if input.Target.Runtime == bridgeprotocol.RuntimeSharedRelay {
			scan, err := a.Local.Scan(user, input.Target.Runtime)
			if err != nil {
				return bridgeprotocol.PreflightResponse{}, err
			}
			_, hash, err := input.Manifest.Canonical()
			if err != nil {
				return bridgeprotocol.PreflightResponse{}, err
			}
			return bridgeprotocol.PreflightResponse{TargetRevision: scan.Revision, ManifestHash: hash, Diff: relayDiff(scan.Members, input.Manifest)}, nil
		}
		return a.Local.Preflight(user, input.Manifest)
	}
	target, err := a.prepareSaltTarget(ctx, input.Target)
	if err != nil {
		return bridgeprotocol.PreflightResponse{}, err
	}
	input.Target = target
	bundle, err := a.Salt.Stage(ctx, input.Target.SaltMinionID, input)
	if err != nil {
		return bridgeprotocol.PreflightResponse{}, err
	}
	defer a.Salt.CleanupRemote(input.Target.SaltMinionID, bundle)
	raw, err := a.Salt.Call(ctx, input.Target.SaltMinionID, "toolhub.preflight", bundle.RemotePath)
	if err != nil {
		return bridgeprotocol.PreflightResponse{}, err
	}
	var response struct {
		OK             bool                     `json:"ok"`
		TargetRevision string                   `json:"targetRevision"`
		ManifestHash   string                   `json:"manifestHash"`
		Diff           bridgeprotocol.Diff      `json:"diff"`
		Error          *bridgeprotocol.APIError `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return bridgeprotocol.PreflightResponse{}, errors.New("Salt preflight result is invalid")
	}
	if !response.OK {
		return bridgeprotocol.PreflightResponse{}, response.Error
	}
	return bridgeprotocol.PreflightResponse{TargetRevision: response.TargetRevision, ManifestHash: response.ManifestHash, Diff: response.Diff}, nil
}

func relayDiff(current []bridgeprotocol.InventoryMember, manifest bridgeprotocol.DesiredManifest) bridgeprotocol.Diff {
	diff := bridgeprotocol.Diff{Add: []bridgeprotocol.DiffItem{}, Replace: []bridgeprotocol.DiffItem{}, Delete: []bridgeprotocol.DiffItem{}, Excluded: []bridgeprotocol.DiffItem{}}
	desired := map[string]bridgeprotocol.MCPMember{}
	for _, server := range manifest.MCPServers {
		desired[server.Name] = server
	}
	for _, item := range current {
		if item.Kind == "anchor" {
			continue
		}
		server, ok := desired[item.Name]
		if !ok {
			diff.Delete = append(diff.Delete, bridgeprotocol.DiffItem{Kind: "mcp", Name: item.Name})
		} else if item.ContentHash != server.ContentHash {
			diff.Replace = append(diff.Replace, bridgeprotocol.DiffItem{Kind: "mcp", MemberID: server.MemberID, Name: item.Name})
		}
		delete(desired, item.Name)
	}
	for _, server := range desired {
		diff.Add = append(diff.Add, bridgeprotocol.DiffItem{Kind: "mcp", MemberID: server.MemberID, Name: server.Name})
	}
	return diff
}

func (a *CompositeAdapter) Commit(ctx context.Context, request bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	if request.Target.NodeKind == bridgeprotocol.NodeKindLocal {
		user, err := runtime.LookupManagedUser(request.Target.ManagedUsername)
		if err != nil {
			return bridgeprotocol.TargetResult{}, err
		}
		var result bridgeprotocol.TargetResult
		if request.Target.Runtime == bridgeprotocol.RuntimeSharedRelay {
			result, err = a.RelayManager.Apply(ctx, user, request)
		} else {
			result, err = a.Local.Apply(user, request)
		}
		if err == nil {
			a.persistResultBackup(request.Target, request.OperationID, result)
		}
		return result, err
	}
	return a.saltMutation(ctx, request.Target, request.OperationID, "toolhub.apply", request)
}

func (a *CompositeAdapter) Reconcile(ctx context.Context, request bridgeprotocol.ReconcileRequest) (bridgeprotocol.TargetResult, error) {
	if request.Target.NodeKind == bridgeprotocol.NodeKindLocal {
		user, err := runtime.LookupManagedUser(request.Target.ManagedUsername)
		if err != nil {
			return bridgeprotocol.TargetResult{}, err
		}
		if request.Target.Runtime == bridgeprotocol.RuntimeSharedRelay {
			commit := bridgeprotocol.CommitRequest{OperationID: request.OperationID, OperationKind: "reconcile", Target: request.Target, Manifest: request.Manifest, Artifacts: request.Artifacts, SecretValues: request.SecretValues, IntentionalPaused: request.IntentionalPaused, FullRelayProbe: request.FullRelayProbe}
			scan, scanErr := a.Local.Scan(user, request.Target.Runtime)
			if scanErr != nil {
				return bridgeprotocol.TargetResult{}, scanErr
			}
			commit.ExpectedRevision = scan.Revision
			result, err := a.RelayManager.Apply(ctx, user, commit)
			if err == nil {
				a.persistResultBackup(request.Target, request.OperationID, result)
			}
			return result, err
		}
		result, err := a.Local.Reconcile(user, request)
		if err == nil {
			a.persistResultBackup(request.Target, request.OperationID, result)
		}
		return result, err
	}
	bundle := saltReconcilePayload{ReconcileRequest: request}
	return a.saltMutation(ctx, request.Target, request.OperationID, "toolhub.reconcile", bundle)
}

func (a *CompositeAdapter) Restore(ctx context.Context, request bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	if request.Target.NodeKind == bridgeprotocol.NodeKindLocal {
		user, err := runtime.LookupManagedUser(request.Target.ManagedUsername)
		if err != nil {
			return bridgeprotocol.TargetResult{}, err
		}
		if request.Target.Runtime == bridgeprotocol.RuntimeSharedRelay {
			result, err := a.RelayManager.Restore(ctx, user, request)
			if err == nil {
				a.persistResultBackup(request.Target, request.OperationID, result)
			}
			return result, err
		}
		result, err := a.Local.Restore(user, request)
		if err == nil {
			a.persistResultBackup(request.Target, request.OperationID, result)
		}
		return result, err
	}
	return a.saltMutation(ctx, request.Target, request.OperationID, "toolhub.restore", request)
}

func (a *CompositeAdapter) saltMutation(ctx context.Context, target bridgeprotocol.Target, operationID, function string, payload any) (bridgeprotocol.TargetResult, error) {
	preparedTarget, err := a.prepareSaltTarget(ctx, target)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	target = preparedTarget
	payload, err = bindSaltTarget(payload, target)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	manifest, secretValues := manifestAndSecretsFromPayload(payload)
	expectedMembers := []saltMemberFingerprint{}
	if manifest != nil {
		var err error
		expectedMembers, err = expectedSaltMembers(*manifest, secretValues)
		if err != nil {
			return bridgeprotocol.TargetResult{}, err
		}
	}
	bundle, err := a.Salt.Stage(ctx, target.SaltMinionID, payload)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	jid, err := a.Salt.Dispatch(ctx, target.SaltMinionID, function, bundle.RemotePath)
	if err != nil {
		a.Salt.CleanupRemote(target.SaltMinionID, bundle)
		return bridgeprotocol.TargetResult{}, err
	}
	now := time.Now().UTC()
	operation := bridgeprotocol.Operation{ID: operationID, Kind: function, Status: bridgeprotocol.OperationRunning, Targets: []bridgeprotocol.OperationTarget{{TargetID: target.ID, Status: bridgeprotocol.OperationRunning, SaltJID: jid}}, CreatedAt: now, UpdatedAt: now}
	recoveryTarget := target
	recoveryTarget.ManagedHome = ""
	recovery := saltRecoveryRecord{OperationID: operationID, Function: function, Target: recoveryTarget, SaltJID: jid, LocalBundle: bundle.LocalPath, RemoteBundle: bundle.RemotePath, ExpectedMembers: expectedMembers, CanVerifySnapshot: manifest != nil, PreserveUnmanaged: function == "toolhub.reconcile", CreatedAt: now}
	if err := a.Journal.PutRunningSaltOperation(operation, recovery); err != nil {
		a.Salt.CleanupRemote(target.SaltMinionID, bundle)
		return bridgeprotocol.TargetResult{}, fmt.Errorf("persist running Salt operation: %w", err)
	}
	defer func() {
		a.Salt.CleanupRemote(target.SaltMinionID, bundle)
		_ = a.Journal.DeleteSaltRecovery(operationID)
	}()
	raw, err := a.Salt.Poll(ctx, jid, target.SaltMinionID)
	if err != nil {
		var apiErr *bridgeprotocol.APIError
		if errors.As(err, &apiErr) && apiErr.Code == bridgeprotocol.ErrSaltJobMissing {
			scan, scanErr := a.Scan(ctx, bridgeprotocol.ScanRequest{Target: target})
			if scanErr == nil && manifest != nil && targetMatchesExpected(scan.Members, expectedMembers, recovery.PreserveUnmanaged) {
				result := bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: scan.TargetRevision}
				if finishErr := a.finishSaltOperation(operationID, function, target.ID, result.Status, result, nil); finishErr != nil {
					return bridgeprotocol.TargetResult{}, finishErr
				}
				return result, nil
			}
		}
		if finishErr := a.finishSaltOperation(operationID, function, target.ID, bridgeprotocol.OperationFailed, bridgeprotocol.TargetResult{}, publicBridgeError(err)); finishErr != nil {
			return bridgeprotocol.TargetResult{}, finishErr
		}
		return bridgeprotocol.TargetResult{}, err
	}
	result, err := a.decodeSaltResult(raw)
	if err != nil {
		if finishErr := a.finishSaltOperation(operationID, function, target.ID, bridgeprotocol.OperationFailed, bridgeprotocol.TargetResult{}, publicBridgeError(err)); finishErr != nil {
			return bridgeprotocol.TargetResult{}, finishErr
		}
		return bridgeprotocol.TargetResult{}, err
	}
	if err := a.finishSaltOperation(operationID, function, target.ID, result.Status, result, nil); err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	return result, nil
}

func bindSaltTarget(payload any, target bridgeprotocol.Target) (any, error) {
	switch value := payload.(type) {
	case bridgeprotocol.CommitRequest:
		value.Target = target
		return value, nil
	case bridgeprotocol.ReconcileRequest:
		value.Target = target
		return value, nil
	case saltReconcilePayload:
		value.Target = target
		return value, nil
	case map[string]any:
		clone := make(map[string]any, len(value)+1)
		for key, item := range value {
			clone[key] = item
		}
		clone["target"] = target
		return clone, nil
	default:
		return nil, errors.New("Salt mutation payload is not a typed target request")
	}
}

func manifestAndSecretsFromPayload(payload any) (*bridgeprotocol.DesiredManifest, map[string]string) {
	body, _ := json.Marshal(payload)
	var container struct {
		Manifest     bridgeprotocol.DesiredManifest `json:"manifest"`
		SecretValues map[string]string              `json:"secretValues"`
	}
	if json.Unmarshal(body, &container) != nil || container.Manifest.SchemaVersion == 0 {
		return nil, nil
	}
	return &container.Manifest, container.SecretValues
}

func expectedSaltMembers(manifest bridgeprotocol.DesiredManifest, secrets map[string]string) ([]saltMemberFingerprint, error) {
	result := make([]saltMemberFingerprint, 0, len(manifest.Skills)+len(manifest.MCPServers))
	for _, skill := range manifest.Skills {
		result = append(result, saltMemberFingerprint{Kind: "skill", Name: skill.Slug, ContentHash: skill.ContentHash})
	}
	for _, server := range manifest.MCPServers {
		value := map[string]any{}
		switch server.Transport {
		case "stdio":
			args := server.Args
			if args == nil {
				args = []string{}
			}
			value["command"], value["args"] = server.Command, args
		case "http", "sse":
			value["url"] = server.URL
			if server.Transport == "sse" {
				value["transport"] = "sse"
			}
		default:
			return nil, errors.New("unsupported MCP transport in recovery fingerprint")
		}
		env, err := resolvedSecretMap(server.EnvRefs, secrets)
		if err != nil {
			return nil, err
		}
		if len(env) > 0 {
			value["env"] = env
		}
		headers, err := resolvedSecretMap(server.HeaderRefs, secrets)
		if err != nil {
			return nil, err
		}
		if len(headers) > 0 {
			key := "http_headers"
			if manifest.Target.Runtime == bridgeprotocol.RuntimeClaude {
				key = "headers"
			}
			value[key] = headers
		}
		if manifest.Target.Runtime == bridgeprotocol.RuntimeClaude && (server.Transport == "http" || server.Transport == "sse") {
			value["type"] = server.Transport
		}
		hash, err := pythonJSONHash(value)
		if err != nil {
			return nil, err
		}
		result = append(result, saltMemberFingerprint{Kind: "mcp", Name: server.Name, ContentHash: hash})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func resolvedSecretMap(refs, secrets map[string]string) (map[string]string, error) {
	result := map[string]string{}
	for key, id := range refs {
		value, ok := secrets[id]
		if !ok {
			return nil, errors.New("missing MCP secret for recovery fingerprint")
		}
		result[key] = value
	}
	return result, nil
}

func pythonJSONHash(value any) (string, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	body := bytes.TrimSpace(encoded.Bytes())
	var ascii bytes.Buffer
	for len(body) > 0 {
		r, size := utf8.DecodeRune(body)
		if r == utf8.RuneError && size == 1 {
			return "", errors.New("recovery fingerprint JSON is not UTF-8")
		}
		body = body[size:]
		if r <= 0x7f {
			ascii.WriteByte(byte(r))
			continue
		}
		if r <= 0xffff {
			_, _ = fmt.Fprintf(&ascii, "\\u%04x", r)
			continue
		}
		r -= 0x10000
		_, _ = fmt.Fprintf(&ascii, "\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff))
	}
	sum := sha256.Sum256(ascii.Bytes())
	return fmt.Sprintf("%x", sum[:]), nil
}

func targetMatchesExpected(current []bridgeprotocol.InventoryMember, expected []saltMemberFingerprint, preserveUnmanaged bool) bool {
	wanted := map[string]saltMemberFingerprint{}
	for _, item := range expected {
		wanted[item.Kind+"\x00"+item.Name] = item
	}
	seen := map[string]bool{}
	for _, item := range current {
		if item.Kind != "skill" && item.Kind != "mcp" {
			continue
		}
		key := item.Kind + "\x00" + item.Name
		expectedItem, managed := wanted[key]
		if managed {
			if item.Protected || item.ContentHash != expectedItem.ContentHash {
				return false
			}
			seen[key] = true
			continue
		}
		if !preserveUnmanaged && !item.Protected {
			return false
		}
	}
	return len(seen) == len(wanted)
}

func (a *CompositeAdapter) decodeSaltResult(raw json.RawMessage) (bridgeprotocol.TargetResult, error) {
	var envelope struct {
		OK             bool                     `json:"ok"`
		Status         string                   `json:"status"`
		Health         string                   `json:"health"`
		TargetRevision string                   `json:"targetRevision"`
		BackupID       string                   `json:"backupId"`
		Backup         *bridgeprotocol.Backup   `json:"backup"`
		Repaired       bool                     `json:"repaired"`
		Error          *bridgeprotocol.APIError `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return bridgeprotocol.TargetResult{}, errors.New("Salt target result is invalid")
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return bridgeprotocol.TargetResult{}, errors.New("Salt target returned an invalid failure")
		}
		return bridgeprotocol.TargetResult{}, envelope.Error
	}
	result := bridgeprotocol.TargetResult{Status: envelope.Status, Health: envelope.Health, TargetRevision: envelope.TargetRevision, BackupID: envelope.BackupID, Repaired: envelope.Repaired}
	if envelope.Backup != nil {
		if err := a.Journal.PutBackup(*envelope.Backup); err != nil {
			return bridgeprotocol.TargetResult{}, err
		}
	}
	if result.Status == "" {
		result.Status = bridgeprotocol.OperationSucceeded
	}
	if result.Health == "" {
		result.Health = bridgeprotocol.HealthHealthy
	}
	return result, nil
}

func publicBridgeError(err error) *bridgeprotocol.APIError {
	var apiErr *bridgeprotocol.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrInvalidRequest, Message: "Salt target operation failed", Retryable: true}
}

func (a *CompositeAdapter) finishSaltOperation(operationID, function, targetID, status string, result bridgeprotocol.TargetResult, apiErr *bridgeprotocol.APIError) error {
	operation, err := a.Journal.Operation(operationID)
	saltJID := ""
	if err != nil {
		now := time.Now().UTC()
		operation = bridgeprotocol.Operation{ID: operationID, Kind: function, CreatedAt: now, UpdatedAt: now}
	} else if len(operation.Targets) == 1 {
		saltJID = operation.Targets[0].SaltJID
	}
	operation.Kind = function
	operation.Status = status
	operation.UpdatedAt = time.Now().UTC()
	if result.Status == "" {
		result.Status = status
	}
	if result.Health == "" && status == bridgeprotocol.OperationFailed {
		result.Health = bridgeprotocol.HealthBlocked
	}
	if result.Error == nil {
		result.Error = apiErr
	}
	result = journalSafeTargetResult(result)
	operation.Targets = []bridgeprotocol.OperationTarget{{TargetID: targetID, Status: status, SaltJID: saltJID, Result: &result, Error: apiErr}}
	return a.Journal.PutOperation(operation)
}

func (a *CompositeAdapter) RecoverOperations(ctx context.Context, now time.Time) error {
	recoveries, err := a.Journal.SaltRecoveries()
	if err != nil {
		return err
	}
	var recoveryErr error
	for _, recovery := range recoveries {
		operation, operationErr := a.Journal.Operation(recovery.OperationID)
		if operationErr != nil || operation.Status != bridgeprotocol.OperationRunning {
			recoveryErr = errors.Join(recoveryErr, a.Journal.DeleteSaltRecovery(recovery.OperationID))
			continue
		}
		raw, pollErr := a.Salt.Poll(ctx, recovery.SaltJID, recovery.Target.SaltMinionID)
		var result bridgeprotocol.TargetResult
		if pollErr == nil {
			result, pollErr = a.decodeSaltResult(raw)
		}
		if pollErr != nil {
			var apiErr *bridgeprotocol.APIError
			if errors.As(pollErr, &apiErr) && apiErr.Code == bridgeprotocol.ErrSaltJobMissing {
				scan, scanErr := a.Scan(ctx, bridgeprotocol.ScanRequest{Target: recovery.Target})
				if scanErr == nil && recovery.CanVerifySnapshot && targetMatchesExpected(scan.Members, recovery.ExpectedMembers, recovery.PreserveUnmanaged) {
					result = bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: scan.TargetRevision}
					pollErr = nil
				}
			}
		}
		status := bridgeprotocol.OperationSucceeded
		var apiErr *bridgeprotocol.APIError
		if pollErr != nil {
			status, apiErr = bridgeprotocol.OperationFailed, publicBridgeError(pollErr)
		} else if result.Status != "" {
			status = result.Status
		}
		recoveryErr = errors.Join(recoveryErr, a.finishSaltOperation(recovery.OperationID, recovery.Function, recovery.Target.ID, status, result, apiErr))
		a.Salt.CleanupRemote(recovery.Target.SaltMinionID, saltdriver.StagedBundle{LocalPath: recovery.LocalBundle, RemotePath: recovery.RemoteBundle})
		recoveryErr = errors.Join(recoveryErr, a.Journal.DeleteSaltRecovery(recovery.OperationID))
	}
	_ = now
	return recoveryErr
}

func (a *CompositeAdapter) prepareSaltNode(ctx context.Context, minionID string) error {
	if _, err := a.Salt.PublishAssets(); err != nil {
		return fmt.Errorf("publish Salt ToolHub assets: %w", err)
	}
	return a.Salt.EnsureCapabilities(ctx, minionID)
}

func (a *CompositeAdapter) prepareSaltTarget(ctx context.Context, target bridgeprotocol.Target) (bridgeprotocol.Target, error) {
	if err := a.prepareSaltNode(ctx, target.SaltMinionID); err != nil {
		return bridgeprotocol.Target{}, err
	}
	home, err := a.Salt.ResolveManagedHome(ctx, target.SaltMinionID, target.ManagedUsername)
	if err != nil {
		return bridgeprotocol.Target{}, err
	}
	target.ManagedHome = home
	return target, nil
}

func (a *CompositeAdapter) persistResultBackup(target bridgeprotocol.Target, operationID string, result bridgeprotocol.TargetResult) {
	if result.BackupID == "" {
		return
	}
	_ = a.Journal.PutBackup(bridgeprotocol.Backup{ID: result.BackupID, TargetID: target.ID, NodeKind: target.NodeKind, SaltMinionID: target.SaltMinionID, Runtime: target.Runtime, SourceOperationID: operationID, Revision: result.TargetRevision, CreatedAt: time.Now().UTC()})
}

func (a *CompositeAdapter) RemoveBackup(ctx context.Context, backup bridgeprotocol.Backup) error {
	if backup.NodeKind == bridgeprotocol.NodeKindLocal {
		return a.Local.RemoveBackup(backup)
	}
	if backup.NodeKind != bridgeprotocol.NodeKindSalt || backup.SaltMinionID == "" {
		return errors.New("backup catalog is missing target routing metadata")
	}
	if err := a.prepareSaltNode(ctx, backup.SaltMinionID); err != nil {
		return err
	}
	_, err := a.Salt.Call(ctx, backup.SaltMinionID, "toolhub.remove_backup", backup.TargetID, backup.ID)
	return err
}

func (a *CompositeAdapter) Relay(ctx context.Context, action string, input bridgeprotocol.RelayActionRequest) (bridgeprotocol.RelayStatus, error) {
	manifestArgs := []bridgeprotocol.DesiredManifest{}
	if input.Manifest != nil {
		manifestArgs = append(manifestArgs, *input.Manifest)
	}
	if action == "health" || action == "status" {
		return a.RelayManager.Status(ctx, input.Port, input.IntentionalPaused, manifestArgs...)
	}
	if action == "start" {
		return a.RelayManager.StartAndCheck(ctx, input.Port, input.Manifest), nil
	}
	if action == "restart" {
		return a.RelayManager.RestartAndCheck(ctx, input.Port, input.Manifest), nil
	}
	if action != "stop" {
		return bridgeprotocol.RelayStatus{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrUnsupportedOperation, Message: "Unsupported relay action"}
	}
	if _, err := a.RelayManager.Controller.Action(ctx, "stop"); err != nil {
		return bridgeprotocol.RelayStatus{}, err
	}
	return a.RelayManager.Status(ctx, input.Port, true)
}

func (a *CompositeAdapter) RelayCapability(ctx context.Context) (bridgeprotocol.RelayCapabilityResponse, error) {
	return a.RelayManager.RelayCapability(ctx)
}

func (a *CompositeAdapter) ReloadRelayGovernance(ctx context.Context, input bridgeprotocol.RelayReloadRequest) (bridgeprotocol.RelayReloadResponse, error) {
	return a.RelayManager.ReloadRelayGovernance(ctx, input)
}

func (a *CompositeAdapter) ObserveRelayContracts(ctx context.Context) (bridgeprotocol.ContractObservationResponse, error) {
	return a.RelayManager.ObserveRelayContracts(ctx)
}

func (a *CompositeAdapter) ListRelayConfirmations(ctx context.Context) (bridgeprotocol.ConfirmationListResponse, error) {
	return a.RelayManager.ListRelayConfirmations(ctx)
}

func (a *CompositeAdapter) DecideRelayConfirmation(ctx context.Context, approve bool, input bridgeprotocol.ConfirmationDecisionRequest) (bridgeprotocol.ConfirmationDecisionResponse, error) {
	return a.RelayManager.DecideRelayConfirmation(ctx, approve, input)
}

func (a *CompositeAdapter) DrainRelayObservations(ctx context.Context, input bridgeprotocol.ObservationDrainRequest) (bridgeprotocol.ObservationDrainResponse, error) {
	return a.RelayManager.DrainRelayObservations(ctx, input)
}
