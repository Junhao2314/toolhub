package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Junhao2314/toolhub/internal/store"
)

type Scheduler struct {
	store  *store.Store
	logger *slog.Logger
	mu     sync.Mutex
	cron   *cron.Cron
}

func NewScheduler(st *store.Store, logger *slog.Logger) *Scheduler {
	return &Scheduler{store: st, logger: logger}
}

func (s *Scheduler) Run(ctx context.Context) {
	s.reload(ctx)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.stop()
			return
		case <-ticker.C:
			s.reload(ctx)
		}
	}
}

func (s *Scheduler) reload(ctx context.Context) {
	schedules, err := s.store.Schedules(ctx)
	if err != nil {
		s.logger.Error("load schedules", "error", err)
		return
	}
	next := cron.New()
	for _, schedule := range schedules {
		current := schedule
		spec := "CRON_TZ=" + current.Timezone + " " + current.Spec
		if _, err := next.AddFunc(spec, func() {
			payload := map[string]any{"scopeType": current.ScopeType, "scopeId": current.ScopeID, "scheduled": true}
			if _, err := s.store.EnqueueJob(context.Background(), current.Kind, payload, false, ""); err != nil {
				s.logger.Error("enqueue scheduled job", "kind", current.Kind, "scope", current.ScopeType, "error", err)
			}
		}); err != nil {
			s.logger.Error("invalid schedule", "kind", current.Kind, "spec", spec, "error", err)
		}
	}
	next.Start()
	s.mu.Lock()
	previous := s.cron
	s.cron = next
	s.mu.Unlock()
	if previous != nil {
		previous.Stop()
	}
}

func (s *Scheduler) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
	}
}
