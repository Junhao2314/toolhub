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
	CreateOperation(context.Context, store.CreateOperationInput) (domain.Operation, error)
}

type Scheduler struct {
	store           schedulerStore
	logger          *slog.Logger
	now             func() time.Time
	reconcileEvery  time.Duration
	settingsRefresh time.Duration
	backupGCEvery   time.Duration
}

func NewScheduler(st *store.Store, logger *slog.Logger) *Scheduler {
	return &Scheduler{store: st, logger: logger, now: time.Now, reconcileEvery: 5 * time.Minute, settingsRefresh: 30 * time.Second, backupGCEvery: 24 * time.Hour}
}

func (s *Scheduler) Run(ctx context.Context) {
	reconcile := time.NewTicker(s.reconcileEvery)
	defer reconcile.Stop()
	settingsRefresh := time.NewTicker(s.settingsRefresh)
	defer settingsRefresh.Stop()
	backupGC := time.NewTicker(s.backupGCEvery)
	defer backupGC.Stop()
	updates := cron.New()
	var updateEntry cron.EntryID
	var updateSignature string
	s.reloadUpdateSchedule(ctx, updates, &updateEntry, &updateSignature)
	s.enqueueBackupGC(ctx)
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
		}
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
