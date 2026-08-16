package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/store"
)

type schedulerStore interface {
	Settings(context.Context) (domain.Settings, error)
	EnqueueReconciles(context.Context) (int, error)
	RelayGovernanceHealthy(context.Context) (bool, error)
	CreateOperation(context.Context, store.CreateOperationInput) (domain.Operation, error)
}

type Scheduler struct {
	store           schedulerStore
	logger          *slog.Logger
	now             func() time.Time
	reconcileEvery  time.Duration
	settingsRefresh time.Duration
	backupGCEvery   time.Duration
	contractEvery   time.Duration
	telemetryEvery  time.Duration
}

func NewScheduler(st *store.Store, logger *slog.Logger) *Scheduler {
	return &Scheduler{store: st, logger: logger, now: time.Now, reconcileEvery: 5 * time.Minute, settingsRefresh: 30 * time.Second, backupGCEvery: 24 * time.Hour, contractEvery: 30 * time.Minute, telemetryEvery: time.Minute}
}

func (s *Scheduler) Run(ctx context.Context) {
	reconcile := time.NewTicker(s.reconcileEvery)
	defer reconcile.Stop()
	settingsRefresh := time.NewTicker(s.settingsRefresh)
	defer settingsRefresh.Stop()
	backupGC := time.NewTicker(s.backupGCEvery)
	defer backupGC.Stop()
	contractObservation := time.NewTicker(s.contractEvery)
	defer contractObservation.Stop()
	telemetryPull := time.NewTicker(s.telemetryEvery)
	defer telemetryPull.Stop()
	updates := cron.New()
	var updateEntry cron.EntryID
	var updateSignature string
	s.reloadUpdateSchedule(ctx, updates, &updateEntry, &updateSignature)
	s.enqueueBackupGC(ctx)
	s.enqueueContractObservation(ctx)
	s.enqueueTelemetryPull(ctx)
	updates.Start()
	defer updates.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcile.C:
			if count, err := s.store.EnqueueReconciles(ctx); err != nil {
				s.logger.Error("enqueue scheduled reconcile", "error", err)
			} else if count > 0 {
				s.logger.Info("scheduled reconcile queued", "targets", count)
			}
		case <-settingsRefresh.C:
			s.reloadUpdateSchedule(ctx, updates, &updateEntry, &updateSignature)
		case <-backupGC.C:
			s.enqueueBackupGC(ctx)
		case <-contractObservation.C:
			s.enqueueContractObservation(ctx)
		case <-telemetryPull.C:
			s.enqueueTelemetryPull(ctx)
		}
	}
}

func (s *Scheduler) enqueueContractObservation(ctx context.Context) {
	healthy, err := s.store.RelayGovernanceHealthy(ctx)
	if err != nil {
		s.logger.Error("check relay health for contract observation", "error", err)
		return
	}
	if !healthy {
		return
	}
	bucket := s.now().UTC().Truncate(30 * time.Minute)
	_, err = s.store.CreateOperation(ctx, store.CreateOperationInput{
		Kind:           "contract_observe",
		IdempotencyKey: "scheduled-contract-observe:" + bucket.Format("20060102T1504Z"),
		Request:        map[string]any{"scheduled": true, "scheduledAt": bucket},
		Metadata:       map[string]any{"scheduled": true, "scheduledAt": bucket},
	})
	if err != nil {
		s.logger.Error("enqueue relay contract observation", "error", err)
	}
}

func (s *Scheduler) enqueueTelemetryPull(ctx context.Context) {
	healthy, err := s.store.RelayGovernanceHealthy(ctx)
	if err != nil {
		s.logger.Error("check relay health for telemetry pull", "error", err)
		return
	}
	if !healthy {
		return
	}
	scheduledAt := s.now().UTC().Truncate(time.Minute)
	_, err = s.store.CreateOperation(ctx, store.CreateOperationInput{
		Kind:           "relay_telemetry_pull",
		IdempotencyKey: "scheduled-relay-telemetry:" + scheduledAt.Format("20060102T1504Z"),
		Request:        map[string]any{"scheduled": true, "scheduledAt": scheduledAt},
		Metadata:       map[string]any{"scheduled": true, "scheduledAt": scheduledAt},
	})
	if err != nil {
		s.logger.Error("enqueue relay telemetry pull", "error", err)
	}
}

func (s *Scheduler) enqueueBackupGC(ctx context.Context) {
	day := s.now().UTC().Format("2006-01-02")
	_, err := s.store.CreateOperation(ctx, store.CreateOperationInput{
		Kind:           "backup_gc",
		IdempotencyKey: "scheduled-backup-gc:" + day,
		Request:        map[string]any{"maxAgeDays": 30, "maxPerTarget": 10},
		Metadata:       map[string]any{"scheduled": true, "retentionDays": 30, "maxPerTarget": 10},
	})
	if err != nil {
		s.logger.Error("enqueue scheduled backup GC", "error", err)
	}
}

func (s *Scheduler) reloadUpdateSchedule(ctx context.Context, runner *cron.Cron, entry *cron.EntryID, signature *string) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.logger.Error("load update schedule", "error", err)
		return
	}
	nextSignature := settings.Timezone + "\x00" + settings.UpdateCron
	if nextSignature == *signature {
		return
	}
	nextEntry, err := runner.AddFunc("CRON_TZ="+settings.Timezone+" "+settings.UpdateCron, func() {
		s.enqueueScheduledUpdate(ctx)
	})
	if err != nil {
		s.logger.Error("configure update schedule", "error", err)
		return
	}
	if *entry != 0 {
		runner.Remove(*entry)
	}
	*entry, *signature = nextEntry, nextSignature
	s.logger.Info("Library update schedule loaded", "schedule", settings.UpdateCron, "timezone", settings.Timezone)
}

func (s *Scheduler) enqueueScheduledUpdate(ctx context.Context) {
	scheduledAt := s.now().UTC().Truncate(time.Minute)
	_, err := s.store.CreateOperation(ctx, store.CreateOperationInput{
		Kind:           "update_check",
		IdempotencyKey: "scheduled-update:" + scheduledAt.Format("20060102T1504Z"),
		Request:        map[string]any{"scheduled": true, "scheduledAt": scheduledAt},
		Metadata:       map[string]any{"scheduled": true, "scheduledAt": scheduledAt},
	})
	if err != nil {
		s.logger.Error("enqueue scheduled Library update check", "error", err)
		return
	}
	s.logger.Info("scheduled Library update check queued", "scheduledAt", scheduledAt)
}
