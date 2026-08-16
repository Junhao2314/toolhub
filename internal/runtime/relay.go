package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

const (
	RelayUnitName          = "toolhub-mcpm-relay.service"
	RelayProfile           = "toolhub"
	RelayAnchor            = "toolhub-relay"
	MCPMExecutable         = "/usr/bin/mcpm"
	relayReadinessInterval = 250 * time.Millisecond
	relayReadinessTimeout  = 15 * time.Second
	relayContentHashField  = "toolhub_content_hash"
	relayIntegrityField    = "toolhub_runtime_integrity"
	mcpmContractMaxBytes   = 64 << 10
	mcpmContractTimeout    = 5 * time.Second
)

var requiredMCPMFeatures = []string{
	"profile-session-binding",
	"tool-filtering",
	"call-policy",
	"one-shot-confirmation",
	"payload-free-observations",
	"routing-hot-reload",
}

type RelayController interface {
	Action(context.Context, string) (string, error)
}

type SystemdRelayController struct{}

func (SystemdRelayController) Action(ctx context.Context, action string) (string, error) {
	allowed := map[string][]string{
		"status":     {"is-active", RelayUnitName},
		"is-enabled": {"is-enabled", RelayUnitName},
		"enable":     {"enable", RelayUnitName},
		"disable":    {"disable", RelayUnitName},
		"start":      {"enable", "--now", RelayUnitName},
		"start-unit": {"start", RelayUnitName},
		"stop":       {"disable", "--now", RelayUnitName},
		"stop-unit":  {"stop", RelayUnitName},
	}
	args, ok := allowed[action]
	if !ok {
		return "", &bridgeprotocol.APIError{Code: bridgeprotocol.ErrUnsupportedOperation, Message: "Unsupported relay action"}
	}
	output, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	state := strings.TrimSpace(string(output))
	if err != nil {
		return state, fmt.Errorf("relay systemd action failed")
	}
	return state, nil
}

type RelayManager struct {
	Controller        RelayController
	Admin             MCPMAdmin
	HTTPClient        *http.Client
	BackupRoot        string
	EnvironmentFile   string
	MCPMPath          string
	now               func() time.Time
	portAvailable     func(int) error
	scanRelayState    func(TargetPaths) (ScanResult, error)
	readinessInterval time.Duration
	readinessTimeout  time.Duration
}

func NewRelayManager(controller RelayController, backupRoot string) *RelayManager {
	return &RelayManager{
		Controller:        controller,
		Admin:             NewMCPMAdminClient(),
		HTTPClient:        &http.Client{Timeout: relayProbePerRequestTimeout},
		BackupRoot:        backupRoot,
		EnvironmentFile:   "/var/lib/toolhub-bridge/mcpm-relay.env",
		MCPMPath:          MCPMExecutable,
		now:               time.Now,
		portAvailable:     ensureRelayPortAvailable,
		readinessInterval: relayReadinessInterval,
		readinessTimeout:  relayReadinessTimeout,
	}
}

func (r *RelayManager) ValidateMCPM(ctx context.Context) (string, error) {
	path := strings.TrimSpace(r.MCPMPath)
	if path == "" {
		path = MCPMExecutable
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", &bridgeprotocol.APIError{Code: bridgeprotocol.ErrMCPMMissing, Message: "mcpm is not installed"}
	}
	contractCtx, cancel := context.WithTimeout(ctx, mcpmContractTimeout)
	defer cancel()
	command := exec.CommandContext(contractCtx, path, "toolhub", "contract", "--json")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", incompatibleMCPMError()
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return "", incompatibleMCPMError()
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, mcpmContractMaxBytes+1))
	waitErr := command.Wait()
	if readErr != nil || waitErr != nil || len(output) == 0 || len(output) > mcpmContractMaxBytes || output[len(output)-1] != '\n' || bytes.Count(output, []byte{'\n'}) != 1 {
		return "", incompatibleMCPMError()
	}
	var contract bridgeprotocol.RelayCapabilityResponse
	body := bytes.TrimSuffix(output, []byte{'\n'})
	if err := bridgeprotocol.ValidateGovernanceBody(body); err != nil || decodeStrictAdminJSON(body, &contract) != nil {
		return "", incompatibleMCPMError()
	}
	if err := validateMCPMCapability(contract); err != nil {
		return "", err
	}
	return contract.RuntimeVersion, nil
}

func validateMCPMCapability(contract bridgeprotocol.RelayCapabilityResponse) error {
	if contract.AdminProtocolVersion != 1 || contract.Runtime != "mcpm" || strings.TrimSpace(contract.RuntimeVersion) == "" || len(contract.RuntimeVersion) > 64 || !containsInt(contract.RoutingSchemaVersions, 1) {
		return incompatibleMCPMError()
	}
	features := make(map[string]struct{}, len(contract.Features))
	for _, feature := range contract.Features {
		if strings.TrimSpace(feature) == "" || len(feature) > 64 {
			return incompatibleMCPMError()
		}
		features[feature] = struct{}{}
	}
	for _, required := range requiredMCPMFeatures {
		if _, ok := features[required]; !ok {
			return incompatibleMCPMError()
		}
	}
	return nil
}

func containsInt(values []int, expected int) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func incompatibleMCPMError() error {
	return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrMCPMIncompatible, Message: "mcpm ToolHub capability contract is incompatible"}
}

func ScanSharedRelay(paths TargetPaths) ([]bridgeprotocol.InventoryMember, error) {
	if err := guardRelayPaths(paths); err != nil {
		return nil, err
	}
	registry, err := readJSONMap(paths.MCPMRegistry)
	if err != nil {
		return nil, err
	}
	members := []bridgeprotocol.InventoryMember{}
	for name, raw := range registry {
		entry, ok := raw.(map[string]any)
		if !ok || !stringSliceContains(entry["profile_tags"], RelayProfile) {
			continue
		}
		actualHash := relayEntryHash(entry)
		contentHash, _ := entry[relayContentHashField].(string)
		integrity, _ := entry[relayIntegrityField].(string)
		if !bridgeprotocol.IsSHA256(contentHash) || integrity != actualHash {
			contentHash = actualHash
		}
		members = append(members, bridgeprotocol.InventoryMember{ID: "mcp:" + name, Kind: "mcp", Name: name, ContentHash: contentHash, Scope: "user"})
	}
	anchorMembers, err := scanRelayAnchors(paths)
	if err != nil {
		return nil, err
	}
	return append(members, anchorMembers...), nil
}

