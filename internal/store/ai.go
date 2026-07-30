package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
)

func (s *Store) DefaultAIProvider(ctx context.Context) (domain.AIProvider, error) {
	var provider domain.AIProvider
	var secretID string
	err := s.pool.QueryRow(ctx, `SELECT id::text,name,base_url,model,api_key_secret_id::text FROM ai_providers WHERE enabled AND is_default LIMIT 1`).Scan(&provider.ID, &provider.Name, &provider.BaseURL, &provider.Model, &secretID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AIProvider{}, ErrNotFound
	}
	if err != nil {
		return domain.AIProvider{}, err
	}
	value, err := s.secretValue(ctx, secretID)
	if err != nil {
		return domain.AIProvider{}, err
	}
	provider.APIKey = string(value)
	return provider, nil
}
