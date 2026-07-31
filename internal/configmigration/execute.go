package configmigration

import (
	"context"
	"fmt"

	"github.com/Junhao2314/toolhub/internal/security"
	"github.com/Junhao2314/toolhub/internal/store"
)

func Execute(ctx context.Context, options Options) (Report, error) {
	legacyCipher, err := security.NewCipher(options.LegacyMasterKey)
	if err != nil {
		return Report{}, migrationError("invalid_options", "legacy master key is invalid", err)
	}
	snapshot, err := ReadSnapshot(ctx, options.LegacyDatabaseURL)
	if err != nil {
		return Report{}, err
	}
	prepared, err := Prepare(snapshot, legacyCipher)
	if err != nil {
		return Report{}, err
	}
	if options.ExpectedSourceFingerprint != "" && options.ExpectedSourceFingerprint != prepared.Import.SourceFingerprint {
		return Report{}, migrationError("source_fingerprint_mismatch", fmt.Sprintf("source fingerprint changed: expected %s, observed %s", options.ExpectedSourceFingerprint, prepared.Import.SourceFingerprint), nil)
	}
	if !options.Apply {
		return prepared.Report, nil
	}

	destinationCipher, err := security.NewCipher(options.Destination.MasterKey)
	if err != nil {
		return Report{}, migrationError("invalid_options", "destination master key is invalid", err)
	}
	st, err := store.Open(ctx, options.Destination.DatabaseURL, destinationCipher)
	if err != nil {
		return Report{}, migrationError("destination_connection_failed", "destination database connection failed", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return Report{}, migrationError("destination_initialize_failed", "destination generation-2 schema initialization failed", err)
	}
	needsBootstrap, err := st.ConfigImportDestinationNeedsBootstrap(ctx)
	if err != nil {
		return Report{}, migrationError("destination_initialize_failed", "destination bootstrap preflight failed", err)
	}
	if needsBootstrap {
		if _, err := st.BootstrapAccount(ctx, options.Destination.BootstrapUsername, options.Destination.BootstrapPassword); err != nil {
			return Report{}, migrationError("destination_initialize_failed", "destination singleton account bootstrap failed", err)
		}
		if err := st.BootstrapEnvironment(ctx, options.Destination.LocalNodeName, options.Destination.ManagedUsername, options.Destination.Timezone, options.Destination.RelayPort); err != nil {
			return Report{}, migrationError("destination_initialize_failed", "destination environment bootstrap failed", err)
		}
	}
	result, err := st.ImportLegacyConfig(ctx, prepared.Import, legacyCipher)
	if err != nil {
		return Report{}, migrationError("destination_import_failed", "destination configuration import failed", err)
	}
	if err := st.VerifyConfigImportAcceptance(ctx, result); err != nil {
		return Report{}, migrationError("destination_verification_failed", "destination configuration acceptance verification failed", err)
	}
	prepared.Report.AlreadyImported = result.AlreadyImported
	if result.AlreadyImported {
		prepared.Report.Status = "already imported"
	} else {
		prepared.Report.Status = "imported"
	}
	return prepared.Report, nil
}