func (r *RelayManager) Apply(ctx context.Context, user ManagedUser, request bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	paths, err := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	current, err := r.scanRelay(paths)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	if current.Revision != request.ExpectedRevision {
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Relay configuration changed after preflight"}
	}
	preserveUnmanaged := request.OperationKind == "reconcile"
	runtimeMatches, err := relayRuntimeConfigurationMatches(paths, r.EnvironmentFile, request.Manifest, request.SecretValues, preserveUnmanaged)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	routingMatches, err := relayRoutingMatches(paths, request.Manifest)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	matches := runtimeMatches && routingMatches
	if preserveUnmanaged && matches {
		if request.IntentionalPaused {
			status, repaired := r.EnsurePaused(ctx, request.Manifest.RelayPort, &request.Manifest)
			health := relayProjectedHealth(status)
			return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: health, TargetRevision: current.Revision, Repaired: repaired, Relay: &status, Error: relayStatusError(status)}, nil
		}
		var status bridgeprotocol.RelayStatus
		if request.FullRelayProbe {
			status, _ = r.Status(ctx, request.Manifest.RelayPort, false, request.Manifest)
		} else {
			status, _ = r.Status(ctx, request.Manifest.RelayPort, false)
		}
		if status.Healthy {
			return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: current.Revision, Repaired: false, Relay: &status}, nil
		}
		relayStatus := r.RestartAndCheck(ctx, request.Manifest.RelayPort, &request.Manifest)
		if !relayStatus.Healthy {
			return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthBlocked, TargetRevision: current.Revision, Repaired: true, Relay: &relayStatus, Error: relayStatusError(relayStatus)}, nil
		}
		return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: current.Revision, Repaired: true, Relay: &relayStatus}, nil
	}
	if !request.IntentionalPaused {
		if _, err := r.ValidateMCPM(ctx); err != nil {
			return bridgeprotocol.TargetResult{}, err
		}
	}
	var process relayProcessSnapshot
	if !runtimeMatches || !routingMatches {
		process = r.captureRelayProcess(ctx)
	}
	backup, err := r.backup(user, request.Target, paths, request.OperationID, current.Revision)
	if err != nil {
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrBackup, Message: "Could not back up relay configuration"}
	}
	rollback, err := backupRelayFiles(paths, r.EnvironmentFile)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	if !runtimeMatches {
		if err := writeRelayConfiguration(paths, request.Manifest, request.SecretValues, user, preserveUnmanaged); err != nil {
			_ = restoreRelayFiles(rollback)
			return bridgeprotocol.TargetResult{}, err
		}
		if err := writeRelayEnvironment(r.EnvironmentFile, request.Manifest.RelayPort); err != nil {
			_ = restoreRelayFiles(rollback)
			return bridgeprotocol.TargetResult{}, err
		}
	}
	if !routingMatches {
		if err := writeRelayRouting(paths, request.Manifest, user); err != nil {
			_ = restoreRelayFiles(rollback)
			return bridgeprotocol.TargetResult{}, err
		}
	}
	if matches, err := relayConfigurationMatches(paths, r.EnvironmentFile, request.Manifest, request.SecretValues, preserveUnmanaged); err != nil || !matches {
		_ = restoreRelayFiles(rollback)
		if err != nil {
			return bridgeprotocol.TargetResult{}, err
		}
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrAtomicWrite, Message: "Relay configuration integrity validation failed"}
	}
	if preserveUnmanaged && request.IntentionalPaused {
		after, scanErr := r.scanRelay(paths)
		if scanErr != nil {
			_ = restoreRelayFiles(rollback)
			return bridgeprotocol.TargetResult{}, scanErr
		}
		relayStatus, _ := r.EnsurePaused(ctx, request.Manifest.RelayPort, &request.Manifest)
		health := relayProjectedHealth(relayStatus)
		return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: health, TargetRevision: after.Revision, BackupID: backup.Backup.ID, Manifest: &request.Manifest, Repaired: true, Relay: &relayStatus, Error: relayStatusError(relayStatus)}, nil
	}
	after, err := r.scanRelay(paths)
	if err != nil {
		_ = restoreRelayFiles(rollback)
		return bridgeprotocol.TargetResult{}, err
	}
	if runtimeMatches && !routingMatches && rollback[paths.RoutingFile].Exists {
		if err := r.reloadRoutingAndCheck(ctx, request.Manifest); err != nil {
			rollbackErr := restoreRelayFiles(rollback)
			recoveryErr := r.recoverPreviousRouting(ctx, rollback[paths.RoutingFile])
			if rollbackErr != nil || recoveryErr != nil {
				return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "Relay routing reload failed and the previous bundle could not be restored", Retryable: true}
			}
			return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "Relay routing reload failed; the previous bundle was restored", Retryable: true}
		}
		relayStatus, _ := r.Status(ctx, request.Manifest.RelayPort, false)
		if !relayStatus.Healthy {
			_ = restoreRelayFiles(rollback)
			_ = r.recoverPreviousRouting(ctx, rollback[paths.RoutingFile])
			return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "Relay routing reload did not preserve runtime health", Retryable: true}
		}
		return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: after.Revision, BackupID: backup.Backup.ID, Manifest: &request.Manifest, Repaired: true, Relay: &relayStatus}, nil
	}
	relayStatus := r.RestartAndCheck(ctx, request.Manifest.RelayPort, &request.Manifest)
	if !relayStatus.Healthy {
		oldPort := relayPortFromBackup(rollback[r.EnvironmentFile], request.Manifest.RelayPort)
		rollbackErr := restoreRelayFiles(rollback)
		recoveryErr := r.restoreRelayProcess(ctx, process, oldPort)
		if rollbackErr != nil || recoveryErr != nil {
			return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "Relay update failed and the previous runtime could not be restored", Retryable: true}
		}
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: relayStatus.ErrorCode, Message: "Relay update failed; the previous runtime was restored", Retryable: true}
	}
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: bridgeprotocol.HealthHealthy, TargetRevision: after.Revision, BackupID: backup.Backup.ID, Manifest: &request.Manifest, Repaired: preserveUnmanaged || !matches, Relay: &relayStatus}, nil
}

