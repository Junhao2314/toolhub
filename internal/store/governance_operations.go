package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
)

type GovernanceApplyPreparation struct {
	RevisionID                        string
	Target                            domain.Target
	Manifest                          bridgeprotocol.DesiredManifest
	RoutingHash                       string
	ExpectedAppliedRevisionID         string
	AffectedProfileRevisions          map[string]string
	ExpectedPublishedProfileRevisions map[string]string
}

type governanceApplyRequestIdentity struct {
	Kind           string
	IdempotencyKey string
	RevisionID     string
	TargetRevision string
	RoutingHash    string
	ProfileIDs     []string
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Store) PrepareRelayConfigurationApply(ctx context.Context, revisionID string, profileIDs []string) (GovernanceApplyPreparation, error) {
	if uuid.Validate(revisionID) != nil || hasDuplicateIDs(profileIDs) {
		return GovernanceApplyPreparation{}, ErrNotFound
	}
	if _, err := s.RelayConfiguration(ctx, revisionID); err != nil {
		return GovernanceApplyPreparation{}, err
	}
	target, err := s.sharedRelayTarget(ctx)
	if err != nil {
		return GovernanceApplyPreparation{}, err
	}
	profiles, predecessors, err := s.profileApplyRevisions(ctx, profileIDs)
	if err != nil {
		return GovernanceApplyPreparation{}, err
	}
	var appliedRevisionID string
	if err := s.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&appliedRevisionID); err != nil {
		return GovernanceApplyPreparation{}, err
	}
	candidate := RoutingBundleCandidate{RelayConfigurationRevisionID: revisionID, PublishedProfileRevisions: profiles}
	manifest, err := s.resolveRelayManifestCandidate(ctx, target, "", 0, candidate)
	if err != nil {
		return GovernanceApplyPreparation{}, err
	}
	return GovernanceApplyPreparation{
		RevisionID: revisionID, Target: target, Manifest: manifest, RoutingHash: manifest.RelayGovernance.RoutingHash,
		ExpectedAppliedRevisionID: appliedRevisionID, AffectedProfileRevisions: profiles, ExpectedPublishedProfileRevisions: predecessors,
	}, nil
}

