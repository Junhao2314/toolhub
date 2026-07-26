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
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/security"
)

var (
	ErrNotFound               = errors.New("not found")
	ErrInvalidCurrentPassword = errors.New("current password is incorrect")
	ErrUsernameUnavailable    = errors.New("username is unavailable")
	ErrEmailUnavailable       = errors.New("email is unavailable")
)

func (s *Store) UserByIdentifier(ctx context.Context, identifier string) (domain.User, error) {
	var user domain.User
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	err := s.pool.QueryRow(ctx, `
			SELECT u.id::text,u.username,u.email,u.display_name,u.password_hash,u.disabled,u.password_change_recommended,u.created_at,
			       coalesce(array_agg(r.name ORDER BY r.name) FILTER (WHERE r.name IS NOT NULL), ARRAY[]::text[])
			FROM users u LEFT JOIN user_roles ur ON ur.user_id=u.id LEFT JOIN roles r ON r.id=ur.role_id
			WHERE (position('@' in $1)>0 AND lower(u.email)=$1) OR (position('@' in $1)=0 AND lower(u.username)=$1)
			GROUP BY u.id`, identifier).Scan(
		&user.ID, &user.Username, &user.Email, &user.DisplayName, &user.PasswordHash, &user.Disabled, &user.PasswordChangeRecommended, &user.CreatedAt, &user.Roles,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	return user, err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (domain.User, error) {
	return s.UserByIdentifier(ctx, email)
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
			SELECT u.id::text,u.username,u.email,u.display_name,u.password_change_recommended,s.csrf_hash,s.expires_at,
		       coalesce(array_agg(r.name ORDER BY r.name) FILTER (WHERE r.name IS NOT NULL), ARRAY[]::text[])
		FROM sessions s JOIN users u ON u.id=s.user_id
		LEFT JOIN user_roles ur ON ur.user_id=u.id LEFT JOIN roles r ON r.id=ur.role_id
		WHERE s.id_hash=$1 AND s.expires_at>now() AND NOT u.disabled
		GROUP BY u.id,s.csrf_hash,s.expires_at`, security.TokenHash(token)).Scan(
		&principal.ID, &principal.Username, &principal.Email, &principal.DisplayName, &principal.PasswordChangeRecommended, &principal.CSRFHash, &principal.ExpiresAt, &principal.Roles,
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

func (s *Store) CreateUser(ctx context.Context, username, email, name, password, role string) (domain.User, error) {
	if role != "admin" && role != "operator" && role != "viewer" {
		return domain.User{}, errors.New("invalid role")
	}
	username, err := security.NormalizeUsername(username)
	if err != nil {
		return domain.User{}, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if email == "" || name == "" {
		return domain.User{}, errors.New("email and display name are required")
	}
	if strings.Count(email, "@") != 1 || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		return domain.User{}, errors.New("email is invalid")
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
	user := domain.User{ID: uuid.NewString(), Username: username, Email: email, DisplayName: name, PasswordHash: hash, Roles: []string{role}, PasswordChangeRecommended: true, CreatedAt: time.Now().UTC()}
	if _, err := tx.Exec(ctx, "INSERT INTO users(id,username,email,display_name,password_hash,password_change_recommended) VALUES($1,$2,$3,$4,$5,true)", user.ID, user.Username, user.Email, user.DisplayName, user.PasswordHash); err != nil {
		return domain.User{}, mapUserWriteError(err)
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

func (s *Store) UpdateOwnUsername(ctx context.Context, userID, currentPassword, username string) error {
	username, err := security.NormalizeUsername(username)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := verifyCurrentPassword(ctx, tx, userID, currentPassword); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET username=$2,updated_at=now() WHERE id=$1", userID, username); err != nil {
		return mapUserWriteError(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) UpdateOwnPassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	hash, err := security.HashPassword(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := verifyCurrentPassword(ctx, tx, userID, currentPassword); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET password_hash=$2,password_change_recommended=false,updated_at=now() WHERE id=$1", userID, hash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ResetUserPassword(ctx context.Context, userID, password string) error {
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, "UPDATE users SET password_hash=$2,password_change_recommended=true,updated_at=now() WHERE id=$1", userID, hash)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func verifyCurrentPassword(ctx context.Context, tx pgx.Tx, userID, password string) error {
	var hash string
	if err := tx.QueryRow(ctx, "SELECT password_hash FROM users WHERE id=$1 AND NOT disabled FOR UPDATE", userID).Scan(&hash); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	valid, err := security.VerifyPassword(hash, password)
	if err != nil || !valid {
		return ErrInvalidCurrentPassword
	}
	return nil
}

func mapUserWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return fmt.Errorf("write user: %w", err)
	}
	if strings.Contains(pgErr.ConstraintName, "username") {
		return ErrUsernameUnavailable
	}
	if strings.Contains(pgErr.ConstraintName, "email") {
		return ErrEmailUnavailable
	}
	return fmt.Errorf("write user: %w", err)
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
