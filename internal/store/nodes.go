package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
)

func (s *Store) BootstrapEnvironment(ctx context.Context, localName, managedUsername, timezone string, relayPort int) error {
	managedUsername = strings.TrimSpace(managedUsername)
	if err := bridgeprotocol.ValidateManagedUsername(managedUsername); err != nil {
		return err
	}
	if relayPort < 1 || relayPort > 65535 {
		return errors.New("relay port is invalid")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO settings(singleton,managed_username,timezone,relay_port) VALUES(true,$1,$2,$3) ON CONFLICT(singleton) DO NOTHING`, managedUsername, timezone, relayPort); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT managed_username FROM settings WHERE singleton`).Scan(&managedUsername); err != nil {
		return err
	}
	var nodeID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE kind='local'`).Scan(&nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		nodeID = uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO nodes(id,name,kind,status,last_seen_at) VALUES($1,$2,'local','online',now())`, nodeID, strings.TrimSpace(localName)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for _, runtime := range []string{domain.RuntimeClaude, domain.RuntimeCodex, domain.RuntimeHermes, domain.RuntimeSharedRelay} {
		targetKey := "local/" + runtime
		if _, err := tx.Exec(ctx, `INSERT INTO targets(id,target_key,node_id,runtime,managed_username,writable) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(node_id,runtime) DO UPDATE SET managed_username=EXCLUDED.managed_username,updated_at=now()`, uuid.NewString(), targetKey, nodeID, runtime, managedUsername, domain.IsWritableRuntime(runtime)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) UpsertDiscoveredNodes(ctx context.Context, nodes []bridgeprotocol.NodeInfo) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var globalUsername string
	if err := tx.QueryRow(ctx, `SELECT managed_username FROM settings WHERE singleton`).Scan(&globalUsername); err != nil {
		return err
	}
	seen := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.Kind != bridgeprotocol.NodeKindSalt || strings.TrimSpace(node.SaltMinionID) == "" {
			continue
		}
		id := node.NodeID
		if uuid.Validate(id) != nil {
			id = uuid.NewString()
		}
		status := "unavailable"
		if node.Status == "online" {
			status = "online"
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(id,name,kind,salt_minion_id,status,salt_version,last_seen_at) VALUES($1,$2,'salt',$3,$4,$5,CASE WHEN $4='online' THEN now() ELSE NULL END) ON CONFLICT(salt_minion_id) WHERE salt_minion_id IS NOT NULL DO UPDATE SET name=EXCLUDED.name,status=EXCLUDED.status,salt_version=EXCLUDED.salt_version,last_seen_at=CASE WHEN EXCLUDED.status='online' THEN now() ELSE nodes.last_seen_at END,updated_at=now() RETURNING id::text`, id, node.Name, node.SaltMinionID, status, node.Version).Scan(&id); err != nil {
			return err
		}
		seen = append(seen, id)
		for _, runtime := range []string{domain.RuntimeClaude, domain.RuntimeCodex, domain.RuntimeHermes} {
			targetKey := fmt.Sprintf("salt:%s/%s", node.SaltMinionID, runtime)
			if _, err := tx.Exec(ctx, `INSERT INTO targets(id,target_key,node_id,runtime,managed_username,writable) VALUES($1,$2,$3,$4,coalesce((SELECT managed_username_override FROM nodes WHERE id=$3),$5),$6) ON CONFLICT(node_id,runtime) DO UPDATE SET managed_username=coalesce((SELECT managed_username_override FROM nodes WHERE id=$3),$5),updated_at=now()`, uuid.NewString(), targetKey, id, runtime, globalUsername, domain.IsWritableRuntime(runtime)); err != nil {
				return err
			}
		}
	}
	if len(seen) == 0 {
		if _, err := tx.Exec(ctx, `UPDATE nodes SET status='unavailable',updated_at=now() WHERE kind='salt' AND archived_at IS NULL`); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE nodes SET status='unavailable',updated_at=now() WHERE kind='salt' AND archived_at IS NULL AND NOT (id=ANY($1::uuid[]))`, seen); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListNodes(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT id::text,name,kind,salt_minion_id AS "saltMinionId",managed_username_override AS "managedUsernameOverride",status,salt_version AS "saltVersion",last_seen_at AS "lastSeenAt",archived_at AS "archivedAt",created_at AS "createdAt",updated_at AS "updatedAt" FROM nodes WHERE archived_at IS NULL ORDER BY kind,name,id`)
}

func (s *Store) ListTargets(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, targetSelect+` WHERE n.archived_at IS NULL ORDER BY n.kind,n.name,t.runtime`)
}

const targetSelect = `SELECT t.id::text,t.target_key AS "targetKey",t.node_id::text AS "nodeId",n.name AS "nodeName",n.kind AS "nodeKind",coalesce(n.salt_minion_id,'') AS "saltMinionId",t.runtime,t.managed_username AS "managedUsername",t.writable,CASE WHEN EXISTS(SELECT 1 FROM operation_targets active_ot JOIN operations active_o ON active_o.id=active_ot.operation_id WHERE active_ot.target_id=t.id AND active_ot.status='running' AND active_o.kind='reconcile') THEN 'repairing' ELSE coalesce(ds.health,'drifted') END AS health,coalesce(ds.desired_revision,0) AS "desiredRevision",coalesce(rs.revision,'') AS "targetRevision",coalesce(ds.drift_summary,'{}'::jsonb) AS "driftSummary",rs.scanned_at AS "lastScannedAt",ds.last_reconciled_at AS "lastReconciledAt",coalesce(ds.error_code,'') AS "errorCode",coalesce(ds.error_reason,'') AS "errorReason" FROM targets t JOIN nodes n ON n.id=t.node_id LEFT JOIN runtime_snapshots rs ON rs.target_id=t.id LEFT JOIN target_desired_snapshots ds ON ds.target_id=t.id`

func (s *Store) Target(ctx context.Context, id string) (domain.Target, error) {
	var target domain.Target
	err := s.pool.QueryRow(ctx, targetSelect+` WHERE t.id=$1`, id).Scan(&target.ID, &target.TargetKey, &target.NodeID, &target.NodeName, &target.NodeKind, &target.SaltMinionID, &target.Runtime, &target.ManagedUsername, &target.Writable, &target.Health, &target.DesiredRevision, &target.TargetRevision, &target.DriftSummary, &target.LastScannedAt, &target.LastReconciledAt, &target.ErrorCode, &target.ErrorReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Target{}, ErrNotFound
	}
	return target, err
}

func (s *Store) ReplaceRuntimeSnapshot(ctx context.Context, targetID, revision string, inventory any) error {
	body, err := json.Marshal(inventory)
	if err != nil {
		return err
	}
	if !bridgeprotocol.IsSHA256(revision) {
		return errors.New("target revision must be a SHA-256 hash")
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO runtime_snapshots(target_id,revision,inventory) VALUES($1,$2,$3) ON CONFLICT(target_id) DO UPDATE SET revision=EXCLUDED.revision,inventory=EXCLUDED.inventory,scanned_at=now()`, targetID, revision, jsonText(body))
	return err
}

func (s *Store) RuntimeSnapshot(ctx context.Context, targetID string) (json.RawMessage, string, error) {
	var body []byte
	var revision string
	err := s.pool.QueryRow(ctx, `SELECT inventory,revision FROM runtime_snapshots WHERE target_id=$1`, targetID).Scan(&body, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	return json.RawMessage(body), revision, err
}

func (s *Store) UpdateNodeManagedUsername(ctx context.Context, nodeID, username string) error {
	username = strings.TrimSpace(username)
	if uuid.Validate(nodeID) != nil {
		return ErrNotFound
	}
	if username != "" {
		if err := bridgeprotocol.ValidateManagedUsername(username); err != nil {
			return err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var global string
	if err := tx.QueryRow(ctx, `SELECT managed_username FROM settings WHERE singleton FOR UPDATE`).Scan(&global); err != nil {
		return err
	}
	var value any
	if username != "" {
		value = username
	}
	command, err := tx.Exec(ctx, `UPDATE nodes SET managed_username_override=$2,updated_at=now() WHERE id=$1 AND kind='salt'`, nodeID, value)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	managed := username
	if managed == "" {
		managed = global
	}
	if _, err := tx.Exec(ctx, `UPDATE targets SET managed_username=$2,updated_at=now() WHERE node_id=$1`, nodeID, managed); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func bridgeTarget(target domain.Target) bridgeprotocol.Target {
	return bridgeprotocol.Target{ID: target.ID, NodeID: target.NodeID, NodeKind: target.NodeKind, SaltMinionID: target.SaltMinionID, Runtime: target.Runtime, ManagedUsername: target.ManagedUsername}
}
