package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
)

func (s *Store) Settings(ctx context.Context) (domain.Settings, error) {
	var value domain.Settings
	err := s.pool.QueryRow(ctx, `SELECT managed_username,update_cron,timezone,relay_port,relay_intentional_paused,updated_at FROM settings WHERE singleton`).Scan(&value.ManagedUsername, &value.UpdateCron, &value.Timezone, &value.RelayPort, &value.RelayIntentionalPaused, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Settings{}, ErrNotFound
	}
	return value, err
}

func (s *Store) UpdateSettings(ctx context.Context, value domain.Settings) (domain.Settings, error) {
	value.ManagedUsername = strings.TrimSpace(value.ManagedUsername)
	value.UpdateCron = strings.TrimSpace(value.UpdateCron)
	value.Timezone = strings.TrimSpace(value.Timezone)
	if value.ManagedUsername == "" || value.UpdateCron == "" || value.Timezone == "" || value.RelayPort < 1 || value.RelayPort > 65535 {
		return domain.Settings{}, errors.New("settings are invalid")
	}
	if err := bridgeprotocol.ValidateManagedUsername(value.ManagedUsername); err != nil {
		return domain.Settings{}, err
	}
	if _, err := cron.ParseStandard(value.UpdateCron); err != nil {
		return domain.Settings{}, errors.New("update schedule must be a valid five-field cron expression")
	}
	if _, err := time.LoadLocation(value.Timezone); err != nil {
		return domain.Settings{}, errors.New("settings timezone is invalid")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Settings{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE settings SET managed_username=$1,update_cron=$2,timezone=$3,relay_port=$4,relay_intentional_paused=$5,updated_at=now() WHERE singleton`, value.ManagedUsername, value.UpdateCron, value.Timezone, value.RelayPort, value.RelayIntentionalPaused); err != nil {
		return domain.Settings{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE targets t SET managed_username=coalesce(n.managed_username_override,$1),updated_at=now() FROM nodes n WHERE n.id=t.node_id`, value.ManagedUsername); err != nil {
		return domain.Settings{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Settings{}, err
	}
	return s.Settings(ctx)
}

func (s *Store) SetRelayIntentionalPaused(ctx context.Context, paused bool) error {
	command, err := s.pool.Exec(ctx, `UPDATE settings SET relay_intentional_paused=$1,updated_at=now() WHERE singleton`, paused)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}
