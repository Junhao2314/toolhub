package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/policy"
)

type GlobalPolicyInput struct {
	Revision             int64
	CatalogVersion       int
	ExplicitOverrides    map[string]string
	UnclassifiedMutating string
	ReviewedReadOnly     string
}

func (s *Store) SaveGlobalPolicy(ctx context.Context, input GlobalPolicyInput) (domain.GlobalPolicyRevision, error) {
	if input.CatalogVersion == 0 {
		input.CatalogVersion = policy.CatalogVersion
	}
	if input.UnclassifiedMutating == "" {
		input.UnclassifiedMutating = policy.DecisionConfirm
	}
	if input.ReviewedReadOnly == "" {
		input.ReviewedReadOnly = policy.DecisionAllow
	}
	if input.CatalogVersion != policy.CatalogVersion || !policy.ValidateDecision(input.UnclassifiedMutating) || !policy.ValidateDecision(input.ReviewedReadOnly) {
		return domain.GlobalPolicyRevision{}, errors.New("invalid global policy catalog or decision")
	}
	if input.ExplicitOverrides == nil {
		input.ExplicitOverrides = map[string]string{}
	}
	if len(input.ExplicitOverrides) > 20000 {
		return domain.GlobalPolicyRevision{}, errors.New("global policy override limit exceeded")
	}
	for key, decision := range input.ExplicitOverrides {
		if strings.TrimSpace(key) == "" || len(key) > 256 || !policy.ValidateDecision(decision) {
			return domain.GlobalPolicyRevision{}, fmt.Errorf("invalid global policy override %q", key)
		}
	}
	hash, err := canonicalPolicyHash(input)
	if err != nil {
		return domain.GlobalPolicyRevision{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.GlobalPolicyRevision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentID string
	if err := tx.QueryRow(ctx, `SELECT current_revision_id::text FROM global_policy_state WHERE singleton FOR UPDATE`).Scan(&currentID); err != nil {
		return domain.GlobalPolicyRevision{}, err
	}
	var currentRevision int64
	var currentHash string
	if err := tx.QueryRow(ctx, `SELECT revision,canonical_hash FROM global_policy_revisions WHERE id=$1`, currentID).Scan(&currentRevision, &currentHash); err != nil {
		return domain.GlobalPolicyRevision{}, err
	}
	if input.Revision != 0 && input.Revision != currentRevision {
		return domain.GlobalPolicyRevision{}, ErrConflict
	}
	if currentHash == hash {
		if err := tx.Commit(ctx); err != nil {
			return domain.GlobalPolicyRevision{}, err
		}
		return s.GlobalPolicy(ctx, currentID)
	}
	revision := currentRevision + 1
	id := uuid.NewString()
	overrides, _ := json.Marshal(input.ExplicitOverrides)
	if _, err := tx.Exec(ctx, `INSERT INTO global_policy_revisions(id,revision,canonical_hash,catalog_version,explicit_overrides,unclassified_mutating,reviewed_read_only) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, revision, hash, input.CatalogVersion, jsonText(overrides), input.UnclassifiedMutating, input.ReviewedReadOnly); err != nil {
		return domain.GlobalPolicyRevision{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE global_policy_state SET current_revision_id=$1,updated_at=now() WHERE singleton`, id); err != nil {
		return domain.GlobalPolicyRevision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.GlobalPolicyRevision{}, err
	}
	return s.GlobalPolicy(ctx, id)
}

func canonicalPolicyHash(input GlobalPolicyInput) (string, error) {
	keys := make([]string, 0, len(input.ExplicitOverrides))
	for key := range input.ExplicitOverrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make([][2]string, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, [2]string{key, input.ExplicitOverrides[key]})
	}
	body, err := json.Marshal(struct {
		CatalogVersion       int         `json:"catalogVersion"`
		ExplicitOverrides    [][2]string `json:"explicitOverrides"`
		UnclassifiedMutating string      `json:"unclassifiedMutating"`
		ReviewedReadOnly     string      `json:"reviewedReadOnly"`
	}{input.CatalogVersion, ordered, input.UnclassifiedMutating, input.ReviewedReadOnly})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) GlobalPolicy(ctx context.Context, id string) (domain.GlobalPolicyRevision, error) {
	var result domain.GlobalPolicyRevision
	var overrides []byte
	err := s.pool.QueryRow(ctx, `SELECT id::text,revision,canonical_hash,catalog_version,explicit_overrides,unclassified_mutating,reviewed_read_only,created_at FROM global_policy_revisions WHERE id=$1`, id).Scan(&result.ID, &result.Revision, &result.CanonicalHash, &result.CatalogVersion, &overrides, &result.UnclassifiedMutating, &result.ReviewedReadOnly, &result.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GlobalPolicyRevision{}, ErrNotFound
	}
	if err != nil {
		return domain.GlobalPolicyRevision{}, err
	}
	if err := json.Unmarshal(overrides, &result.ExplicitOverrides); err != nil {
		return domain.GlobalPolicyRevision{}, err
	}
	return result, nil
}

func (s *Store) ApplyGlobalPolicy(ctx context.Context, revisionID string) error {
	return ErrConflict
}

func (s *Store) FinalizeGlobalPolicyApply(ctx context.Context, operationID, revisionID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	metadata, err := validateSucceededGovernanceReloadTx(ctx, tx, operationID, "policy_apply", "", "")
	if err != nil {
		return err
	}
	if stringMetadata(metadata, "revisionId") != revisionID {
		return ErrConflict
	}
	var appliedRevisionID string
	if err := tx.QueryRow(ctx, `SELECT applied_revision_id::text FROM global_policy_state WHERE singleton FOR UPDATE`).Scan(&appliedRevisionID); err != nil {
		return err
	}
	if appliedRevisionID != stringMetadata(metadata, "expectedAppliedGlobalPolicyRevisionId") {
		return ErrConflict
	}
	if err := s.validateCandidateRoutingHashTx(ctx, tx, RoutingBundleCandidate{GlobalPolicyRevisionID: revisionID}, stringMetadata(metadata, "routingHash")); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM global_policy_revisions WHERE id=$1)`, revisionID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE global_policy_state SET applied_revision_id=$1,updated_at=now() WHERE singleton`, revisionID); err != nil {
		return err
	}
	if err := markGovernanceFinalizedTx(ctx, tx, operationID, "global_policy_apply"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) AppliedGlobalPolicy(ctx context.Context) (domain.GlobalPolicyRevision, error) {
	var id string
	if err := s.pool.QueryRow(ctx, `SELECT applied_revision_id::text FROM global_policy_state WHERE singleton`).Scan(&id); err != nil {
		return domain.GlobalPolicyRevision{}, err
	}
	return s.GlobalPolicy(ctx, id)
}

