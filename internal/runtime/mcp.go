package runtime

import (
	"context"
	"errors"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
)

type SecretResolver func(context.Context, string) (string, error)

// ApplyMCP materializes one centrally desired profile into mcpm and then writes
// the runtime's single native anchor. If the anchor edit fails, the mcpm store
// is restored to its pre-task snapshot.
func ApplyMCP(ctx context.Context, paths Paths, dataDir string, request protocol.ApplyMCPPayload, resolve SecretResolver) (protocol.ApplyMCPResult, error) {
	if request.Runtime == domain.RuntimeHermes {
		return protocol.ApplyMCPResult{}, errors.New("Hermes is a read-only import source")
	}
	if request.MCPMProfile == "" {
		request.MCPMProfile = MCPMProfileForRuntime(request.Runtime)
	}
	result, mcpmResult, err := ApplyMCPM(ctx, paths.Home, dataDir, request.Runtime, request.MCPMProfile, request.Servers, request.Enabled, resolve)
	if err != nil {
		return protocol.ApplyMCPResult{}, err
	}
	anchor, err := ApplyRuntimeMCPAnchor(paths.Home, dataDir, request.Runtime, request.MCPMProfile, request.Enabled)
	if err != nil {
		if mcpmResult.Restore != nil {
			if restoreErr := mcpmResult.Restore(); restoreErr != nil {
				return protocol.ApplyMCPResult{}, errors.Join(err, restoreErr)
			}
		}
		return protocol.ApplyMCPResult{}, err
	}
	result.ActualHash = request.DesiredHash
	result.ActualEnabled = request.Enabled
	result.ConfigPath = anchor.ConfigPath
	if anchor.BackupPath != "" {
		result.BackupPaths = append(result.BackupPaths, anchor.BackupPath)
	}
	return result, nil
}
