package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
)

var subagentBundleSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func normalizeSubagentBundle(name string, values []string) ([]string, error) {
	if len(values) > 100 {
		return nil, fmt.Errorf("%s bundle may contain at most 100 Skills", name)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		slug := strings.TrimSpace(raw)
		if slug == "" {
			continue
		}
		if !subagentBundleSlugPattern.MatchString(slug) {
			return nil, fmt.Errorf("%s bundle contains invalid Skill slug %q", name, slug)
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		result = append(result, slug)
	}
	return result, nil
}

func (s *Store) Settings(ctx context.Context) (domain.Settings, error) {
	var value domain.Settings
	err := s.pool.QueryRow(ctx, `SELECT managed_username,update_cron,timezone,relay_port,relay_intentional_paused,kimi_frontend_bundle,pi_bundle,updated_at FROM settings WHERE singleton`).Scan(&value.ManagedUsername, &value.UpdateCron, &value.Timezone, &value.RelayPort, &value.RelayIntentionalPaused, &value.KimiFrontendBundle, &value.PiBundle, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Settings{}, ErrNotFound
	}
	return value, err
}

func (s *Store) UpdateSettings(ctx context.Context, value domain.Settings) (domain.Settings, error) {
	value.ManagedUsername = strings.TrimSpace(value.ManagedUsername)
	value.UpdateCron = strings.TrimSpace(value.UpdateCron)
	value.Timezone = strings.TrimSpace(value.Timezone)
	kimiBundle, err := normalizeSubagentBundle("Kimi frontend", value.KimiFrontendBundle)
	if err != nil {
		return domain.Settings{}, err
	}
	piBundle, err := normalizeSubagentBundle("Pi", value.PiBundle)
	if err != nil {
		return domain.Settings{}, err
	}
	if len(kimiBundle) > 0 && !containsSubagentBundleSlug(kimiBundle, "ui-ux-pro-max-cn") {
		return domain.Settings{}, errors.New("Kimi frontend bundle must include ui-ux-pro-max-cn")
	}
	value.KimiFrontendBundle = kimiBundle
	value.PiBundle = piBundle
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
	if _, err := tx.Exec(ctx, `UPDATE settings SET managed_username=$1,update_cron=$2,timezone=$3,relay_port=$4,relay_intentional_paused=$5,kimi_frontend_bundle=$6,pi_bundle=$7,updated_at=now() WHERE singleton`, value.ManagedUsername, value.UpdateCron, value.Timezone, value.RelayPort, value.RelayIntentionalPaused, value.KimiFrontendBundle, value.PiBundle); err != nil {
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

func containsSubagentBundleSlug(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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
