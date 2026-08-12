# Salt Target Refresh Archive Design

## Goal

Make the existing manual **Refresh nodes** workflow synchronize active Salt
Targets with the Salt master's accepted keys:

- newly accepted minions appear automatically with Claude, Codex, and Hermes
  Targets;
- minions no longer present in the accepted-key result disappear from the
  active Nodes and Targets views;
- a previously removed minion restores its original node and Target records if
  the same Salt minion ID is accepted again.

This change does not add periodic node discovery. Refresh remains an explicit,
durable control operation started by the browser button or Browser API.

## Terminology

- **Discovered**: the minion ID is present in the successful
  `salt-key --out=json --list=acc` result.
- **Unavailable**: the key is still accepted, but the minion did not return a
  supported Salt version or is offline.
- **Archived**: the minion ID is absent from a successful accepted-key result
  and is therefore outside ToolHub's active management inventory.
- **Restored**: an archived minion ID is discovered again and its existing
  records are made active.

An unavailable minion is not archived. This prevents transient network or Salt
execution failures from removing a still-accepted node.

## Chosen Design

Use the existing `nodes.archived_at` and `status='archived'` fields as a soft
archive boundary. Targets remain linked to the node and do not need a separate
archive column.

On every successful node refresh, `UpsertDiscoveredNodes` performs one database
transaction:

1. Upsert every discovered Salt node by `salt_minion_id`.
2. Clear `archived_at` for discovered nodes, update their name, version and
   online/unavailable status, and retain their existing node ID.
3. Upsert the fixed Claude, Codex, and Hermes Target set for each node by
   `(node_id, runtime)`. Existing Target IDs and desired state are retained.
4. Set `status='archived'` and `archived_at=now()` for active Salt nodes not in
   the discovery result.
5. Lock and cancel queued, not-yet-started target work belonging to newly
   archived nodes and recalculate the affected parent operation statuses.

Running target work is not interrupted. It already crossed the dispatch
boundary and must finish atomically. Historical operations continue to resolve
their Target keys because neither nodes nor Targets are physically deleted.

## Active Management Boundary

Archived nodes and their Targets must be excluded from all active paths:

- `GET /nodes` and `GET /targets` omit them;
- direct active Target lookup treats them as not found, preventing new scans,
  Apply, Restore, import, or relay actions against archived Targets;
- the reconcile scheduler does not enqueue their desired snapshots;
- the worker does not claim queued work for an archived Target;
- operation history, desired snapshots, runtime snapshots, backups, and audit
  history remain readable through their historical projections.

The archive transaction cancels queued work rather than leaving it permanently
unclaimable. The worker claim query joins through active nodes and locks the
selected operation-target row. Refresh locks queued operation-target rows before
cancelling them. A claim racing with refresh therefore has two valid outcomes:
work claimed first becomes running and completes; work archived first becomes
cancelled and cannot be claimed.

Coalesced reconcile reruns also recheck that the node is active before creating
new work. Archiving clears `pending_rerun` on affected terminal or running rows
so a running operation cannot schedule a new reconcile after it finishes.

## Browser Completion Semantics

`POST /nodes/refresh` remains asynchronous and returns the durable refresh
operation. The Targets page must not treat the initial `202 Accepted` response
as completed discovery.

After queueing refresh, the page:

1. disables the Refresh nodes button and shows the queued/running notice;
2. polls `GET /operations/{id}` until the operation is terminal;
3. reloads `/targets`, `/skills`, and `/mcp/servers` after success;
4. shows the operation's public error if refresh fails or is cancelled;
5. stops polling if the page unmounts.

Only one refresh request can be initiated from the button while this sequence
is active. This makes one click reflect the authoritative discovery result
without requiring a browser reload or a second click.

## Restore Behavior

Rediscovering the same `salt_minion_id` clears the node archive fields and
reuses the existing node and Target rows. Therefore it preserves:

- Target UUIDs and `target_key` values;
- managed-username overrides;
- active immutable desired snapshots;
- runtime inventory history, operation history, and backups.

The normal five-minute reconcile scheduler resumes management after restore.
Refresh itself does not Apply a Profile, mutate an immutable snapshot, or force
an immediate destructive operation.

## Failure Handling

Archiving occurs only after Bridge discovery succeeds and the store receives a
complete `RefreshNodesResponse`. If listing accepted Salt keys fails, the
refresh operation fails and the database is unchanged.

An empty but successful accepted-key list is authoritative and archives every
active Salt node. The local node and local Targets are never archived by this
workflow.

The discovery upsert and archive/cancellation changes commit atomically. Any
database error rolls back the entire refresh projection.

## Alternatives Considered

### Keep absent nodes unavailable

This is the current behavior. It preserves state but leaves stale Targets in
the active UI and does not meet the requested synchronization behavior.

### Physically delete nodes and Targets

Rejected because Targets are referenced by operations, immutable desired
snapshots, confirmations, and backups. Cascading or rewriting those records
would destroy history and substantially increase the blast radius.

### Soft archive and create fresh Targets on rediscovery

Rejected because it loses continuity of desired state and Target identity. It
would also leave duplicate historical Target keys or require renaming them.

## Files And Contracts

Expected implementation scope:

- `internal/store/nodes.go`: transactional archive/restore behavior and active
  Target lookup boundary;
- `internal/store/operations.go`: active-node claim/reconcile filtering and
  queued-operation cancellation support;
- `internal/store/integration_test.go`: PostgreSQL-backed discovery lifecycle
  and scheduler/worker regression coverage;
- `web/src/pages/Targets.tsx`: wait for the durable refresh operation and reload
  data after terminal success;
- `web/e2e/smoke.spec.ts`: verify the refresh button waits for terminal state
  and reflects the resulting Target list;
- `docs/API.md` and/or `docs/SALT.md`: document refresh archive/restore
  semantics.

The Browser and Bridge HTTP payloads do not change, so neither OpenAPI contract
requires a schema update. No new UI control is required.

## Validation

Focused PostgreSQL integration tests must prove:

1. a new accepted minion creates exactly three Targets;
2. an offline but accepted minion remains active and unavailable;
3. a missing minion becomes archived and disappears from active node/Target
   lists and lookups;
4. queued work is cancelled while running work is preserved;
5. archived desired snapshots are not scheduled for reconcile;
6. rediscovery restores the original node and Target UUIDs, username override,
   and desired snapshot;
7. an empty successful discovery archives all Salt nodes but not the local
   node;
8. a failed refresh does not invoke the store update and therefore archives
   nothing;
9. the browser waits for refresh completion, reloads active Targets on success,
   and surfaces terminal failure without issuing duplicate refresh requests.

After focused tests, run the full relevant gates:

```bash
GOCACHE=/tmp/toolhub-gocache go test ./internal/store ./internal/worker
GOCACHE=/tmp/toolhub-gocache go test ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
cd web && npm run typecheck && npm run build
cd .. && make docker-config
```

## Rollback

Reverting the application change restores the prior refresh behavior. Existing
archived rows can be recovered without data loss by rediscovering their accepted
Salt keys under the new behavior, or by a controlled database update that
clears `archived_at` and restores the observed status. No schema migration or
irreversible data deletion is introduced.