func (r *RelayManager) RestartAndCheck(ctx context.Context, port int, manifest *bridgeprotocol.DesiredManifest) bridgeprotocol.RelayStatus {
	_, contractErr := r.ValidateMCPM(ctx)
	if err := r.RestartFixed(ctx, port); err != nil {
		if contractErr != nil {
			return r.relayBlockedFromError(ctx, port, contractErr, manifest)
		}
		var apiErr *bridgeprotocol.APIError
		if errors.As(err, &apiErr) {
			return r.relayBlockedStatus(ctx, port, apiErr.Code, apiErr.Message, manifest)
		}
		return r.relayBlockedStatus(ctx, port, bridgeprotocol.ErrRelayUnhealthy, "relay could not be restarted", manifest)
	}
	if contractErr != nil {
		return r.relayBlockedFromError(ctx, port, contractErr, manifest)
	}
	if manifest == nil {
		status, _ := r.Status(ctx, port, false)
		return status
	}
	status, _ := r.waitUntilHealthy(ctx, port, *manifest)
	return status
}

func (r *RelayManager) RestartFixed(ctx context.Context, port int) error {
	if _, err := r.Controller.Action(ctx, "enable"); err != nil {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "relay unit could not be enabled"}
	}
	if _, err := r.Controller.Action(ctx, "stop-unit"); err != nil {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "relay unit could not be stopped before restart"}
	}
	if err := r.waitUntilPortAvailable(ctx, port); err != nil {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayPortConflict, Message: "fixed relay port did not become available after stop"}
	}
	if _, err := r.Controller.Action(ctx, "start-unit"); err != nil {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "relay unit could not be started"}
	}
	return nil
}

func (r *RelayManager) StartAndCheck(ctx context.Context, port int, manifest *bridgeprotocol.DesiredManifest) bridgeprotocol.RelayStatus {
	_, contractErr := r.ValidateMCPM(ctx)
	state, _ := r.Controller.Action(ctx, "status")
	if strings.TrimSpace(state) != "active" {
		if err := r.checkPortAvailable(port); err != nil {
			return r.relayBlockedStatus(ctx, port, bridgeprotocol.ErrRelayPortConflict, "fixed relay port is already in use", manifest)
		}
	}
	if _, err := r.Controller.Action(ctx, "start"); err != nil {
		if contractErr != nil {
			return r.relayBlockedFromError(ctx, port, contractErr, manifest)
		}
		return r.relayBlockedStatus(ctx, port, bridgeprotocol.ErrRelayUnhealthy, "relay unit could not be enabled and started", manifest)
	}
	if contractErr != nil {
		return r.relayBlockedFromError(ctx, port, contractErr, manifest)
	}
	if manifest == nil {
		status, _ := r.Status(ctx, port, false)
		return status
	}
	status, _ := r.waitUntilHealthy(ctx, port, *manifest)
	return status
}

func (r *RelayManager) waitUntilHealthy(ctx context.Context, port int, manifest bridgeprotocol.DesiredManifest) (bridgeprotocol.RelayStatus, error) {
	interval := r.readinessInterval
	if interval <= 0 {
		interval = relayReadinessInterval
	}
	timeout := r.readinessTimeout
	if timeout <= 0 {
		timeout = relayReadinessTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		status, _ := r.Status(waitCtx, port, false)
		if status.Healthy {
			cancel()
			return r.Status(ctx, port, false, manifest)
		}
		select {
		case <-waitCtx.Done():
			cancel()
			if status.ErrorCode == "" {
				status.ErrorCode = bridgeprotocol.ErrRelayUnhealthy
				status.ErrorReason = "Relay failed its health check before the readiness timeout"
			}
			return status, waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func (r *RelayManager) waitUntilPortAvailable(ctx context.Context, port int) error {
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := r.checkPortAvailable(port); err == nil {
			return nil
		}
		select {
		case <-deadline.Done():
			return deadline.Err()
		case <-ticker.C:
		}
	}
}

func (r *RelayManager) checkPortAvailable(port int) error {
	if r.portAvailable == nil {
		return ensureRelayPortAvailable(port)
	}
	return r.portAvailable(port)
}

func (r *RelayManager) scanRelay(paths TargetPaths) (ScanResult, error) {
	if r.scanRelayState != nil {
		return r.scanRelayState(paths)
	}
	return (&Manager{}).scanRelay(paths)
}

func (r *RelayManager) EnsurePaused(ctx context.Context, port int, manifest *bridgeprotocol.DesiredManifest) (bridgeprotocol.RelayStatus, bool) {
	status, _ := r.Status(ctx, port, true)
	if status.Healthy {
		return status, false
	}
	if _, err := r.Controller.Action(ctx, "stop"); err != nil {
		blocked := r.relayBlockedStatus(ctx, port, bridgeprotocol.ErrRelayUnhealthy, "relay unit could not be disabled and stopped", manifest)
		blocked.IntentionalPaused = true
		return blocked, false
	}
	status, _ = r.Status(ctx, port, true)
	return status, true
}

func (r *RelayManager) Restore(ctx context.Context, user ManagedUser, request bridgeprotocol.CommitRequest) (bridgeprotocol.TargetResult, error) {
	paths, err := PathsFor(user, bridgeprotocol.RuntimeSharedRelay)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	current, err := r.scanRelay(paths)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	if request.ExpectedRevision != "" && current.Revision != request.ExpectedRevision {
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Relay changed before restore"}
	}
	process := r.captureRelayProcess(ctx)
	recovery, err := r.backup(user, request.Target, paths, request.OperationID, current.Revision)
	if err != nil {
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrBackup, Message: "Could not back up relay before restore"}
	}
	selected := filepath.Join(r.BackupRoot, request.Target.ID, request.BackupID)
	if err := ensureWithin(filepath.Join(r.BackupRoot, request.Target.ID), selected); err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	rollback, err := backupRelayFiles(paths, r.EnvironmentFile)
	if err != nil {
		return bridgeprotocol.TargetResult{}, err
	}
	if err := restoreRelayBackup(selected, paths, r.EnvironmentFile, user); err != nil {
		_ = restoreRelayFiles(rollback)
		return bridgeprotocol.TargetResult{}, err
	}
	matches, matchErr := relayConfigurationMatches(paths, r.EnvironmentFile, request.Manifest, request.SecretValues, true)
	if matchErr != nil || !matches {
		_ = restoreRelayFiles(rollback)
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrBackup, Message: "Restored relay configuration does not match the pinned backup manifest"}
	}
	after, err := r.scanRelay(paths)
	if err != nil {
		oldPort := relayPortFromBackup(rollback[r.EnvironmentFile], request.Manifest.RelayPort)
		rollbackErr := restoreRelayFiles(rollback)
		recoveryErr := r.restoreRelayProcess(ctx, process, oldPort)
		if rollbackErr != nil || recoveryErr != nil {
			return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "Relay restore scan failed and the previous runtime could not be recovered", Retryable: true}
		}
		return bridgeprotocol.TargetResult{}, err
	}
	var relayStatus bridgeprotocol.RelayStatus
	if request.IntentionalPaused {
		relayStatus, _ = r.EnsurePaused(ctx, request.Manifest.RelayPort, &request.Manifest)
	} else {
		relayStatus = r.RestartAndCheck(ctx, request.Manifest.RelayPort, &request.Manifest)
	}
	if !relayStatus.Healthy {
		oldPort := relayPortFromBackup(rollback[r.EnvironmentFile], request.Manifest.RelayPort)
		rollbackErr := restoreRelayFiles(rollback)
		recoveryErr := r.restoreRelayProcess(ctx, process, oldPort)
		if rollbackErr != nil || recoveryErr != nil {
			return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayUnhealthy, Message: "Relay restore failed and the previous runtime could not be recovered", Retryable: true}
		}
		code := relayStatus.ErrorCode
		if code == "" {
			code = bridgeprotocol.ErrRelayUnhealthy
		}
		return bridgeprotocol.TargetResult{}, &bridgeprotocol.APIError{Code: code, Message: "Relay restore failed; the previous runtime was recovered", Retryable: true}
	}
	health := relayProjectedHealth(relayStatus)
	return bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationSucceeded, Health: health, TargetRevision: after.Revision, BackupID: recovery.Backup.ID, Manifest: &request.Manifest, Relay: &relayStatus, Error: relayStatusError(relayStatus)}, nil
}

