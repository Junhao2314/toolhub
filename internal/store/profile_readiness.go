package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/clientlaunch"
	"github.com/Junhao2314/toolhub/internal/domain"
)

type ProfileLaunchReadiness struct {
	Ready             bool                                          `json:"ready"`
	ReasonCode        string                                        `json:"reasonCode,omitempty"`
	ProfileID         string                                        `json:"profileId"`
	ProfileRevisionID string                                        `json:"profileRevisionId"`
	ClientKind        string                                        `json:"clientKind"`
	NativeClient      bridgeprotocol.NativeClientInspectionResponse `json:"nativeClient"`
	Command           *clientlaunch.Command                         `json:"command,omitempty"`
}

func (s *Store) ProfileNativeClientInspectionRequest(ctx context.Context, profileID string) (domain.Profile, bridgeprotocol.NativeClientInspectionRequest, error) {
	profile, err := s.Profile(ctx, profileID)
	if err != nil {
		return domain.Profile{}, bridgeprotocol.NativeClientInspectionRequest{}, err
	}
	if profile.ClientKind != bridgeprotocol.RuntimeClaude && profile.ClientKind != bridgeprotocol.RuntimeCodex {
		return profile, bridgeprotocol.NativeClientInspectionRequest{}, ErrConflict
	}
	var managedUsername string
	err = s.pool.QueryRow(ctx, `SELECT managed_username FROM targets WHERE target_key=$1`, "local/"+profile.ClientKind).Scan(&managedUsername)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Profile{}, bridgeprotocol.NativeClientInspectionRequest{}, ErrNotFound
	}
	if err != nil {
		return domain.Profile{}, bridgeprotocol.NativeClientInspectionRequest{}, err
	}
	return profile, bridgeprotocol.NativeClientInspectionRequest{ManagedUsername: managedUsername, ClientKind: profile.ClientKind}, nil
}

func (s *Store) ProfileReadiness(ctx context.Context, profileID string, inspection bridgeprotocol.NativeClientInspectionResponse) (ProfileLaunchReadiness, error) {
	if uuid.Validate(profileID) != nil {
		return ProfileLaunchReadiness{}, ErrNotFound
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ProfileLaunchReadiness{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentRevisionID, publishedRevisionID, profileName, clientKind string
	var profileRevision int64
	var archived bool
	err = tx.QueryRow(ctx, `SELECT p.current_revision_id::text,p.revision,p.name,p.client_kind,p.archived_at IS NOT NULL,coalesce(pp.profile_revision_id::text,'') FROM profiles p LEFT JOIN published_profiles pp ON pp.profile_id=p.id WHERE p.id=$1`, profileID).Scan(&currentRevisionID, &profileRevision, &profileName, &clientKind, &archived, &publishedRevisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfileLaunchReadiness{}, ErrNotFound
	}
	if err != nil {
		return ProfileLaunchReadiness{}, err
	}
	result := ProfileLaunchReadiness{ProfileID: profileID, ProfileRevisionID: currentRevisionID, ClientKind: clientKind, NativeClient: inspection}
	notReady := func(reason string) (ProfileLaunchReadiness, error) {
		result.ReasonCode = reason
		return result, tx.Commit(ctx)
	}
	if archived {
		return notReady("profile_archived")
	}
	if publishedRevisionID != currentRevisionID {
		return notReady("profile_revision_not_published")
	}
	if clientKind != bridgeprotocol.RuntimeClaude && clientKind != bridgeprotocol.RuntimeCodex {
		return notReady("profile_client_unsupported")
	}

	var skillSourceKind, skillSourceID, skillHealth, skillTargetID string
	var skillProfileRevision int64
	err = tx.QueryRow(ctx, `SELECT ds.source_kind,coalesce(ds.source_id::text,''),coalesce(ds.profile_revision,0),active.health,t.id::text FROM targets t JOIN target_desired_snapshots active ON active.target_id=t.id JOIN desired_snapshots ds ON ds.id=active.snapshot_id WHERE t.target_key=$1`, "local/"+clientKind).Scan(&skillSourceKind, &skillSourceID, &skillProfileRevision, &skillHealth, &skillTargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return notReady("profile_target_not_applied")
	}
	if err != nil {
		return ProfileLaunchReadiness{}, err
	}
	if skillSourceKind != "profile_apply" || skillSourceID != profileID || skillProfileRevision != profileRevision {
		return notReady("profile_target_not_applied")
	}
	if skillHealth != bridgeprotocol.HealthHealthy {
		return notReady("profile_target_unhealthy")
	}

	var relayHealth, appliedRoutingHash, relayTargetID string
	err = tx.QueryRow(ctx, `SELECT active.health,coalesce(ds.manifest->'relayGovernance'->>'routingHash',''),t.id::text FROM targets t JOIN target_desired_snapshots active ON active.target_id=t.id JOIN desired_snapshots ds ON ds.id=active.snapshot_id WHERE t.target_key='local/shared-relay'`).Scan(&relayHealth, &appliedRoutingHash, &relayTargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return notReady("relay_not_applied")
	}
	if err != nil {
		return ProfileLaunchReadiness{}, err
	}
	if appliedRoutingHash == "" {
		return notReady("relay_not_applied")
	}
	var relayPort int
	var relayPaused bool
	if err := tx.QueryRow(ctx, `SELECT relay_port,relay_intentional_paused FROM settings WHERE singleton`).Scan(&relayPort, &relayPaused); err != nil {
		return ProfileLaunchReadiness{}, err
	}
	if relayPaused {
		return notReady("relay_paused")
	}
	if relayHealth != bridgeprotocol.HealthHealthy {
		return notReady("relay_unhealthy")
	}
	var unfinalizedMutation bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM operation_targets ot
			JOIN operations o ON o.id=ot.operation_id
			JOIN target_desired_snapshots active ON active.target_id=ot.target_id
			JOIN desired_snapshots snapshot ON snapshot.id=active.snapshot_id
			WHERE ot.target_id IN ($1,$2)
			  AND o.kind='apply'
			  AND ot.request->>'sourceKind'='profile_apply'
			  AND (
				ot.status IN ('queued','running')
				OR (ot.status='succeeded' AND ot.finished_at>snapshot.created_at)
			  )
		)`, skillTargetID, relayTargetID).Scan(&unfinalizedMutation); err != nil {
		return ProfileLaunchReadiness{}, err
	}
	if unfinalizedMutation {
		return notReady("profile_apply_unfinalized")
	}
	_, currentRoutingHash, err := s.RenderRoutingBundleTx(ctx, tx)
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		return notReady("relay_routing_unavailable")
	}
	if err != nil {
		return ProfileLaunchReadiness{}, err
	}
	if currentRoutingHash != appliedRoutingHash {
		return notReady("relay_routing_mismatch")
	}

	if inspection.ClientKind != clientKind {
		return notReady("native_client_kind_mismatch")
	}
	if !inspection.Supported {
		reason := inspection.ErrorCode
		if reason == "" {
			reason = "native_client_unsupported"
		}
		return notReady(reason)
	}
	if inspection.Version == "" {
		return notReady("native_client_inspection_invalid")
	}
	command, err := clientlaunch.BuildCommand(clientKind, profileName, relayPort)
	if err != nil {
		return ProfileLaunchReadiness{}, err
	}
	result.Ready = true
	result.Command = &command
	return result, tx.Commit(ctx)
}
