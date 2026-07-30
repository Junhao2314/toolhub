package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateSecret(ctx context.Context, name, kind string, value []byte, metadata map[string]any) (string, error) {
	id := uuid.NewString()
	ciphertext, err := s.cipher.Encrypt(value, id)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO encrypted_secrets(id,name,kind,ciphertext,metadata) VALUES($1,$2,$3,$4,$5)`, id, name, kind, ciphertext, jsonText(encoded))
	return id, err
}

func (s *Store) secretValue(ctx context.Context, id string) ([]byte, error) {
	var ciphertext []byte
	err := s.pool.QueryRow(ctx, "SELECT ciphertext FROM encrypted_secrets WHERE id=$1", id).Scan(&ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.cipher.Decrypt(ciphertext, id)
}

// SecretValues resolves only IDs already authorized by a validated desired manifest.
// It must never be called with browser-provided IDs directly.
func (s *Store) SecretValues(ctx context.Context, ids []string) (map[string]string, error) {
	values := make(map[string]string, len(ids))
	for _, id := range ids {
		if uuid.Validate(id) != nil {
			return nil, errors.New("invalid secret reference")
		}
		value, err := s.secretValue(ctx, id)
		if err != nil {
			return nil, err
		}
		values[id] = string(value)
	}
	return values, nil
}
