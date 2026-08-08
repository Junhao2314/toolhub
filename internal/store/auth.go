package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/security"
)

func (s *Store) AccountByUsername(ctx context.Context, username string) (domain.Account, error) {
	var account domain.Account
	username = strings.ToLower(strings.TrimSpace(username))
	err := s.pool.QueryRow(ctx, `SELECT username,password_hash,password_change_recommended,password_changed_at,created_at,updated_at FROM account WHERE username=$1`, username).Scan(
		&account.Username, &account.PasswordHash, &account.PasswordChangeRecommended, &account.PasswordChangedAt, &account.CreatedAt, &account.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, ErrNotFound
	}
	return account, err
}

func (s *Store) Account(ctx context.Context) (domain.Account, error) {
	var account domain.Account
	err := s.pool.QueryRow(ctx, `SELECT username,password_hash,password_change_recommended,password_changed_at,created_at,updated_at FROM account WHERE singleton`).Scan(
		&account.Username, &account.PasswordHash, &account.PasswordChangeRecommended, &account.PasswordChangedAt, &account.CreatedAt, &account.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, ErrNotFound
	}
	return account, err
}

func (s *Store) CreateSession(ctx context.Context, ttl time.Duration, ip, userAgent string) (string, string, time.Time, error) {
	token, err := security.RandomToken(32)
	if err != nil {
		return "", "", time.Time{}, err
	}
	csrf, err := security.RandomToken(24)
	if err != nil {
		return "", "", time.Time{}, err
	}
	expires := time.Now().UTC().Add(ttl)
	var ipValue any
	if parsed := net.ParseIP(ip); parsed != nil {
		ipValue = parsed.String()
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO sessions(id_hash,csrf_hash,expires_at,ip_address,user_agent) VALUES($1,$2,$3,$4,$5)`, security.TokenHash(token), security.TokenHash(csrf), expires, ipValue, truncate(userAgent, 500))
	return token, csrf, expires, err
}

func (s *Store) SessionPrincipal(ctx context.Context, token string) (domain.Principal, error) {
	var principal domain.Principal
	err := s.pool.QueryRow(ctx, `SELECT a.username,a.password_change_recommended,s.csrf_hash,s.expires_at FROM sessions s CROSS JOIN account a WHERE s.id_hash=$1 AND s.expires_at>now()`, security.TokenHash(token)).Scan(
		&principal.Username, &principal.PasswordChangeRecommended, &principal.CSRFHash, &principal.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Principal{}, ErrNotFound
	}
	if err == nil {
		_, _ = s.pool.Exec(ctx, "UPDATE sessions SET last_seen_at=now() WHERE id_hash=$1 AND last_seen_at<now()-interval '5 minutes'", security.TokenHash(token))
	}
	return principal, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE id_hash=$1", security.TokenHash(token))
	return err
}

func (s *Store) RotateSessionCSRF(ctx context.Context, token string) (string, error) {
	csrf, err := security.RandomToken(24)
	if err != nil {
		return "", err
	}
	command, err := s.pool.Exec(ctx, "UPDATE sessions SET csrf_hash=$2 WHERE id_hash=$1 AND expires_at>now()", security.TokenHash(token), security.TokenHash(csrf))
	if err != nil {
		return "", err
	}
	if command.RowsAffected() != 1 {
		return "", ErrNotFound
	}
	return csrf, nil
}

func (s *Store) UpdateUsername(ctx context.Context, username, currentPassword string) error {
	username, err := security.NormalizeUsername(username)
	if err != nil {
		return err
	}
	return s.updateCredentials(ctx, currentPassword, func(tx pgx.Tx, hash string) error {
		_, err := tx.Exec(ctx, "UPDATE account SET username=$1,updated_at=now() WHERE singleton", username)
		if isUniqueViolation(err) {
			return ErrUsernameUnavailable
		}
		return err
	})
}

func (s *Store) UpdatePassword(ctx context.Context, currentPassword, newPassword string) error {
	newHash, err := security.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.updateCredentials(ctx, currentPassword, func(tx pgx.Tx, _ string) error {
		_, err := tx.Exec(ctx, `UPDATE account SET password_hash=$1,password_change_recommended=false,password_changed_at=now(),updated_at=now() WHERE singleton`, newHash)
		return err
	})
}

func (s *Store) VerifyCurrentPassword(ctx context.Context, currentPassword string) error {
	var hash string
	if err := s.pool.QueryRow(ctx, "SELECT password_hash FROM account WHERE singleton").Scan(&hash); err != nil {
		return err
	}
	valid, err := security.VerifyPassword(hash, currentPassword)
	if err != nil || !valid {
		return ErrInvalidCurrentPassword
	}
	return nil
}

func (s *Store) updateCredentials(ctx context.Context, currentPassword string, update func(pgx.Tx, string) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var hash string
	if err := tx.QueryRow(ctx, "SELECT password_hash FROM account WHERE singleton FOR UPDATE").Scan(&hash); err != nil {
		return err
	}
	valid, verifyErr := security.VerifyPassword(hash, currentPassword)
	if verifyErr != nil || !valid {
		return ErrInvalidCurrentPassword
	}
	if err := update(tx, hash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM sessions"); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE expires_at<=now()")
	return err
}