func (r *RelayManager) Status(ctx context.Context, port int, intentionalPaused bool, manifests ...bridgeprotocol.DesiredManifest) (bridgeprotocol.RelayStatus, error) {
	state, err := r.Controller.Action(ctx, "status")
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	enabledState, _ := r.Controller.Action(ctx, "is-enabled")
	status := bridgeprotocol.RelayStatus{State: state, Endpoint: endpoint, FixedPort: port, IntentionalPaused: intentionalPaused, SystemdEnabled: strings.TrimSpace(enabledState) == "enabled"}
	if intentionalPaused {
		status.Healthy = !relayStateRunning(state) && !status.SystemdEnabled
		if !status.Healthy {
			status.ErrorCode = bridgeprotocol.ErrRelayUnhealthy
			status.ErrorReason = "intentional relay pause is not enforced"
		}
		return status, nil
	}
	if err != nil || state != "active" {
		status.ErrorCode = bridgeprotocol.ErrRelayUnhealthy
		status.ErrorReason = "relay unit is not active"
		if len(manifests) > 0 {
			status.Contract = "unavailable"
			status.MemberStatuses = unavailableRelayMembers(manifests[0], status.ErrorCode, status.ErrorReason, r.relayNow().UTC())
		}
		return status, nil
	}
	if len(manifests) > 0 {
		probed, probeErr := r.ProbeMembers(ctx, port, manifests[0])
		probed.State = status.State
		probed.SystemdEnabled = status.SystemdEnabled
		probed.IntentionalPaused = status.IntentionalPaused
		if probeErr != nil && probed.ErrorCode == "" {
			probed.ErrorCode = bridgeprotocol.ErrRelayUnhealthy
			probed.ErrorReason = safeRelayReason(probeErr)
		}
		return probed, nil
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err == nil {
		_ = response.Body.Close()
		status.Healthy = response.StatusCode >= 200 && response.StatusCode < 500
	}
	if !status.Healthy {
		status.ErrorCode = bridgeprotocol.ErrRelayUnhealthy
		status.ErrorReason = "relay endpoint did not pass HTTP health"
	}
	return status, nil
}

func relayStateRunning(state string) bool {
	switch strings.TrimSpace(state) {
	case "active", "activating", "reloading", "deactivating":
		return true
	default:
		return false
	}
}

type relayProcessSnapshot struct {
	Running bool
	Enabled bool
}

func (r *RelayManager) captureRelayProcess(ctx context.Context) relayProcessSnapshot {
	state, _ := r.Controller.Action(ctx, "status")
	enabled, _ := r.Controller.Action(ctx, "is-enabled")
	return relayProcessSnapshot{Running: relayStateRunning(state), Enabled: strings.TrimSpace(enabled) == "enabled"}
}

func (r *RelayManager) reloadRoutingAndCheck(ctx context.Context, manifest bridgeprotocol.DesiredManifest) error {
	if manifest.RelayGovernance == nil {
		return errors.New("relay routing governance is required for hot reload")
	}
	status, err := r.adminClient().ReloadRouting(ctx)
	if err != nil {
		return err
	}
	if !relayAdminStatusMatches(status, *manifest.RelayGovernance) {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Relay loaded a different routing bundle"}
	}
	capability, err := r.adminClient().Capability(ctx)
	if err != nil {
		return err
	}
	if err := validateMCPMCapability(capability); err != nil {
		return err
	}
	observed, err := r.adminClient().Status(ctx)
	if err != nil {
		return err
	}
	if !relayAdminStatusMatches(observed, *manifest.RelayGovernance) {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRevisionConflict, Message: "Relay status does not match the requested routing bundle"}
	}
	return nil
}

