package store

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

const maxRelayObservationBatch = 1000

var telemetryDecisions = map[string]bool{"allow": true, "confirm": true, "deny": true}
var telemetryOutcomes = map[string]bool{
	"confirmation_required": true, "confirmed": true, "rejected": true,
	"expired": true, "denied": true, "not_executed": true,
	"executed": true, "failed": true, "unknown": true,
}
var telemetryErrorClasses = map[string]bool{
	"none": true, "policy": true, "confirmation": true, "rate_limited": true,
	"timeout": true, "transport": true, "upstream": true, "internal": true,
}
var telemetryDurationBuckets = map[string]bool{
	"lt_10ms": true, "lt_100ms": true, "lt_1s": true, "lt_10s": true, "gte_10s": true,
}

type RelayObservationCursor struct {
	BootID   string
	Sequence int64
}

type TelemetryIngestResult struct {
	Accepted   int
	Duplicates int
	Cursor     RelayObservationCursor
}

func (s *Store) RelayObservationCursor(ctx context.Context) (RelayObservationCursor, error) {
	var servers, missing, boots int
	var bootID string
	var sequence int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE coalesce(cursor.boot_id,'')=''),
		       count(DISTINCT nullif(cursor.boot_id,'')),
		       coalesce(min(nullif(cursor.boot_id,'')),''),
		       coalesce(min(coalesce(cursor.cursor,0)),0)
		FROM relay_configuration_state state
		JOIN relay_configuration_revision_mcp_servers member
		  ON member.relay_configuration_revision_id=state.applied_revision_id
		LEFT JOIN relay_observation_cursors cursor ON cursor.server_id=member.server_id
		WHERE state.singleton`).Scan(&servers, &missing, &boots, &bootID, &sequence)
	if err != nil {
		return RelayObservationCursor{}, err
	}
	if servers == 0 || missing != 0 || boots != 1 {
		return RelayObservationCursor{}, nil
	}
	return RelayObservationCursor{BootID: bootID, Sequence: sequence}, nil
}

func (s *Store) IngestRelayObservations(ctx context.Context, response bridgeprotocol.ObservationDrainResponse) (TelemetryIngestResult, error) {
	if err := validateObservationDrain(response); err != nil {
		return TelemetryIngestResult{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TelemetryIngestResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	serverIDs, err := appliedRelayServerIDsTx(ctx, tx)
	if err != nil {
		return TelemetryIngestResult{}, err
	}
	cursors := make(map[string]RelayObservationCursor, len(serverIDs))
	for serverID := range serverIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO relay_observation_cursors(server_id) VALUES($1) ON CONFLICT(server_id) DO NOTHING`, serverID); err != nil {
			return TelemetryIngestResult{}, err
		}
		var cursor RelayObservationCursor
		if err := tx.QueryRow(ctx, `SELECT boot_id,cursor FROM relay_observation_cursors WHERE server_id=$1 FOR UPDATE`, serverID).Scan(&cursor.BootID, &cursor.Sequence); err != nil {
			return TelemetryIngestResult{}, err
		}
		cursors[serverID] = cursor
	}

	result := TelemetryIngestResult{Cursor: RelayObservationCursor{BootID: response.BootID, Sequence: response.NextSequence}}
	for _, observation := range response.Items {
		if _, ok := serverIDs[observation.ServerID]; !ok {
			return TelemetryIngestResult{}, ErrConflict
		}
		cursor := cursors[observation.ServerID]
		if cursor.BootID == observation.BootID && observation.Sequence <= cursor.Sequence {
			result.Duplicates++
			continue
		}
		clientKind, err := telemetryClientKindTx(ctx, tx, observation)
		if err != nil {
			return TelemetryIngestResult{}, err
		}
		day, err := observationDay(observation)
		if err != nil {
			return TelemetryIngestResult{}, err
		}
		errorClass := observation.ErrorClass
		if errorClass == "none" {
			errorClass = ""
		}
		errorCount := 0
		if errorClass != "" {
			errorCount = 1
		}
		command, err := tx.Exec(ctx, `
			UPDATE mcp_daily_aggregates
			SET call_count=call_count+1,error_count=error_count+$10
			WHERE day=$1 AND profile_id IS NOT DISTINCT FROM $2::uuid
			  AND profile_revision_id IS NOT DISTINCT FROM $3::uuid
			  AND server_id IS NOT DISTINCT FROM $4::uuid
			  AND tool_id IS NOT DISTINCT FROM $5::uuid
			  AND client_kind=$6 AND decision=$7 AND outcome=$8
			  AND error_class=$9 AND duration_bucket=$11`,
			day, observation.ProfileID, observation.ProfileRevisionID, observation.ServerID, observation.ToolID,
			clientKind, observation.Decision, observation.Outcome, errorClass, errorCount, observation.DurationBucket)
		if err != nil {
			return TelemetryIngestResult{}, err
		}
		if command.RowsAffected() == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO mcp_daily_aggregates(day,profile_id,profile_revision_id,server_id,tool_id,client_kind,decision,outcome,error_class,call_count,error_count,duration_bucket) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,1,$10,$11)`, day, observation.ProfileID, observation.ProfileRevisionID, observation.ServerID, observation.ToolID, clientKind, observation.Decision, observation.Outcome, errorClass, errorCount, observation.DurationBucket); err != nil {
				return TelemetryIngestResult{}, err
			}
		}
		cursor.BootID, cursor.Sequence = observation.BootID, observation.Sequence
		cursors[observation.ServerID] = cursor
		result.Accepted++
	}

	for serverID := range serverIDs {
		if _, err := tx.Exec(ctx, `UPDATE relay_observation_cursors SET boot_id=$2,cursor=$3,updated_at=now() WHERE server_id=$1`, serverID, response.BootID, response.NextSequence); err != nil {
			return TelemetryIngestResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return TelemetryIngestResult{}, err
	}
	return result, nil
}

func (s *Store) DeleteExpiredTelemetry(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.UTC().AddDate(0, 0, -30).Format("2006-01-02")
	command, err := s.pool.Exec(ctx, `DELETE FROM mcp_daily_aggregates WHERE day<$1::date`, cutoff)
	if err != nil {
		return 0, err
	}
	return command.RowsAffected(), nil
}

func (s *Store) RelayGovernanceHealthy(ctx context.Context) (bool, error) {
	var healthy bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM targets target
		JOIN target_desired_snapshots desired ON desired.target_id=target.id
		CROSS JOIN settings
		WHERE target.target_key='local/shared-relay'
		  AND desired.health='healthy'
		  AND NOT settings.relay_intentional_paused
	)`).Scan(&healthy)
	return healthy, err
}

