package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/toolhub-dev/toolhub/internal/domain"
	"github.com/toolhub-dev/toolhub/internal/security"
)

var ErrNotFound = errors.New("not found")

func (s *Store) UserByEmail(ctx context.Context, email string) (domain.User, error) {
	var user domain.User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id::text,u.email,u.display_name,u.password_hash,u.disabled,u.created_at,
		       coalesce(array_agg(r.name ORDER BY r.name) FILTER (WHERE r.name IS NOT NULL), ARRAY[]::text[])
		FROM users u LEFT JOIN user_roles ur ON ur.user_id=u.id LEFT JOIN roles r ON r.id=ur.role_id
		WHERE u.email=$1 GROUP BY u.id`, strings.ToLower(strings.TrimSpace(email))).Scan(
		&user.ID, &user.Email, &user.DisplayName, &user.PasswordHash, &user.Disabled, &user.CreatedAt, &user.Roles,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	return user, err
}

func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration, ip, userAgent string) (string, string, time.Time, error) {
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
	_, err = s.pool.Exec(ctx, `INSERT INTO sessions(id_hash,user_id,csrf_hash,expires_at,ip_address,user_agent)
		VALUES($1,$2,$3,$4,$5,$6)`, security.TokenHash(token), userID, security.TokenHash(csrf), expires, ipValue, truncate(userAgent, 500))
	return token, csrf, expires, err
}

func (s *Store) SessionPrincipal(ctx context.Context, token string) (domain.Principal, error) {
	var principal domain.Principal
	err := s.pool.QueryRow(ctx, `
		SELECT u.id::text,u.email,u.display_name,s.csrf_hash,s.expires_at,
		       coalesce(array_agg(r.name ORDER BY r.name) FILTER (WHERE r.name IS NOT NULL), ARRAY[]::text[])
		FROM sessions s JOIN users u ON u.id=s.user_id
		LEFT JOIN user_roles ur ON ur.user_id=u.id LEFT JOIN roles r ON r.id=ur.role_id
		WHERE s.id_hash=$1 AND s.expires_at>now() AND NOT u.disabled
		GROUP BY u.id,s.csrf_hash,s.expires_at`, security.TokenHash(token)).Scan(
		&principal.ID, &principal.Email, &principal.DisplayName, &principal.CSRFHash, &principal.ExpiresAt, &principal.Roles,
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
	command, err := s.pool.Exec(ctx, "UPDATE sessions SET csrf_hash=$2,last_seen_at=now() WHERE id_hash=$1 AND expires_at>now()", security.TokenHash(token), security.TokenHash(csrf))
	if err != nil {
		return "", err
	}
	if command.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return csrf, nil
}

func (s *Store) CreateUser(ctx context.Context, email, name, password, role string) (domain.User, error) {
	if role != "admin" && role != "operator" && role != "viewer" {
		return domain.User{}, errors.New("invalid role")
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return domain.User{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	user := domain.User{ID: uuid.NewString(), Email: strings.ToLower(strings.TrimSpace(email)), DisplayName: strings.TrimSpace(name), PasswordHash: hash, Roles: []string{role}, CreatedAt: time.Now().UTC()}
	if user.Email == "" || user.DisplayName == "" {
		return domain.User{}, errors.New("email and display name are required")
	}
	if _, err := tx.Exec(ctx, "INSERT INTO users(id,email,display_name,password_hash) VALUES($1,$2,$3,$4)", user.ID, user.Email, user.DisplayName, user.PasswordHash); err != nil {
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO user_roles(user_id,role_id) SELECT $1,id FROM roles WHERE name=$2", user.ID, role); err != nil {
		return domain.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, err
	}
	user.PasswordHash = ""
	return user, nil
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
