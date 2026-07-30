package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
)

type ProfileInput struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SkillIDs     []string `json:"skillIds"`
	MCPServerIDs []string `json:"mcpServerIds"`
	Revision     int64    `json:"revision,omitempty"`
}

func (s *Store) SaveProfile(ctx context.Context, id string, input ProfileInput) (domain.Profile, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 {
		return domain.Profile{}, errors.New("Profile name must contain 1-120 characters")
	}
	if id == "" {
		id = uuid.NewString()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Profile{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current int64
	err = tx.QueryRow(ctx, `SELECT revision FROM profiles WHERE id=$1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		current = 0
	} else if err != nil {
		return domain.Profile{}, err
	}
	if current > 0 && input.Revision != current {
		return domain.Profile{}, ErrConflict
	}
	current++
	if _, err := tx.Exec(ctx, `INSERT INTO profiles(id,name,description,revision) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,description=EXCLUDED.description,revision=EXCLUDED.revision,updated_at=now()`, id, input.Name, strings.TrimSpace(input.Description), current); err != nil {
		if isUniqueViolation(err) {
			return domain.Profile{}, ErrConflict
		}
		return domain.Profile{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM profile_skills WHERE profile_id=$1`, id); err != nil {
		return domain.Profile{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM profile_mcp_servers WHERE profile_id=$1`, id); err != nil {
		return domain.Profile{}, err
	}
	for _, skillID := range uniqueIDs(input.SkillIDs) {
		if _, err := tx.Exec(ctx, `INSERT INTO profile_skills(profile_id,skill_id) SELECT $1,id FROM skills WHERE id=$2 AND archived_at IS NULL`, id, skillID); err != nil {
			return domain.Profile{}, err
		}
	}
	for _, serverID := range uniqueIDs(input.MCPServerIDs) {
		if _, err := tx.Exec(ctx, `INSERT INTO profile_mcp_servers(profile_id,server_id) SELECT $1,id FROM mcp_servers WHERE id=$2`, id, serverID); err != nil {
			return domain.Profile{}, err
		}
	}
	var skillCount, serverCount int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM profile_skills WHERE profile_id=$1),(SELECT count(*) FROM profile_mcp_servers WHERE profile_id=$1)`, id).Scan(&skillCount, &serverCount); err != nil {
		return domain.Profile{}, err
	}
	if skillCount != len(uniqueIDs(input.SkillIDs)) || serverCount != len(uniqueIDs(input.MCPServerIDs)) {
		return domain.Profile{}, ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Profile{}, err
	}
	return s.Profile(ctx, id)
}

func (s *Store) ListProfiles(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, profileSelect+` ORDER BY lower(p.name),p.id`)
}

const profileSelect = `SELECT p.id::text,p.name,p.description,p.revision,coalesce((SELECT jsonb_agg(skill_id::text ORDER BY skill_id::text) FROM profile_skills WHERE profile_id=p.id),'[]'::jsonb) AS "skillIds",coalesce((SELECT jsonb_agg(server_id::text ORDER BY server_id::text) FROM profile_mcp_servers WHERE profile_id=p.id),'[]'::jsonb) AS "mcpServerIds",p.created_at AS "createdAt",p.updated_at AS "updatedAt" FROM profiles p`

func (s *Store) Profile(ctx context.Context, id string) (domain.Profile, error) {
	var profile domain.Profile
	err := s.pool.QueryRow(ctx, `SELECT p.id::text,p.name,p.description,p.revision,ARRAY(SELECT skill_id::text FROM profile_skills WHERE profile_id=p.id ORDER BY skill_id::text),ARRAY(SELECT server_id::text FROM profile_mcp_servers WHERE profile_id=p.id ORDER BY server_id::text),p.created_at,p.updated_at FROM profiles p WHERE p.id=$1`, id).Scan(&profile.ID, &profile.Name, &profile.Description, &profile.Revision, &profile.SkillIDs, &profile.MCPServerIDs, &profile.CreatedAt, &profile.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Profile{}, ErrNotFound
	}
	return profile, err
}

func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	command, err := s.pool.Exec(ctx, `DELETE FROM profiles WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func uniqueIDs(ids []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}