func (s *Store) CreateRelayConfigurationApplyOperation(ctx context.Context, revisionID string, profileIDs []string, targetRevision, routingHash, idempotencyKey string) (domain.Operation, error) {
	if !bridgeprotocol.IsSHA256(targetRevision) || !bridgeprotocol.IsSHA256(routingHash) {
		return domain.Operation{}, ErrConflict
	}
	if uuid.Validate(revisionID) != nil || hasDuplicateIDs(profileIDs) {
		return domain.Operation{}, ErrNotFound
	}
	identity := governanceApplyRequestIdentity{Kind: "relay_config_apply", IdempotencyKey: idempotencyKey, RevisionID: revisionID, TargetRevision: targetRevision, RoutingHash: routingHash, ProfileIDs: profileIDs}
	if operation, found, err := s.replayGovernanceApplyOperation(ctx, identity); err != nil {
		return domain.Operation{}, err
	} else if found {
		return operation, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Operation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	target, appliedRelayRevisionID, _, replayID, err := s.lockGovernanceApplyTransactionTx(ctx, tx, identity)
	if err != nil {
		return domain.Operation{}, err
	}
	if replayID != "" {
		_ = tx.Rollback(ctx)
		return s.Operation(ctx, replayID)
	}
	prepared, err := s.prepareRelayConfigurationApplyTx(ctx, tx, target, revisionID, profileIDs, appliedRelayRevisionID)
	if err != nil {
		return domain.Operation{}, err
	}
	if prepared.RoutingHash != routingHash {
		return domain.Operation{}, ErrConflict
	}
	metadata := map[string]any{
		"revisionId": revisionID, "routingHash": routingHash,
		"expectedAppliedRelayConfigurationRevisionId": prepared.ExpectedAppliedRevisionID,
		"affectedProfileRevisions":                    prepared.AffectedProfileRevisions,
		"expectedPublishedProfileRevisions":           prepared.ExpectedPublishedProfileRevisions,
	}
	targetRequest := map[string]any{"manifest": prepared.Manifest, "targetRevision": targetRevision, "sourceKind": "relay_config_apply", "sourceId": revisionID}
	result, err := s.createOperationTx(ctx, tx, CreateOperationInput{
		Kind: "relay_config_apply", SourceID: revisionID, IdempotencyKey: idempotencyKey,
		Request: metadata, Metadata: metadata, TargetIDs: []string{prepared.Target.ID}, TargetRequests: map[string]any{prepared.Target.ID: targetRequest},
	}, true)
	if err != nil {
		return domain.Operation{}, err
	}
	if result.Replay {
		_ = tx.Rollback(ctx)
		return s.Operation(ctx, result.ID)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, err
	}
	return s.Operation(ctx, result.ID)
}

func (s *Store) PrepareGlobalPolicyApply(ctx context.Context, revisionID string) (GovernanceApplyPreparation, error) {
	if uuid.Validate(revisionID) != nil {
		return GovernanceApplyPreparation{}, ErrNotFound
	}
	if _, err := s.GlobalPolicy(ctx, revisionID); err != nil {
		return GovernanceApplyPreparation{}, err
	}
	target, err := s.sharedRelayTarget(ctx)
	if err != nil {
		return GovernanceApplyPreparation{}, err
	}
	var appliedRevisionID string
	if err := s.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM global_policy_state WHERE singleton`).Scan(&appliedRevisionID); err != nil {
		return GovernanceApplyPreparation{}, err
	}
	manifest, err := s.resolveRelayManifestCandidate(ctx, target, "", 0, RoutingBundleCandidate{GlobalPolicyRevisionID: revisionID})
	if err != nil {
		return GovernanceApplyPreparation{}, err
	}
	return GovernanceApplyPreparation{RevisionID: revisionID, Target: target, Manifest: manifest, RoutingHash: manifest.RelayGovernance.RoutingHash, ExpectedAppliedRevisionID: appliedRevisionID}, nil
}

func (s *Store) CreateGlobalPolicyApplyOperation(ctx context.Context, revisionID, targetRevision, idempotencyKey string) (domain.Operation, error) {
	if uuid.Validate(revisionID) != nil {
		return domain.Operation{}, ErrNotFound
	}
	if !bridgeprotocol.IsSHA256(targetRevision) {
		return domain.Operation{}, ErrConflict
	}
	identity := governanceApplyRequestIdentity{Kind: "policy_apply", IdempotencyKey: idempotencyKey, RevisionID: revisionID, TargetRevision: targetRevision}
	if operation, found, err := s.replayGovernanceApplyOperation(ctx, identity); err != nil {
		return domain.Operation{}, err
	} else if found {
		return operation, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Operation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	target, _, appliedPolicyRevisionID, replayID, err := s.lockGovernanceApplyTransactionTx(ctx, tx, identity)
	if err != nil {
		return domain.Operation{}, err
	}
	if replayID != "" {
		_ = tx.Rollback(ctx)
		return s.Operation(ctx, replayID)
	}
	prepared, err := s.prepareGlobalPolicyApplyTx(ctx, tx, target, revisionID, appliedPolicyRevisionID)
	if err != nil {
		return domain.Operation{}, err
	}
	metadata := map[string]any{"revisionId": revisionID, "routingHash": prepared.RoutingHash, "expectedAppliedGlobalPolicyRevisionId": prepared.ExpectedAppliedRevisionID}
	targetRequest := map[string]any{"manifest": prepared.Manifest, "targetRevision": targetRevision, "sourceKind": "relay_config_apply", "sourceId": prepared.Manifest.RelayGovernance.RelayConfigurationRevisionID}
	result, err := s.createOperationTx(ctx, tx, CreateOperationInput{
		Kind: "policy_apply", SourceID: revisionID, IdempotencyKey: idempotencyKey,
		Request: metadata, Metadata: metadata, TargetIDs: []string{prepared.Target.ID}, TargetRequests: map[string]any{prepared.Target.ID: targetRequest},
	}, true)
	if err != nil {
		return domain.Operation{}, err
	}
	if result.Replay {
		_ = tx.Rollback(ctx)
		return s.Operation(ctx, result.ID)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, err
	}
	return s.Operation(ctx, result.ID)
}

func (s *Store) replayGovernanceApplyOperation(ctx context.Context, identity governanceApplyRequestIdentity) (domain.Operation, bool, error) {
	operationID, found, err := governanceApplyReplayID(ctx, s.pool, identity)
	if err != nil || !found {
		return domain.Operation{}, found, err
	}
	operation, err := s.Operation(ctx, operationID)
	return operation, true, err
}

func governanceApplyReplayID(ctx context.Context, query queryRower, identity governanceApplyRequestIdentity) (string, bool, error) {
	if identity.IdempotencyKey == "" {
		return "", false, nil
	}
	var operationID string
	var metadata, targetRequest json.RawMessage
	err := query.QueryRow(ctx, `SELECT operation.id::text,operation.metadata,target.request FROM operations operation JOIN operation_targets target ON target.operation_id=operation.id JOIN targets relay ON relay.id=target.target_id AND relay.runtime='shared-relay' WHERE operation.kind=$1 AND operation.idempotency_key=$2`, identity.Kind, identity.IdempotencyKey).Scan(&operationID, &metadata, &targetRequest)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var request struct {
		TargetRevision string `json:"targetRevision"`
	}
	if json.Unmarshal(targetRequest, &request) != nil || stringMetadata(metadata, "revisionId") != identity.RevisionID || request.TargetRevision != identity.TargetRevision {
		return "", false, ErrIdempotencyConflict
	}
	if identity.Kind == "relay_config_apply" {
		affected, ok := stringMapMetadata(metadata, "affectedProfileRevisions")
		if !ok || stringMetadata(metadata, "routingHash") != identity.RoutingHash || !sameProfileIDSet(identity.ProfileIDs, affected) {
			return "", false, ErrIdempotencyConflict
		}
	}
	return operationID, true, nil
}

func sameProfileIDSet(profileIDs []string, revisions map[string]string) bool {
	if len(profileIDs) != len(revisions) {
		return false
	}
	for _, profileID := range profileIDs {
		if _, ok := revisions[profileID]; !ok {
			return false
		}
	}
	return true
}

func (s *Store) lockGovernanceApplyTransactionTx(ctx context.Context, tx pgx.Tx, identity governanceApplyRequestIdentity) (domain.Target, string, string, string, error) {
	var targetID string
	if err := tx.QueryRow(ctx, `SELECT t.id::text FROM targets t JOIN nodes n ON n.id=t.node_id WHERE t.target_key='local/shared-relay' AND n.archived_at IS NULL`).Scan(&targetID); errors.Is(err, pgx.ErrNoRows) {
		return domain.Target{}, "", "", "", ErrNotFound
	} else if err != nil {
		return domain.Target{}, "", "", "", err
	}
	if err := lockActiveTargets(ctx, tx, []string{targetID}); err != nil {
		return domain.Target{}, "", "", "", err
	}
	if replayID, found, err := governanceApplyReplayID(ctx, tx, identity); err != nil {
		return domain.Target{}, "", "", "", err
	} else if found {
		return domain.Target{}, "", "", replayID, nil
	}
	if active, err := governanceTargetOwnedTx(ctx, tx, targetID); err != nil {
		return domain.Target{}, "", "", "", err
	} else if active {
		return domain.Target{}, "", "", "", ErrOperationActive
	}
	var appliedRelayRevisionID, appliedPolicyRevisionID string
	var relayPort int
	if err := tx.QueryRow(ctx, `SELECT relay.applied_revision_id::text,policy.applied_revision_id::text,settings.relay_port FROM relay_configuration_state relay CROSS JOIN global_policy_state policy CROSS JOIN settings WHERE relay.singleton AND policy.singleton AND settings.singleton FOR SHARE OF relay,policy,settings`).Scan(&appliedRelayRevisionID, &appliedPolicyRevisionID, &relayPort); err != nil {
		return domain.Target{}, "", "", "", err
	}
	var target domain.Target
	if err := tx.QueryRow(ctx, `SELECT t.id::text,t.target_key,t.node_id::text,n.name,n.kind,coalesce(n.salt_minion_id,''),t.runtime,t.managed_username,t.writable FROM targets t JOIN nodes n ON n.id=t.node_id WHERE t.id=$1 AND n.archived_at IS NULL FOR UPDATE OF t`, targetID).Scan(&target.ID, &target.TargetKey, &target.NodeID, &target.NodeName, &target.NodeKind, &target.SaltMinionID, &target.Runtime, &target.ManagedUsername, &target.Writable); errors.Is(err, pgx.ErrNoRows) {
		return domain.Target{}, "", "", "", ErrNotFound
	} else if err != nil {
		return domain.Target{}, "", "", "", err
	}
	if replayID, found, err := governanceApplyReplayID(ctx, tx, identity); err != nil {
		return domain.Target{}, "", "", "", err
	} else if found {
		return domain.Target{}, "", "", replayID, nil
	}
	if active, err := governanceTargetOwnedTx(ctx, tx, targetID); err != nil {
		return domain.Target{}, "", "", "", err
	} else if active {
		return domain.Target{}, "", "", "", ErrOperationActive
	}
	return target, appliedRelayRevisionID, appliedPolicyRevisionID, "", nil
}

func governanceTargetOwnedTx(ctx context.Context, tx pgx.Tx, targetID string) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_targets WHERE target_id=$1 AND (status IN ('queued','running') OR governance_finalization_pending))`, targetID).Scan(&active)
	return active, err
}

