package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/toolhub-dev/toolhub/internal/skills"
)

type SourceInput struct {
	Kind         string
	Name         string
	URL          string
	Subdirectory string
	Commit       string
}

type ImportedSkill struct {
	SkillID   string `json:"skillId"`
	VersionID string `json:"versionId"`
	SourceID  string `json:"sourceId"`
	SHA256    string `json:"sha256"`
	RiskLevel string `json:"riskLevel"`
	Status    string `json:"status"`
}

func (s *Store) ImportSkill(ctx context.Context, source SourceInput, pkg skills.Package, provenance map[string]any, createdBy string) (ImportedSkill, error) {
	if source.Kind != "upload" && source.Kind != "git" && source.Kind != "skillsmp" && source.Kind != "openai" {
		return ImportedSkill{}, errors.New("invalid source kind")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ImportedSkill{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result := ImportedSkill{SkillID: uuid.NewString(), VersionID: uuid.NewString(), SourceID: uuid.NewString(), SHA256: pkg.SHA256, RiskLevel: pkg.Report.RiskLevel, Status: "pending"}
	if _, err := tx.Exec(ctx, `INSERT INTO skill_sources(id,kind,name,url,subdirectory,created_by) VALUES($1,$2,$3,$4,$5,$6)`, result.SourceID, source.Kind, strings.TrimSpace(source.Name), nullString(source.URL), source.Subdirectory, createdBy); err != nil {
		return ImportedSkill{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO skills(id,slug,name,description,source_id,created_by) VALUES($1,$2,$3,$4,$5,$6)`, result.SkillID, pkg.Slug, pkg.Name, pkg.Description, result.SourceID, createdBy); err != nil {
		return ImportedSkill{}, fmt.Errorf("create skill: %w", err)
	}
	report, _ := json.Marshal(pkg.Report)
	var artifactID string
	err = tx.QueryRow(ctx, "SELECT id::text FROM skill_artifacts WHERE sha256=$1", pkg.SHA256).Scan(&artifactID)
	if errors.Is(err, pgx.ErrNoRows) {
		artifactID = uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO skill_artifacts(id,sha256,size_bytes,content,scan_report) VALUES($1,$2,$3,$4,$5)`, artifactID, pkg.SHA256, len(pkg.CanonicalZIP), pkg.CanonicalZIP, string(report)); err != nil {
			return ImportedSkill{}, err
		}
	} else if err != nil {
		return ImportedSkill{}, err
	}
	manifest, _ := json.Marshal(pkg.Manifest)
	provenance["sourceKind"] = source.Kind
	provenance["sourceURL"] = source.URL
	provenance["subdirectory"] = source.Subdirectory
	provenance["sourceCommit"] = source.Commit
	provenance["contentSHA256"] = pkg.SHA256
	provenanceJSON, _ := json.Marshal(provenance)
	if _, err := tx.Exec(ctx, `INSERT INTO skill_versions(id,skill_id,source_commit,content_sha256,artifact_id,provenance,manifest,risk_level)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, result.VersionID, result.SkillID, source.Commit, pkg.SHA256, artifactID, string(provenanceJSON), string(manifest), pkg.Report.RiskLevel); err != nil {
		return ImportedSkill{}, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) ReviewSkill(ctx context.Context, skillID, decision, actor string) error {
	if decision != "approved" && decision != "rejected" {
		return errors.New("decision must be approved or rejected")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var versionID string
	if err := tx.QueryRow(ctx, "SELECT id::text FROM skill_versions WHERE skill_id=$1 ORDER BY created_at DESC LIMIT 1 FOR UPDATE", skillID).Scan(&versionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if decision == "approved" {
		if _, err := tx.Exec(ctx, "UPDATE skill_versions SET approved_at=now(),approved_by=$2 WHERE id=$1", versionID, actor); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE skills SET review_status='approved',current_version_id=$2,updated_at=now() WHERE id=$1", skillID, versionID); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, "UPDATE skills SET review_status='rejected',updated_at=now() WHERE id=$1", skillID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type DeploymentTarget struct {
	NodeID  string `json:"nodeId"`
	Runtime string `json:"runtime"`
	Enabled bool   `json:"enabled"`
}

func (s *Store) SetSkillTargets(ctx context.Context, skillID, actor string, targets []DeploymentTarget) error {
	if len(targets) > 500 {
		return errors.New("too many deployment targets")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var versionID string
	if err := tx.QueryRow(ctx, "SELECT current_version_id::text FROM skills WHERE id=$1 AND review_status='approved' AND archived_at IS NULL", skillID).Scan(&versionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("skill must be approved before targets are assigned")
		}
		return err
	}
	for _, target := range targets {
		if target.Runtime != "codex" && target.Runtime != "claude" && target.Runtime != "hermes" {
			return errors.New("invalid runtime target")
		}
		_, err := tx.Exec(ctx, `INSERT INTO deployments(id,node_id,runtime_kind,skill_id,desired_version_id,desired_enabled,state)
			VALUES($1,$2,$3,$4,$5,$6,'pending')
			ON CONFLICT(node_id,runtime_kind,skill_id) DO UPDATE SET previous_version_id=deployments.desired_version_id,desired_version_id=excluded.desired_version_id,desired_enabled=excluded.desired_enabled,state='pending',updated_at=now()`, uuid.NewString(), target.NodeID, target.Runtime, skillID, versionID, target.Enabled)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ArchiveSkill(ctx context.Context, id string) error {
	command, err := s.pool.Exec(ctx, "UPDATE skills SET archived_at=now(),updated_at=now() WHERE id=$1 AND protected=false AND archived_at IS NULL", id)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, _ = s.pool.Exec(ctx, "UPDATE deployments SET state='archived',updated_at=now() WHERE skill_id=$1", id)
	return nil
}

func (s *Store) ApproveUpdate(ctx context.Context, updateID, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var skillID, sha string
	if err := tx.QueryRow(ctx, "SELECT skill_id::text,candidate_sha256 FROM updates WHERE id=$1 AND status='available' FOR UPDATE", updateID).Scan(&skillID, &sha); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var versionID string
	if err := tx.QueryRow(ctx, "SELECT id::text FROM skill_versions WHERE skill_id=$1 AND content_sha256=$2 ORDER BY created_at DESC LIMIT 1", skillID, sha).Scan(&versionID); err != nil {
		return errors.New("candidate artifact is not available; rerun update check")
	}
	if _, err := tx.Exec(ctx, "UPDATE skill_versions SET approved_at=now(),approved_by=$2 WHERE id=$1", versionID, actor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE skills SET current_version_id=$2,updated_at=now() WHERE id=$1", skillID, versionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE deployments SET previous_version_id=desired_version_id,desired_version_id=$2,state='pending',updated_at=now() WHERE skill_id=$1", skillID, versionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE updates SET status='approved',approved_by=$2,approved_at=now() WHERE id=$1", updateID, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RollbackDeployment(ctx context.Context, deploymentID string) (string, string, error) {
	var nodeID, skillID string
	err := s.pool.QueryRow(ctx, `UPDATE deployments SET desired_version_id=previous_version_id,previous_version_id=desired_version_id,state='rolling_back',updated_at=now()
		WHERE id=$1 AND previous_version_id IS NOT NULL RETURNING node_id::text,skill_id::text`, deploymentID).Scan(&nodeID, &skillID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	return nodeID, skillID, nil
}

func (s *Store) Artifact(ctx context.Context, versionID string) ([]byte, string, error) {
	var content []byte
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT a.content,a.sha256 FROM skill_versions v JOIN skill_artifacts a ON a.id=v.artifact_id
		WHERE v.id=$1 AND v.approved_at IS NOT NULL`, versionID).Scan(&content, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	return content, hash, err
}

func (s *Store) PurgeExpiredArchives(ctx context.Context) (int64, error) {
	command, err := s.pool.Exec(ctx, "DELETE FROM skills WHERE archived_at<now()-interval '30 days' AND protected=false")
	if err != nil {
		return 0, err
	}
	return command.RowsAffected(), nil
}

func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

var _ = time.Now
