package worker

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/store"
)

type fakeSchedulerStore struct {
	settings     domain.Settings
	relayHealthy bool
	created      []store.CreateOperationInput
}

func (fake *fakeSchedulerStore) Settings(context.Context) (domain.Settings, error) {
	return fake.settings, nil
}

func (fake *fakeSchedulerStore) EnqueueReconciles(context.Context) (int, error) { return 0, nil }

func (fake *fakeSchedulerStore) RelayGovernanceHealthy(context.Context) (bool, error) {
	return fake.relayHealthy, nil
}

func (fake *fakeSchedulerStore) CreateOperation(_ context.Context, input store.CreateOperationInput) (domain.Operation, error) {
	fake.created = append(fake.created, input)
	return domain.Operation{}, nil
}

func TestEnqueueScheduledUpdateUsesMinuteIdempotency(t *testing.T) {
	fake := &fakeSchedulerStore{}
	scheduler := &Scheduler{store: fake, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time {
		return time.Date(2026, 7, 30, 12, 34, 56, 0, time.FixedZone("CST", 8*60*60))
	}}
	scheduler.enqueueScheduledUpdate(context.Background())
	if len(fake.created) != 1 {
		t.Fatalf("created=%d", len(fake.created))
	}
	input := fake.created[0]
	if input.Kind != "update_check" || input.IdempotencyKey != "scheduled-update:20260730T0434Z" {
		t.Fatalf("scheduled operation=%+v", input)
	}
}

func TestEnqueueBackupGCUsesDailyIdempotencyAndFixedRetention(t *testing.T) {
	fake := &fakeSchedulerStore{}
	scheduler := &Scheduler{store: fake, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time {
		return time.Date(2026, 7, 30, 23, 45, 0, 0, time.FixedZone("CST", 8*60*60))
	}}
	scheduler.enqueueBackupGC(context.Background())
	if len(fake.created) != 1 {
		t.Fatalf("created=%d", len(fake.created))
	}
	input := fake.created[0]
	if input.Kind != "backup_gc" || input.IdempotencyKey != "scheduled-backup-gc:2026-07-30" {
		t.Fatalf("scheduled backup GC=%+v", input)
	}
	request, ok := input.Request.(map[string]any)
	if !ok || request["maxAgeDays"] != 30 || request["maxPerTarget"] != 10 {
		t.Fatalf("backup GC request=%#v", input.Request)
	}
}

func TestSchedulerEnqueuesHealthyContractObservationOnBoundedCadence(t *testing.T) {
	fake := &fakeSchedulerStore{relayHealthy: true}
	scheduler := &Scheduler{store: fake, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time {
		return time.Date(2026, 8, 16, 12, 44, 29, 0, time.UTC)
	}}
	scheduler.enqueueContractObservation(context.Background())
	if len(fake.created) != 1 {
		t.Fatalf("created=%d", len(fake.created))
	}
	input := fake.created[0]
	if input.Kind != "contract_observe" || input.IdempotencyKey != "scheduled-contract-observe:20260816T1230Z" {
		t.Fatalf("contract observation=%+v", input)
	}

	fake.relayHealthy = false
	scheduler.enqueueContractObservation(context.Background())
	if len(fake.created) != 1 {
		t.Fatalf("unhealthy relay queued another observation: %d", len(fake.created))
	}
}

func TestSchedulerEnqueuesTelemetryPullWithMinuteIdempotency(t *testing.T) {
	fake := &fakeSchedulerStore{}
	scheduler := &Scheduler{store: fake, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time {
		return time.Date(2026, 8, 16, 12, 44, 29, 0, time.UTC)
	}}
	scheduler.enqueueTelemetryPull(context.Background())
	if len(fake.created) != 1 {
		t.Fatalf("created=%d", len(fake.created))
	}
	input := fake.created[0]
	if input.Kind != "relay_telemetry_pull" || input.IdempotencyKey != "scheduled-relay-telemetry:20260816T1244Z" {
		t.Fatalf("telemetry pull=%+v", input)
	}
}