func (s *Store) prepareRelayConfigurationApplyTx(ctx context.Context, tx pgx.Tx, target domain.Target, revisionID string, profileIDs []string, appliedRevisionID string) (GovernanceApplyPreparation, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_configuration_revisions WHERE id=$1)`, revisionID).Scan(&exists); err != nil {
		return GovernanceApplyPreparation{}, err
	}
	if !exists {
		return GovernanceApplyPreparation{}, ErrNotFound
	}
	profiles, predecessors, err := profileApplyRevisionsTx(ctx, tx, profileIDs)
	if err != nil {
		return GovernanceApplyPreparation{}, err
	}
	if err := lockRoutingContractStatesTx(ctx, tx, revisionID); err != nil {
		return GovernanceApplyPreparation{}, err
	}
	candidate := RoutingBundleCandidate{RelayConfigurationRevisionID: revisionID, PublishedProfileRevisions: profiles}
	manifest, err := s.resolveRelayManifestCandidateTx(ctx, tx, target, "", 0, candidate)
	if err != nil {
		return GovernanceApplyPreparation{}, err
	}
	return GovernanceApplyPreparation{
		RevisionID: revisionID, Target: target, Manifest: manifest, RoutingHash: manifest.RelayGovernance.RoutingHash,
		ExpectedAppliedRevisionID: appliedRevisionID, AffectedProfileRevisions: profiles, ExpectedPublishedProfileRevisions: predecessors,
	}, nil
}

func (s *Store) prepareGlobalPolicyApplyTx(ctx context.Context, tx pgx.Tx, target domain.Target, revisionID, appliedRevisionID string) (GovernanceApplyPreparation, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM global_policy_revisions WHERE id=$1)`, revisionID).Scan(&exists); err != nil {
		return GovernanceApplyPreparation{}, err
	}
	if !exists {
		return GovernanceApplyPreparation{}, ErrNotFound
	}
	var relayRevisionID string
	if err := tx.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton`).Scan(&relayRevisionID); err != nil {
		return GovernanceApplyPreparation{}, err
	}
	if err := lockRoutingContractStatesTx(ctx, tx, relayRevisionID); err != nil {
		return GovernanceApplyPreparation{}, err
	}
	manifest, err := s.resolveRelayManifestCandidateTx(ctx, tx, target, "", 0, RoutingBundleCandidate{GlobalPolicyRevisionID: revisionID})
	if err != nil {
		return GovernanceApplyPreparation{}, err
	}
	return GovernanceApplyPreparation{RevisionID: revisionID, Target: target, Manifest: manifest, RoutingHash: manifest.RelayGovernance.RoutingHash, ExpectedAppliedRevisionID: appliedRevisionID}, nil
}

func profileApplyRevisionsTx(ctx context.Context, tx pgx.Tx, profileIDs []string) (map[string]string, map[string]string, error) {
	ids := append([]string(nil), profileIDs...)
	sort.Strings(ids)
	candidates := make(map[string]string, len(ids))
	predecessors := make(map[string]string, len(ids))
	for _, profileID := range ids {
		if uuid.Validate(profileID) != nil {
			return nil, nil, ErrNotFound
		}
		var currentRevisionID, publishedRevisionID string
		err := tx.QueryRow(ctx, `SELECT p.current_revision_id::text,pp.profile_revision_id::text FROM profiles p JOIN published_profiles pp ON pp.profile_id=p.id WHERE p.id=$1 AND p.archived_at IS NULL FOR SHARE OF p,pp`, profileID).Scan(&currentRevisionID, &publishedRevisionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrConflict
		}
		if err != nil {
			return nil, nil, err
		}
		candidates[profileID], predecessors[profileID] = currentRevisionID, publishedRevisionID
	}
	return candidates, predecessors, nil
}

func lockRoutingContractStatesTx(ctx context.Context, tx pgx.Tx, relayRevisionID string) error {
	rows, err := tx.Query(ctx, `SELECT state.server_id::text FROM mcp_contract_state state JOIN relay_configuration_revision_mcp_servers member ON member.server_id=state.server_id WHERE member.relay_configuration_revision_id=$1 ORDER BY state.server_id FOR SHARE OF state`, relayRevisionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var serverID string
		if err := rows.Scan(&serverID); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) sharedRelayTarget(ctx context.Context) (domain.Target, error) {
	var id string
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM targets WHERE target_key='local/shared-relay'`).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return domain.Target{}, ErrNotFound
	} else if err != nil {
		return domain.Target{}, err
	}
	return s.Target(ctx, id)
}

func (s *Store) profileApplyRevisions(ctx context.Context, profileIDs []string) (map[string]string, map[string]string, error) {
	ids := append([]string(nil), profileIDs...)
	sort.Strings(ids)
	candidates := make(map[string]string, len(ids))
	predecessors := make(map[string]string, len(ids))
	for _, profileID := range ids {
		if uuid.Validate(profileID) != nil {
			return nil, nil, ErrNotFound
		}
		var currentRevisionID, publishedRevisionID string
		err := s.pool.QueryRow(ctx, `SELECT p.current_revision_id::text,pp.profile_revision_id::text FROM profiles p JOIN published_profiles pp ON pp.profile_id=p.id WHERE p.id=$1 AND p.archived_at IS NULL`, profileID).Scan(&currentRevisionID, &publishedRevisionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrConflict
		}
		if err != nil {
			return nil, nil, err
		}
		candidates[profileID], predecessors[profileID] = currentRevisionID, publishedRevisionID
	}
	return candidates, predecessors, nil
}

func hasDuplicateIDs(ids []string) bool {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}