func globalDecisionForTool(global domain.GlobalPolicyRevision, toolID string) string {
	if decision := global.ExplicitOverrides[toolID]; policy.ValidateDecision(decision) {
		return decision
	}
	return global.UnclassifiedMutating
}

func validateProfileToolCeiling(ctx context.Context, tx pgx.Tx, rules []ProfileToolRuleInput) error {
	if len(rules) == 0 {
		return nil
	}
	var globalID string
	if err := tx.QueryRow(ctx, `SELECT applied_revision_id::text FROM global_policy_state WHERE singleton`).Scan(&globalID); err != nil {
		return err
	}
	var global domain.GlobalPolicyRevision
	var overrides []byte
	if err := tx.QueryRow(ctx, `SELECT id::text,revision,canonical_hash,catalog_version,explicit_overrides,unclassified_mutating,reviewed_read_only,created_at FROM global_policy_revisions WHERE id=$1`, globalID).Scan(&global.ID, &global.Revision, &global.CanonicalHash, &global.CatalogVersion, &overrides, &global.UnclassifiedMutating, &global.ReviewedReadOnly, &global.CreatedAt); err != nil {
		return err
	}
	if err := json.Unmarshal(overrides, &global.ExplicitOverrides); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if uuid.Validate(rule.ToolID) != nil || !policy.ValidateDecision(rule.Decision) || len(rule.ReasonCodes) > 16 {
			return errors.New("invalid profile tool rule")
		}
		for _, reason := range rule.ReasonCodes {
			if !policy.ValidateReasonCode(reason) {
				return errors.New("invalid profile tool rule reason")
			}
		}
		if _, exists := seen[rule.ToolID]; exists {
			return errors.New("duplicate profile tool rule")
		}
		seen[rule.ToolID] = struct{}{}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_tools WHERE id=$1)`, rule.ToolID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		var toolName string
		if err := tx.QueryRow(ctx, `SELECT name FROM mcp_tools WHERE id=$1`, rule.ToolID).Scan(&toolName); err != nil {
			return err
		}
		globalDecision := globalDecisionForTool(global, rule.ToolID)
		if decision := global.ExplicitOverrides[toolName]; policy.ValidateDecision(decision) {
			globalDecision = decision
		}
		if policy.EffectiveDecision(globalDecision, rule.Decision) != rule.Decision {
			return ErrConflict
		}
	}
	return nil
}
