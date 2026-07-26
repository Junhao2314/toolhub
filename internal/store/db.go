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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Junhao2314/toolhub/internal/security"
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

func (s *Store) Close() { s.pool.Close() }

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
	if _, err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version bigint PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
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
		var applied bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, migrationExecutionSQL(entry.Name(), body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func migrationExecutionSQL(name string, body []byte) string {
	sql := string(body)
	if name != "006_orchestration_leases_generations.sql" {
		return sql
	}
	// Migration 006 was applied to the live database from a transient version
	// whose JSON extractions were parenthesized. The checked-in applied file is
	// immutable, but its unparenthesized concatenation is parsed by PostgreSQL as
	// (text || jsonb) ->> key on a clean install. Reproduce the already-applied
	// semantics only at execution time for databases that do not yet have v006.
	const ambiguous = "kind || ':' || payload->>'deploymentId' || ':' || payload->>'desiredGeneration'"
	const corrected = "kind || ':' || (payload->>'deploymentId') || ':' || (payload->>'desiredGeneration')"
	return strings.ReplaceAll(sql, ambiguous, corrected)
}

func (s *Store) BootstrapAdmin(ctx context.Context, username, email, name, password string) (bool, error) {
	var count int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	username, err := security.NormalizeUsername(username)
	if err != nil {
		return false, fmt.Errorf("validate bootstrap username: %w", err)
	}
	if strings.TrimSpace(email) == "" || strings.TrimSpace(name) == "" || password == "" {
		return false, errors.New("bootstrap admin username, email, name, and password are required on first startup")
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
	userID := uuid.NewString()
	if _, err := tx.Exec(ctx, "INSERT INTO users(id,username,email,display_name,password_hash) VALUES($1,$2,$3,$4,$5)", userID, username, strings.ToLower(strings.TrimSpace(email)), strings.TrimSpace(name), hash); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO user_roles(user_id,role_id) SELECT $1,id FROM roles WHERE name='admin'", userID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO audit_events(id,actor_user_id,action,resource_type,resource_id,outcome,metadata) VALUES($1,$2,'bootstrap','user',$2,'success','{}')", uuid.NewString(), userID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