func relayAdminStatusMatches(status bridgeprotocol.RelayAdminStatus, governance bridgeprotocol.RelayGovernanceManifest) bool {
	return status.RelayConfigurationRevisionID == governance.RelayConfigurationRevisionID && status.RoutingBundleHash == governance.RoutingHash
}

func (r *RelayManager) recoverPreviousRouting(ctx context.Context, previous relayFileState) error {
	if !previous.Exists {
		return nil
	}
	var bundle bridgeprotocol.RoutingBundle
	if err := bridgeprotocol.DecodeGovernanceBody(previous.Body, &bundle); err != nil {
		return err
	}
	_, hash, err := bundle.Canonical()
	if err != nil {
		return err
	}
	status, err := r.adminClient().ReloadRouting(ctx)
	if err != nil {
		return err
	}
	if status.RelayConfigurationRevisionID != bundle.RelayConfigurationRevisionID || status.RoutingBundleHash != hash {
		return errors.New("previous relay routing bundle was not restored")
	}
	return nil
}

func relayPortFromBackup(environment relayFileState, fallback int) int {
	if !environment.Exists {
		return fallback
	}
	value := strings.TrimSpace(string(environment.Body))
	port, err := strconv.Atoi(strings.TrimPrefix(value, "TOOLHUB_RELAY_PORT="))
	if err != nil || port < 1 || port > 65535 {
		return fallback
	}
	return port
}

func (r *RelayManager) restoreRelayProcess(ctx context.Context, previous relayProcessSnapshot, port int) error {
	if previous.Running {
		if err := r.RestartFixed(ctx, port); err != nil {
			return err
		}
		status, _ := r.Status(ctx, port, false)
		if !status.Healthy {
			return errors.New("previous relay process did not recover")
		}
		if !previous.Enabled {
			if _, err := r.Controller.Action(ctx, "disable"); err != nil {
				return err
			}
		}
		return nil
	}
	if !previous.Enabled {
		_, err := r.Controller.Action(ctx, "stop")
		return err
	}
	_, err := r.Controller.Action(ctx, "stop-unit")
	return err
}

func relayProjectedHealth(status bridgeprotocol.RelayStatus) string {
	if status.Healthy {
		return bridgeprotocol.HealthHealthy
	}
	return bridgeprotocol.HealthBlocked
}

func (r *RelayManager) relayBlockedFromError(ctx context.Context, port int, err error, manifest *bridgeprotocol.DesiredManifest) bridgeprotocol.RelayStatus {
	code := relayProbeErrorCode(err, bridgeprotocol.ErrRelayUnhealthy)
	reason := relayProbeSafeMessage(err, "relay runtime contract could not be verified")
	return r.relayBlockedStatus(ctx, port, code, reason, manifest)
}

func (r *RelayManager) relayBlockedStatus(ctx context.Context, port int, code, reason string, manifest *bridgeprotocol.DesiredManifest) bridgeprotocol.RelayStatus {
	enabledState, _ := r.Controller.Action(ctx, "is-enabled")
	status := bridgeprotocol.RelayStatus{State: "blocked", Endpoint: fmt.Sprintf("http://127.0.0.1:%d/mcp", port), FixedPort: port, SystemdEnabled: strings.TrimSpace(enabledState) == "enabled", Contract: "unavailable", ErrorCode: code, ErrorReason: safeRelayReason(errors.New(reason))}
	if manifest != nil {
		status.MemberStatuses = unavailableRelayMembers(*manifest, code, status.ErrorReason, r.relayNow().UTC())
	}
	return status
}

func relayStatusError(status bridgeprotocol.RelayStatus) *bridgeprotocol.APIError {
	if status.Healthy || status.ErrorCode == "" {
		return nil
	}
	message := status.ErrorReason
	if message == "" {
		message = "Relay runtime is blocked"
	}
	return &bridgeprotocol.APIError{Code: status.ErrorCode, Message: message, Retryable: true}
}

func ensureRelayPortAvailable(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("relay port is invalid")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrRelayPortConflict, Message: "Configured relay port is already in use"}
	}
	return listener.Close()
}

type relayFileState struct {
	Body   []byte
	Exists bool
	Mode   os.FileMode
	UID    int
	GID    int
}

type relayFileBackup map[string]relayFileState

func backupRelayFiles(paths TargetPaths, extra ...string) (relayFileBackup, error) {
	if err := guardRelayPaths(paths); err != nil {
		return nil, err
	}
	result := relayFileBackup{}
	files := append([]string{paths.MCPMRegistry, paths.ClaudeConfig, paths.CodexConfig, paths.HermesConfig, paths.RoutingFile}, extra...)
	for _, path := range files {
		if path == "" {
			continue
		}
		body, err := readSafeConfig(path)
		if errors.Is(err, os.ErrNotExist) {
			result[path] = relayFileState{}
			continue
		}
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		state := relayFileState{Body: body, Exists: true, Mode: info.Mode().Perm()}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			state.UID, state.GID = int(stat.Uid), int(stat.Gid)
		}
		result[path] = state
	}
	return result, nil
}

type relayBackupState struct {
	Version           int                                `json:"version"`
	RegistryExists    bool                               `json:"registryExists"`
	ClaudeExists      bool                               `json:"claudeExists"`
	CodexExists       bool                               `json:"codexExists"`
	HermesExists      bool                               `json:"hermesExists"`
	EnvironmentExists bool                               `json:"environmentExists"`
	Files             map[string]relayBackupFileMetadata `json:"files,omitempty"`
}

