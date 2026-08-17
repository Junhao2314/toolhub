package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Junhao2314/toolhub/internal/security"
)

const SchemaGeneration = 2

var (
	ErrNotFound               = errors.New("not found")
	ErrConflict               = errors.New("state conflict")
	ErrFinalizationDeferred   = errors.New("governance finalization deferred")
	ErrLegacySchema           = errors.New("legacy database schema")
	ErrInvalidCurrentPassword = errors.New("current password is incorrect")
	ErrUsernameUnavailable    = errors.New("username is unavailable")
	ErrOperationActive        = errors.New("target already has an active operation")
	ErrIdempotencyConflict    = errors.New("idempotency key was reused with a different request")
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	pool   *pgxpool.Pool
	cipher *security.Cipher
}

func Open(ctx context.Context, databaseURL string, cipher *security.Cipher) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool, cipher: cipher}, nil
}

func (s *Store) Close()              { s.pool.Close() }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(18480)"); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock(18480)") }()

	fresh, err := inspectSchemaGeneration(ctx, conn)
	if err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		prefix := strings.SplitN(entry.Name(), "_", 2)[0]
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid migration name %q", entry.Name())
		}
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version bigint PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		var applied bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", version).Scan(&applied); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if !applied {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", version); err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		fresh = false
	}
	_ = fresh
	return s.RequireSchemaGeneration(ctx)
}

func inspectSchemaGeneration(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (bool, error) {
	var tableCount int
	if err := query.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname='public'`).Scan(&tableCount); err != nil {
		return false, err
	}
	if tableCount == 0 {
		return true, nil
	}
	var appMetaExists bool
	if err := query.QueryRow(ctx, "SELECT to_regclass('public.app_meta') IS NOT NULL").Scan(&appMetaExists); err != nil {
		return false, err
	}
	if !appMetaExists {
		return false, legacySchemaError()
	}
	var generation string
	if err := query.QueryRow(ctx, "SELECT value FROM app_meta WHERE key='schema_generation'").Scan(&generation); err != nil || generation != strconv.Itoa(SchemaGeneration) {
		return false, legacySchemaError()
	}
	return false, nil
}

func legacySchemaError() error {
	return fmt.Errorf("%w: ToolHub generation %d requires a fresh PostgreSQL volume; back up and replace the existing database volume before starting", ErrLegacySchema, SchemaGeneration)
}

func (s *Store) RequireSchemaGeneration(ctx context.Context) error {
	var value string
	if err := s.pool.QueryRow(ctx, "SELECT value FROM app_meta WHERE key='schema_generation'").Scan(&value); err != nil || value != strconv.Itoa(SchemaGeneration) {
		return legacySchemaError()
	}
	return nil
}

func (s *Store) BootstrapAccount(ctx context.Context, username, password string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM account)").Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	username, err := security.NormalizeUsername(username)
	if err != nil {
		return false, fmt.Errorf("validate bootstrap username: %w", err)
	}
	if password == "" {
		return false, errors.New("TOOLHUB_BOOTSTRAP_PASSWORD is required on first startup")
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return false, fmt.Errorf("hash bootstrap password: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "INSERT INTO account(singleton,username,password_hash) VALUES(true,$1,$2) ON CONFLICT(singleton) DO NOTHING", username, hash); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata) VALUES($1,'bootstrap','account','singleton','success','{}')`, uuid.NewString()); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