func appliedRelayServerIDsTx(ctx context.Context, tx pgx.Tx) (map[string]struct{}, error) {
	rows, err := tx.Query(ctx, `SELECT member.server_id::text FROM relay_configuration_state state JOIN relay_configuration_revision_mcp_servers member ON member.relay_configuration_revision_id=state.applied_revision_id WHERE state.singleton ORDER BY member.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]struct{}{}
	for rows.Next() {
		var serverID string
		if err := rows.Scan(&serverID); err != nil {
			return nil, err
		}
		result[serverID] = struct{}{}
	}
	return result, rows.Err()
}

func telemetryClientKindTx(ctx context.Context, tx pgx.Tx, observation bridgeprotocol.Observation) (string, error) {
	var clientKind string
	err := tx.QueryRow(ctx, `
		SELECT revision.client_kind
		FROM profile_revisions revision
		JOIN mcp_tools tool ON tool.id=$4 AND tool.server_id=$3
		WHERE revision.id=$2 AND revision.profile_id=$1`,
		observation.ProfileID, observation.ProfileRevisionID, observation.ServerID, observation.ToolID).Scan(&clientKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrConflict
	}
	if err != nil {
		return "", err
	}
	if clientKind != "claude" && clientKind != "codex" {
		return "", ErrConflict
	}
	return clientKind, nil
}

func validateObservationDrain(response bridgeprotocol.ObservationDrainResponse) error {
	if uuid.Validate(response.BootID) != nil || response.NextSequence < 0 || len(response.Items) > maxRelayObservationBatch {
		return ErrConflict
	}
	previous := int64(0)
	for _, item := range response.Items {
		if item.BootID != response.BootID || item.Sequence <= previous || item.Sequence > response.NextSequence || uuid.Validate(item.ProfileID) != nil || uuid.Validate(item.ProfileRevisionID) != nil || uuid.Validate(item.ServerID) != nil || uuid.Validate(item.ToolID) != nil {
			return ErrConflict
		}
		if !telemetryDecisions[item.Decision] || !telemetryOutcomes[item.Outcome] || !telemetryErrorClasses[item.ErrorClass] || !telemetryDurationBuckets[item.DurationBucket] || len(item.ReasonCodes) > 32 {
			return ErrConflict
		}
		for _, reason := range item.ReasonCodes {
			if strings.TrimSpace(reason) == "" || len(reason) > 64 {
				return ErrConflict
			}
		}
		if _, err := observationDay(item); err != nil {
			return err
		}
		previous = item.Sequence
	}
	if len(response.Items) > 0 && previous != response.NextSequence {
		return ErrConflict
	}
	return nil
}

func observationDay(observation bridgeprotocol.Observation) (string, error) {
	if math.IsNaN(observation.ObservedAt) || math.IsInf(observation.ObservedAt, 0) || observation.ObservedAt < 0 {
		return "", ErrConflict
	}
	bucket, err := time.Parse("2006-01-02T15:04:00Z", observation.MinuteBucket)
	if err != nil || !time.Unix(int64(observation.ObservedAt), 0).UTC().Truncate(time.Minute).Equal(bucket) {
		return "", ErrConflict
	}
	return bucket.Format("2006-01-02"), nil
}