type relayBackupFileMetadata struct {
	Exists bool   `json:"exists"`
	Mode   uint32 `json:"mode,omitempty"`
	UID    int    `json:"uid,omitempty"`
	GID    int    `json:"gid,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

func (r *RelayManager) backup(user ManagedUser, target bridgeprotocol.Target, paths TargetPaths, operationID, revision string) (backupRecord, error) {
	if strings.TrimSpace(r.BackupRoot) == "" {
		return backupRecord{}, errors.New("relay backup root is required")
	}
	id := uuid.NewString()
	destination := filepath.Join(r.BackupRoot, target.ID, id)
	if err := ensureWithin(r.BackupRoot, destination); err != nil {
		return backupRecord{}, err
	}
	if err := guardRelayPaths(paths); err != nil {
		return backupRecord{}, err
	}
	if err := rejectSymlinkParent(destination); err != nil {
		return backupRecord{}, err
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		return backupRecord{}, err
	}
	state := relayBackupState{Version: 3, Files: map[string]relayBackupFileMetadata{}}
	items := []struct {
		path string
		name string
		has  *bool
	}{{paths.MCPMRegistry, "registry", &state.RegistryExists}, {paths.ClaudeConfig, "claude", &state.ClaudeExists}, {paths.CodexConfig, "codex", &state.CodexExists}, {paths.HermesConfig, "hermes", &state.HermesExists}, {paths.RoutingFile, "routing", nil}, {r.EnvironmentFile, "environment", &state.EnvironmentExists}}
	for _, item := range items {
		body, err := readSafeConfig(item.path)
		if errors.Is(err, os.ErrNotExist) {
			state.Files[item.name] = relayBackupFileMetadata{}
			continue
		}
		if err != nil {
			return backupRecord{}, err
		}
		if item.has != nil {
			*item.has = true
		}
		info, err := os.Lstat(item.path)
		if err != nil {
			return backupRecord{}, err
		}
		sum := sha256.Sum256(body)
		metadata := relayBackupFileMetadata{Exists: true, Mode: uint32(info.Mode().Perm()), SHA256: hex.EncodeToString(sum[:])}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			metadata.UID, metadata.GID = int(stat.Uid), int(stat.Gid)
		}
		state.Files[item.name] = metadata
		if err := atomicWrite(filepath.Join(destination, item.name), body, 0600); err != nil {
			return backupRecord{}, err
		}
	}
	stateBody, _ := json.Marshal(state)
	if err := atomicWrite(filepath.Join(destination, "state.json"), stateBody, 0600); err != nil {
		return backupRecord{}, err
	}
	backup := bridgeprotocol.Backup{ID: id, TargetID: target.ID, NodeKind: target.NodeKind, Runtime: target.Runtime, SourceOperationID: operationID, Revision: revision, CreatedAt: r.now().UTC()}
	_ = user
	return backupRecord{Backup: backup, Path: destination}, nil
}

func restoreRelayBackup(source string, paths TargetPaths, environmentFile string, user ManagedUser) error {
	if err := guardRelayPaths(paths); err != nil {
		return err
	}
	stateBody, err := readSafeConfig(filepath.Join(source, "state.json"))
	if err != nil {
		return errors.New("relay backup is missing or unsafe")
	}
	var state relayBackupState
	if err := json.Unmarshal(stateBody, &state); err != nil {
		return errors.New("relay backup state is invalid")
	}
	if state.Version >= 3 {
		items := map[string]string{"registry": paths.MCPMRegistry, "claude": paths.ClaudeConfig, "codex": paths.CodexConfig, "hermes": paths.HermesConfig, "routing": paths.RoutingFile, "environment": environmentFile}
		for name, destination := range items {
			metadata, ok := state.Files[name]
			if !ok {
				return errors.New("relay backup state is incomplete")
			}
			if !metadata.Exists {
				if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				continue
			}
			body, err := readSafeConfig(filepath.Join(source, name))
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			if hex.EncodeToString(sum[:]) != metadata.SHA256 || metadata.Mode == 0 {
				return errors.New("relay backup content hash or mode is invalid")
			}
			if err := atomicWrite(destination, body, os.FileMode(metadata.Mode)); err != nil {
				return err
			}
			if os.Geteuid() == 0 {
				if err := os.Chown(destination, metadata.UID, metadata.GID); err != nil {
					return err
				}
			}
		}
		return nil
	}
	items := []struct {
		destination string
		name        string
		exists      bool
		chown       bool
	}{{paths.MCPMRegistry, "registry", state.RegistryExists, true}, {paths.ClaudeConfig, "claude", state.ClaudeExists, true}, {paths.CodexConfig, "codex", state.CodexExists, true}, {paths.HermesConfig, "hermes", state.HermesExists, true}, {environmentFile, "environment", state.EnvironmentExists, false}}
	if state.Version < 2 {
		items = []struct {
			destination string
			name        string
			exists      bool
			chown       bool
		}{{paths.MCPMRegistry, "registry", state.RegistryExists, true}, {paths.ClaudeConfig, "claude", state.ClaudeExists, true}, {paths.CodexConfig, "codex", state.CodexExists, true}, {environmentFile, "environment", state.EnvironmentExists, false}}
	}
	for _, item := range items {
		if !item.exists {
			if err := os.Remove(item.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		body, err := readSafeConfig(filepath.Join(source, item.name))
		if err != nil {
			return err
		}
		if err := atomicWrite(item.destination, body, 0600); err != nil {
			return err
		}
		if item.chown && os.Geteuid() == 0 {
			if err := os.Chown(item.destination, user.UID, user.GID); err != nil {
				return err
			}
		}
	}
	return nil
}

func restoreRelayFiles(backup relayFileBackup) error {
	for path, state := range backup {
		if !state.Exists {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if err := atomicWrite(path, state.Body, state.Mode); err != nil {
			return err
		}
		if os.Geteuid() == 0 {
			if err := os.Chown(path, state.UID, state.GID); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeRelayConfiguration(paths TargetPaths, manifest bridgeprotocol.DesiredManifest, secrets map[string]string, user ManagedUser, preserveUnmanaged bool) error {
	if err := guardRelayPaths(paths); err != nil {
		return err
	}
	registry, err := readJSONMap(paths.MCPMRegistry)
	if err != nil {
		return err
	}
	desired := map[string]bool{}
	for _, server := range manifest.MCPServers {
		desired[server.Name] = true
		entry := expectedRelayEntry(server, secrets)
		registry[server.Name] = entry
	}
	for name, raw := range registry {
		entry, ok := raw.(map[string]any)
		if preserveUnmanaged || !ok || !stringSliceContains(entry["profile_tags"], RelayProfile) || desired[name] {
			continue
		}
		delete(registry, name)
	}
	registryBody, _ := json.MarshalIndent(registry, "", "  ")
	if err := atomicWrite(paths.MCPMRegistry, append(registryBody, '\n'), 0600); err != nil {
		return err
	}
	if err := writeClaudeRelayAnchor(paths.ClaudeConfig, manifest.RelayPort); err != nil {
		return err
	}
	if err := writeCodexRelayAnchor(paths.CodexConfig, manifest.RelayPort); err != nil {
		return err
	}
	if err := writeHermesRelayAnchor(paths.HermesConfig, manifest.RelayPort, preserveUnmanaged); err != nil {
		return err
	}
	for _, path := range []string{paths.MCPMRegistry, paths.ClaudeConfig, paths.CodexConfig, paths.HermesConfig} {
		if err := os.Chown(path, user.UID, user.GID); err != nil && os.Geteuid() == 0 {
			return err
		}
	}
	return nil
}

func writeRelayRouting(paths TargetPaths, manifest bridgeprotocol.DesiredManifest, user ManagedUser) error {
	if manifest.SchemaVersion < bridgeprotocol.ManifestSchemaVersionV2 || manifest.RelayGovernance == nil {
		return nil
	}
	if err := manifest.RelayGovernance.Validate(); err != nil {
		return err
	}
	if err := atomicWrite(paths.RoutingFile, manifest.RelayGovernance.RoutingBundle, 0600); err != nil {
		return err
	}
	if err := os.Chown(paths.RoutingFile, user.UID, user.GID); err != nil && os.Geteuid() == 0 {
		return err
	}
	return nil
}

func expectedRelayEntry(server bridgeprotocol.MCPMember, secrets map[string]string) map[string]any {
	entry := map[string]any{"name": server.Name, "profile_tags": []string{RelayProfile}, "toolhub_member_id": server.MemberID, relayContentHashField: server.ContentHash}
	if server.Transport == "stdio" {
		entry["command"], entry["args"] = server.Command, server.Args
	} else {
		entry["url"], entry["transport"] = server.URL, server.Transport
	}
	if env := resolveSecretMap(server.EnvRefs, secrets); len(env) > 0 {
		entry["env"] = env
	}
	if headers := resolveSecretMap(server.HeaderRefs, secrets); len(headers) > 0 {
		entry["headers"] = headers
	}
	entry[relayIntegrityField] = relayEntryHash(entry)
	return entry
}

func relayEntryHash(entry map[string]any) string {
	projection := make(map[string]any, len(entry))
	for key, value := range entry {
		if key != relayIntegrityField {
			projection[key] = value
		}
	}
	body, _ := json.Marshal(projection)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func relayRuntimeConfigurationMatches(paths TargetPaths, environmentFile string, manifest bridgeprotocol.DesiredManifest, secrets map[string]string, preserveUnmanaged bool) (bool, error) {
	registry, err := readJSONMap(paths.MCPMRegistry)
	if err != nil {
		return false, err
	}
	desired := map[string]bool{}
	for _, server := range manifest.MCPServers {
		desired[server.Name] = true
		if !jsonEqual(registry[server.Name], expectedRelayEntry(server, secrets)) {
			return false, nil
		}
	}
	if !preserveUnmanaged {
		for name, raw := range registry {
			entry, ok := raw.(map[string]any)
			if ok && stringSliceContains(entry["profile_tags"], RelayProfile) && !desired[name] {
				return false, nil
			}
		}
	}
	anchors, err := scanRelayAnchors(paths)
	if err != nil {
		return false, err
	}
	wantURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", manifest.RelayPort)
	if len(anchors) != 3 {
		return false, nil
	}
	claude, err := readJSONMap(paths.ClaudeConfig)
	if err != nil {
		return false, err
	}
	claudeServers, _ := claude["mcpServers"].(map[string]any)
	claudeAnchor, _ := claudeServers[RelayAnchor].(map[string]any)
	if claudeAnchor["type"] != "http" || claudeAnchor["url"] != wantURL {
		return false, nil
	}
	codex := map[string]any{}
	if body, err := readSafeConfig(paths.CodexConfig); err == nil {
		if toml.Unmarshal(body, &codex) != nil {
			return false, errors.New("Codex config TOML is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	codexServers, _ := codex["mcp_servers"].(map[string]any)
	codexAnchor, _ := codexServers[RelayAnchor].(map[string]any)
	if codexAnchor["url"] != wantURL {
		return false, nil
	}
	hermes, err := readYAMLMap(paths.HermesConfig)
	if err != nil {
		return false, err
	}
	hermesServers, err := yamlStringMap(hermes["mcp_servers"])
	if err != nil {
		return false, err
	}
	hermesAnchor, _ := hermesServers[RelayAnchor].(map[string]any)
	if hermesAnchor["url"] != wantURL || hermesAnchor["enabled"] != true {
		return false, nil
	}
	if !preserveUnmanaged {
		for name := range hermesServers {
			if name != RelayAnchor {
				return false, nil
			}
		}
	}
	environment, err := readSafeConfig(environmentFile)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return string(environment) == "TOOLHUB_RELAY_PORT="+strconv.Itoa(manifest.RelayPort)+"\n", nil
}

func relayRoutingMatches(paths TargetPaths, manifest bridgeprotocol.DesiredManifest) (bool, error) {
	if manifest.SchemaVersion < bridgeprotocol.ManifestSchemaVersionV2 || manifest.RelayGovernance == nil {
		return true, nil
	}
	body, err := readSafeConfig(paths.RoutingFile)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return bytes.Equal(body, manifest.RelayGovernance.RoutingBundle), nil
}

func relayConfigurationMatches(paths TargetPaths, environmentFile string, manifest bridgeprotocol.DesiredManifest, secrets map[string]string, preserveUnmanaged bool) (bool, error) {
	runtimeMatches, err := relayRuntimeConfigurationMatches(paths, environmentFile, manifest, secrets, preserveUnmanaged)
	if err != nil || !runtimeMatches {
		return runtimeMatches, err
	}
	return relayRoutingMatches(paths, manifest)
}

func jsonEqual(left, right any) bool {
	leftBody, leftErr := json.Marshal(left)
	rightBody, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBody) == string(rightBody)
}

func writeRelayEnvironment(path string, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("relay port is invalid")
	}
	return atomicWrite(path, []byte("TOOLHUB_RELAY_PORT="+strconv.Itoa(port)+"\n"), 0600)
}

func resolveSecretMap(refs, values map[string]string) map[string]string {
	result := map[string]string{}
	for key, id := range refs {
		result[key] = values[id]
	}
	return result
}

func writeClaudeRelayAnchor(path string, port int) error {
	root, err := readJSONMap(path)
	if err != nil {
		return err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if root["mcpServers"] != nil && !ok {
		return errors.New("Claude user mcpServers is not an object")
	}
	if !ok {
		servers = map[string]any{}
	}
	servers[RelayAnchor] = map[string]any{"type": "http", "url": fmt.Sprintf("http://127.0.0.1:%d/mcp", port)}
	root["mcpServers"] = servers
	body, _ := json.MarshalIndent(root, "", "  ")
	return atomicWrite(path, append(body, '\n'), 0600)
}

func writeCodexRelayAnchor(path string, port int) error {
	root := map[string]any{}
	if body, err := readSafeConfig(path); err == nil {
		if len(body) > 4<<20 || toml.Unmarshal(body, &root) != nil {
			return errors.New("Codex config TOML is invalid or too large")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	servers, ok := root["mcp_servers"].(map[string]any)
	if root["mcp_servers"] != nil && !ok {
		return errors.New("Codex mcp_servers is not a table")
	}
	if !ok {
		servers = map[string]any{}
	}
	servers[RelayAnchor] = map[string]any{"url": fmt.Sprintf("http://127.0.0.1:%d/mcp", port)}
	root["mcp_servers"] = servers
	body, err := toml.Marshal(root)
	if err != nil {
		return err
	}
	return atomicWrite(path, body, 0600)
}

func writeHermesRelayAnchor(path string, port int, preserveUnmanaged bool) error {
	root, err := readYAMLMap(path)
	if err != nil {
		return err
	}
	if root["mcp_servers"] != nil {
		if _, err := yamlStringMap(root["mcp_servers"]); err != nil {
			return err
		}
	}
	servers := map[string]any{}
	if preserveUnmanaged {
		current, err := yamlStringMap(root["mcp_servers"])
		if err != nil {
			return err
		}
		for name, value := range current {
			if name != RelayAnchor {
				servers[name] = value
			}
		}
	}
	servers[RelayAnchor] = map[string]any{"url": fmt.Sprintf("http://127.0.0.1:%d/mcp", port), "enabled": true}
	root["mcp_servers"] = servers
	body, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return atomicWrite(path, body, 0600)
}

func scanRelayAnchors(paths TargetPaths) ([]bridgeprotocol.InventoryMember, error) {
	result := []bridgeprotocol.InventoryMember{}
	claude, err := readJSONMap(paths.ClaudeConfig)
	if err != nil {
		return nil, err
	}
	if servers, ok := claude["mcpServers"].(map[string]any); ok {
		if anchor, ok := servers[RelayAnchor]; ok {
			body, _ := json.Marshal(anchor)
			sum := sha256.Sum256(body)
			result = append(result, bridgeprotocol.InventoryMember{ID: "anchor:claude", Kind: "anchor", Name: RelayAnchor, Scope: "user", ContentHash: hex.EncodeToString(sum[:])})
		}
	}
	root := map[string]any{}
	if body, err := readSafeConfig(paths.CodexConfig); err == nil {
		if toml.Unmarshal(body, &root) != nil {
			return nil, errors.New("Codex config TOML is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if servers, ok := root["mcp_servers"].(map[string]any); ok {
		if anchor, ok := servers[RelayAnchor]; ok {
			body, _ := json.Marshal(anchor)
			sum := sha256.Sum256(body)
			result = append(result, bridgeprotocol.InventoryMember{ID: "anchor:codex", Kind: "anchor", Name: RelayAnchor, Scope: "user", ContentHash: hex.EncodeToString(sum[:])})
		}
	}
	hermes, err := readYAMLMap(paths.HermesConfig)
	if err != nil {
		return nil, err
	}
	if servers, err := yamlStringMap(hermes["mcp_servers"]); err != nil {
		return nil, err
	} else if anchor, ok := servers[RelayAnchor]; ok {
		body, _ := json.Marshal(anchor)
		sum := sha256.Sum256(body)
		result = append(result, bridgeprotocol.InventoryMember{ID: "anchor:hermes", Kind: "anchor", Name: RelayAnchor, Scope: "user", ContentHash: hex.EncodeToString(sum[:])})
	}
	return result, nil
}

func readJSONMap(path string) (map[string]any, error) {
	body, err := readSafeConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	root := map[string]any{}
	if len(strings.TrimSpace(string(body))) == 0 {
		return root, nil
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	return root, nil
}

func readYAMLMap(path string) (map[string]any, error) {
	body, err := readSafeConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	root := map[string]any{}
	if len(strings.TrimSpace(string(body))) == 0 {
		return root, nil
	}
	if err := yaml.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	return root, nil
}

func yamlStringMap(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	if typed, ok := value.(map[string]any); ok {
		return typed, nil
	}
	return nil, errors.New("Hermes mcp_servers is not a mapping")
}

func readSafeConfig(path string) ([]byte, error) {
	if err := rejectSymlinkParent(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("configuration is not a regular file")
	}
	if info.Size() > 4<<20 {
		return nil, errors.New("configuration exceeds 4 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("configuration changed during safe open")
	}
	body, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 4<<20 {
		return nil, errors.New("configuration exceeds 4 MiB")
	}
	return body, nil
}

func atomicWrite(path string, body []byte, mode os.FileMode) error {
	if len(body) > 4<<20 {
		return errors.New("configuration exceeds 4 MiB")
	}
	if err := rejectSymlinkParent(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := rejectSymlinkParent(path); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".toolhub-write-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func guardRelayPaths(paths TargetPaths) error {
	for _, candidate := range []string{paths.MCPMRegistry, paths.ClaudeConfig, paths.CodexConfig, paths.HermesConfig, paths.RoutingFile} {
		if err := rejectSymlinkComponents(paths.Home, candidate); err != nil {
			return err
		}
	}
	return nil
}

func stringSliceContains(value any, want string) bool {
	switch typed := value.(type) {
	case []string:
		for _, item := range typed {
			if item == want {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && text == want {
				return true
			}
		}
	}
	return false
}
