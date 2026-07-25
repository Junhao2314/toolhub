package store

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type AIProvider struct {
	ID      string
	Name    string
	BaseURL string
	Model   string
	APIKey  string
}

func (s *Store) CreateAIProvider(ctx context.Context, name, baseURL, model, apiKey, actor string, makeDefault bool) (string, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(baseURL) == "" || strings.TrimSpace(model) == "" || apiKey == "" {
		return "", errors.New("name, base URL, model, and API key are required")
	}
	secretID, err := s.CreateSecret(ctx, "ai-provider:"+strings.TrimSpace(name), "ai-api-key", []byte(apiKey), map[string]any{"provider": name}, actor)
	if err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if makeDefault {
		if _, err := tx.Exec(ctx, "UPDATE ai_providers SET is_default=false"); err != nil {
			return "", err
		}
	}
	id := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO ai_providers(id,name,base_url,model,api_key_secret_id,is_default)
		VALUES($1,$2,$3,$4,$5,$6)`, id, strings.TrimSpace(name), strings.TrimRight(strings.TrimSpace(baseURL), "/"), strings.TrimSpace(model), secretID, makeDefault); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) DefaultAIProvider(ctx context.Context) (AIProvider, error) {
	var provider AIProvider
	var secretID string
	err := s.pool.QueryRow(ctx, `SELECT id::text,name,base_url,model,api_key_secret_id::text FROM ai_providers
		WHERE enabled ORDER BY is_default DESC,created_at LIMIT 1`).Scan(&provider.ID, &provider.Name, &provider.BaseURL, &provider.Model, &secretID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AIProvider{}, ErrNotFound
	}
	if err != nil {
		return AIProvider{}, err
	}
	key, err := s.SecretValue(ctx, secretID)
	if err != nil {
		return AIProvider{}, err
	}
	provider.APIKey = string(key)
	return provider, nil
}
