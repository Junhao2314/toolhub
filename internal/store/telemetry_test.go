package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func TestTelemetryIngestDeduplicatesBootSequenceAndResetsOnNewBoot(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, _, profile, _ := setupPublishedRelayProfile(t, st, "telemetry-dedupe")
	toolID := integrationToolID(t, st, server.ID, "read_item")
	day := time.Date(2026, 8, 16, 2, 3, 0, 0, time.UTC)
	firstBoot := uuid.NewString()

	first := bridgeprotocol.ObservationDrainResponse{
		BootID: firstBoot,
		Items: []bridgeprotocol.Observation{
			telemetryObservation(firstBoot, 1, day, profile.ID, profile.CurrentRevisionID, server.ID, toolID, "confirm", "confirmed", "none", "lt_10ms"),
			telemetryObservation(firstBoot, 2, day, profile.ID, profile.CurrentRevisionID, server.ID, toolID, "allow", "executed", "none", "lt_100ms"),
		},
		NextSequence: 2,
	}
	result, err := st.IngestRelayObservations(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 2 || result.Duplicates != 0 {
		t.Fatalf("first ingest=%+v", result)
	}
	result, err = st.IngestRelayObservations(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 0 || result.Duplicates != 2 {
		t.Fatalf("replayed ingest=%+v", result)
	}
	if calls := aggregateCallCount(t, st); calls != 2 {
		t.Fatalf("replayed observations produced callCount=%d, want 2", calls)
	}

	secondBoot := uuid.NewString()
	second := bridgeprotocol.ObservationDrainResponse{
		BootID: secondBoot,
		Items: []bridgeprotocol.Observation{
			telemetryObservation(secondBoot, 1, day.Add(time.Minute), profile.ID, profile.CurrentRevisionID, server.ID, toolID, "confirm", "rejected", "none", "lt_10ms"),
		},
		NextSequence: 1,
	}
	result, err = st.IngestRelayObservations(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 1 || aggregateCallCount(t, st) != 3 {
		t.Fatalf("new boot ingest=%+v calls=%d", result, aggregateCallCount(t, st))
	}
	cursor, err := st.RelayObservationCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.BootID != secondBoot || cursor.Sequence != 1 {
		t.Fatalf("cursor=%+v", cursor)
	}
}

func TestTelemetryIngestRollsBackCursorAndAggregatesTogether(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, _, profile, _ := setupPublishedRelayProfile(t, st, "telemetry-atomic")
	toolID := integrationToolID(t, st, server.ID, "read_item")
	bootID := uuid.NewString()
	observedAt := time.Date(2026, 8, 16, 4, 5, 0, 0, time.UTC)
	baseline := bridgeprotocol.ObservationDrainResponse{
		BootID: bootID,
		Items: []bridgeprotocol.Observation{
			telemetryObservation(bootID, 1, observedAt, profile.ID, profile.CurrentRevisionID, server.ID, toolID, "allow", "executed", "none", "lt_10ms"),
		},
		NextSequence: 1,
	}
	if _, err := st.IngestRelayObservations(ctx, baseline); err != nil {
		t.Fatal(err)
	}

	invalid := bridgeprotocol.ObservationDrainResponse{
		BootID: bootID,
		Items: []bridgeprotocol.Observation{
			telemetryObservation(bootID, 2, observedAt.Add(time.Minute), profile.ID, profile.CurrentRevisionID, server.ID, toolID, "allow", "failed", "upstream", "lt_1s"),
			telemetryObservation(bootID, 3, observedAt.Add(2*time.Minute), profile.ID, uuid.NewString(), server.ID, toolID, "allow", "executed", "none", "lt_10ms"),
		},
		NextSequence: 3,
	}
	if _, err := st.IngestRelayObservations(ctx, invalid); !errors.Is(err, ErrConflict) {
		t.Fatalf("invalid transactional batch returned %v", err)
	}
	if calls := aggregateCallCount(t, st); calls != 1 {
		t.Fatalf("failed batch changed aggregates to %d", calls)
	}
	cursor, err := st.RelayObservationCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.BootID != bootID || cursor.Sequence != 1 {
		t.Fatalf("failed batch changed cursor=%+v", cursor)
	}
}

func TestTelemetryRetentionDeletesOnlyBucketsOlderThanThirtyDays(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	server, _, profile, _ := setupPublishedRelayProfile(t, st, "telemetry-retention")
	toolID := integrationToolID(t, st, server.ID, "read_item")
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	bootID := uuid.NewString()
	response := bridgeprotocol.ObservationDrainResponse{
		BootID: bootID,
		Items: []bridgeprotocol.Observation{
			telemetryObservation(bootID, 1, now.AddDate(0, 0, -31), profile.ID, profile.CurrentRevisionID, server.ID, toolID, "confirm", "expired", "none", "lt_10ms"),
			telemetryObservation(bootID, 2, now.AddDate(0, 0, -30), profile.ID, profile.CurrentRevisionID, server.ID, toolID, "allow", "executed", "none", "lt_10ms"),
		},
		NextSequence: 2,
	}
	if _, err := st.IngestRelayObservations(ctx, response); err != nil {
		t.Fatal(err)
	}
	deleted, err := st.DeleteExpiredTelemetry(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 || aggregateCallCount(t, st) != 1 {
		t.Fatalf("retention deleted=%d remainingCalls=%d", deleted, aggregateCallCount(t, st))
	}
}

func TestGovernanceControlOperationsCoalesceWhileActive(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	first, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "relay_telemetry_pull", IdempotencyKey: "telemetry:first", Metadata: map[string]any{"scheduled": true}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateOperation(ctx, CreateOperationInput{Kind: "relay_telemetry_pull", IdempotencyKey: "telemetry:second", Metadata: map[string]any{"scheduled": true}})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("active telemetry controls did not coalesce: %s then %s", first.ID, second.ID)
	}
}

func TestGovernanceControlOperationsCoalesceConcurrently(t *testing.T) {
	ctx := context.Background()
	st := newIntegrationStore(t, true)
	const callers = 12
	start := make(chan struct{})
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			operation, err := st.CreateOperation(ctx, CreateOperationInput{
				Kind:           "contract_observe",
				IdempotencyKey: "contract-concurrent:" + uuid.NewString(),
				Metadata:       map[string]any{"caller": index},
			})
			if err != nil {
				errs <- err
				return
			}
			ids <- operation.ID
		}(index)
	}
	close(start)
	group.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent control creation failed: %v", err)
	}
	unique := map[string]struct{}{}
	for id := range ids {
		unique[id] = struct{}{}
	}
	if len(unique) != 1 {
		t.Fatalf("concurrent controls produced %d active operations: %v", len(unique), unique)
	}
}

func telemetryObservation(bootID string, sequence int64, at time.Time, profileID, profileRevisionID, serverID, toolID, decision, outcome, errorClass, durationBucket string) bridgeprotocol.Observation {
	return bridgeprotocol.Observation{
		BootID: bootID, Sequence: sequence, ObservedAt: float64(at.Unix()), MinuteBucket: at.UTC().Format("2006-01-02T15:04:00Z"),
		ProfileID: profileID, ProfileRevisionID: profileRevisionID, ServerID: serverID, ToolID: toolID,
		Decision: decision, Outcome: outcome, ErrorClass: errorClass, DurationBucket: durationBucket, ReasonCodes: []string{"test"},
	}
}

func integrationToolID(t *testing.T, st *Store, serverID, name string) string {
	t.Helper()
	var id string
	if err := st.pool.QueryRow(context.Background(), `SELECT id::text FROM mcp_tools WHERE server_id=$1 AND name=$2`, serverID, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func aggregateCallCount(t *testing.T, st *Store) int64 {
	t.Helper()
	var count int64
	if err := st.pool.QueryRow(context.Background(), `SELECT coalesce(sum(call_count),0) FROM mcp_daily_aggregates`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
