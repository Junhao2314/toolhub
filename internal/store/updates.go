package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/skills"
)

type UpdateSource struct {
	SkillID       string
	SourceID      string
	SkillName     string
	SourceKind    string
	URL           string
	Subdirectory  string
	CurrentCommit string
	CurrentSHA256 string
	CurrentRisk   string
	CurrentReport map[string]any
}

func (s *Store) UpdateSources(ctx context.Context, skillIDs []string) ([]UpdateSource, error) {
	rows, err := s.pool.Query(ctx, `SELECT s.id::text,ss.id::text,s.name,ss.kind,coalesce(ss.url,''),ss.subdirectory,
		coalesce(v.source_commit,''),coalesce(v.content_sha256,''),coalesce(v.risk_level,'low'),coalesce(a.scan_report,'{}'::jsonb)
		FROM skills s JOIN skill_sources ss ON ss.id=s.source_id LEFT JOIN skill_versions v ON v.id=s.current_version_id
		LEFT JOIN skill_artifacts a ON a.id=v.artifact_id
		WHERE s.archived_at IS NULL AND ss.kind IN ('git','skillsmp','openai') AND (cardinality($1::uuid[])=0 OR s.id=ANY($1::uuid[]))`, skillIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []UpdateSource
	for rows.Next() {
		var source UpdateSource
		var report []byte
		if err := rows.Scan(&source.SkillID, &source.SourceID, &source.SkillName, &source.SourceKind, &source.URL, &source.Subdirectory, &source.CurrentCommit, &source.CurrentSHA256, &source.CurrentRisk, &report); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(report, &source.CurrentReport)
		result = append(result, source)
	}
	return result, rows.Err()
}

func (s *Store) AddUpdateCandidate(ctx context.Context, source UpdateSource, sourceCommit string, pkg skills.Package, _ string) (string, error) {
	if pkg.SHA256 == source.CurrentSHA256 {
		return "", errors.New("candidate content matches current version")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var artifactID string
	err = tx.QueryRow(ctx, "SELECT id::text FROM skill_artifacts WHERE sha256=$1", pkg.SHA256).Scan(&artifactID)
	if errors.Is(err, pgx.ErrNoRows) {
		artifactID = uuid.NewString()
		report, _ := json.Marshal(pkg.Report)
		if _, err := tx.Exec(ctx, "INSERT INTO skill_artifacts(id,sha256,size_bytes,content,scan_report) VALUES($1,$2,$3,$4,$5)", artifactID, pkg.SHA256, len(pkg.CanonicalZIP), pkg.CanonicalZIP, string(report)); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	versionID := uuid.NewString()
	manifest, _ := json.Marshal(pkg.Manifest)
	provenance, _ := json.Marshal(map[string]any{"sourceKind": source.SourceKind, "sourceURL": source.URL, "subdirectory": source.Subdirectory, "sourceCommit": sourceCommit, "contentSHA256": pkg.SHA256, "updateCandidate": true})
	if _, err := tx.Exec(ctx, `INSERT INTO skill_versions(id,skill_id,source_commit,content_sha256,artifact_id,provenance,manifest,risk_level)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(skill_id,source_commit,content_sha256) DO NOTHING`, versionID, source.SkillID, sourceCommit, pkg.SHA256, artifactID, string(provenance), string(manifest), pkg.Report.RiskLevel); err != nil {
		return "", err
	}
	diff, _ := json.Marshal(map[string]any{"fromSHA256": source.CurrentSHA256, "toSHA256": pkg.SHA256, "fromFiles": source.CurrentReport["fileCount"], "toFiles": pkg.Report.FileCount, "fromBytes": source.CurrentReport["sizeBytes"], "toBytes": pkg.Report.SizeBytes})
	riskChange, _ := json.Marshal(map[string]any{"from": source.CurrentRisk, "to": pkg.Report.RiskLevel, "findings": pkg.Report.Findings})
	licenseChange, _ := json.Marshal(map[string]any{"from": source.CurrentReport["license"], "to": pkg.Report.License})
	updateID := uuid.NewString()
	if _, err := tx.Exec(ctx, `UPDATE updates SET status='superseded' WHERE skill_id=$1 AND status='available';
		INSERT INTO updates(id,skill_id,candidate_commit,candidate_sha256,diff,risk_change,license_change) VALUES($2,$1,$3,$4,$5,$6,$7)`, source.SkillID, updateID, sourceCommit, pkg.SHA256, string(diff), string(riskChange), string(licenseChange)); err != nil {
		return "", err
	}
	return updateID, tx.Commit(ctx)
}
