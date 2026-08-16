# Generation-2 Rollout And Rollback

ToolHub generation 2 replaces the Agent/RBAC/deployment architecture with a
single-user control plane, a host Bridge, Salt remote delivery, immutable
desired snapshots, and five-minute reconcile. Database compatibility is a hard
boundary: there is no in-place generation-1 migration.

## Release Invariants

- ToolHub HTTP remains published on host loopback only.
- The container mounts only the Bridge socket directory, never managed homes.
- The Bridge has no TCP listener and exposes no arbitrary execution surface.
- `/etc/salt/master` and Hermes-managed content are never modified.
- Apply always follows revision-bound preflight and is destructive only inside
  manageable scope.
- Reconcile repairs pinned members and preserves later unmanaged additions.
- Generation-1 and generation-2 binaries/databases are never mixed.

## Pre-Rollout Backup

Record image digests, binary versions, unit contents, volume names, and the
current Salt state root. Take restorable backups of:

1. the PostgreSQL volume;
2. local Claude/Codex/Hermes managed homes;
3. mcpm registry/profile and native anchors;
4. `/srv/salt/states`;
5. generation-1 ToolHub/Agent packages and service configuration.

These are whole-system rollback inputs. Do not import old tables, users, node
state, jobs, deployments, or audit rows into generation 2.

## Deployment Order

1. Build both `toolhub` and `toolhub-bridge` from the same revision.
2. Install the Bridge and rendered systemd units.
3. Verify the root-only HMAC key, socket group, and managed OS user.
4. Start the Bridge and confirm its Unix socket mode/group.
5. Start ToolHub against a new empty PostgreSQL volume.
6. Verify `/healthz`, username login, CSRF, and the generation-2 navigation.
7. Refresh nodes; do not Apply remotely until Salt `test.version` returns
   `3008.x`.

Use [`DEPLOYMENT.md`](DEPLOYMENT.md) for exact commands.

## Canary Sequence

### Local Skills

1. Scan `local/claude` and `local/codex`.
2. Inspect protected/excluded entries.
3. Apply a small Profile to one runtime.
4. Verify backup creation, pinned desired snapshot, and inventory hash.
5. Add an unmanaged Skill manually and allow one reconcile tick; confirm it is
   preserved while pinned drift is repaired.

### Local Shared Relay

1. Verify `/usr/libexec/toolhub-mcpm toolhub contract --json` and that port `6276` (or the configured
   fixed port) is free.
2. Apply a Profile to `local/shared-relay`.
3. Connect at least one Claude, Codex, and local Hermes client to the same endpoint.
4. Verify exactly one `toolhub-relay` user-scope anchor in each native config;
   confirm the previous Hermes `mcp_servers` entries were removed by Apply.
5. Stop the relay through ToolHub, wait through reconcile, and verify
   `intentional_paused` prevents automatic start while config drift is still
   detected/repaired.
6. Restart and run the explicit health action.
7. Require every desired MCP member to be `ready`; for the current canary this
   means eight namespaces and 75 tools. Call the safe read-only ACEMCP indexing
   status, Grok config info, and Trellis UI info tools.
8. Wait through one 30-minute full member-health cadence and repeat the check.

### Relay Governance Transition

1. Start in `compatibility`; startup may identify the exact legacy
   `shared-mcp` Profile but must not observe, accept, publish, Apply, or create an
   operation.
2. Observe and accept every current Contract, then resolve every Profile whose
   client/category metadata is ambiguous. First Contract acceptance may create
   candidate Profile revisions but must not publish them.
3. Preflight `enforced` and verify the fixed runtime order: mcpm capability,
   Claude native-client inspection, Codex native-client inspection, then the
   five-part session canary. Missing features, unsupported clients, an
   unhealthy/non-v2 active Relay snapshot, missing backup, or unreviewed state
   must fail closed before delivery.
4. Verify that final canary against the candidate routing bundle: Claude
   explicit Profile catalog, Codex explicit Profile catalog, missing Profile
   default all-tools behavior, unknown Profile `profile_unknown` failure, and two
   concurrent sessions with every upstream process count still exactly one.
5. Apply `enforced`. Only after a healthy Applied snapshot has a Restore backup
   may the exact legacy Profile be marked `migrated_relay` and hidden from the
   ordinary Profile list. Keep its history, Bundle references, and Secret
   bindings.
6. Use each Profile's **Launch session** action and copy the server-generated
   strict `claude --strict-mcp-config --mcp-config ...` or `codex
   --strict-config -c ...` command. Do not reuse a command for another Profile;
   its Relay URL binds the client kind and exact Profile UUID.
7. Roll back through normal Preflight/Apply to the previous `compatibility`
   routing bundle. Confirm upstreams are not duplicated, expired grants do not
   revive, Contract history remains, and old native clients regain the complete
   catalog.

### Salt Minion

1. Verify the selected accepted non-critical minion is online and reports
   `3008.x`.
2. Set/verify its managed user override.
3. Scan Claude/Codex/Hermes; confirm Hermes is read-only.
4. Preflight and Apply a small Profile to one writable runtime.
5. Verify asset publication, sync functions, staged-bundle cleanup, JID
   projection, target backup, snapshot pinning, and next-tick no-op reconcile.
6. Induce one pinned-member drift and verify repair creates a backup first.

Only expand to the fleet after all three canaries pass.

## Monitoring During Expansion

Use Overview/Targets for aggregated `drifted`, `blocked`, and `unavailable`
health. Use Operations for per-target terminal results and failed-only retry;
use Audit for state-changing history. ToolHub intentionally sends no email,
Webhook, or IM notification.

Expected state transitions:

- operation: `queued -> running -> succeeded|partial|failed|cancelled`;
- target health: `healthy|drifted|repairing|blocked|unavailable`;
- overlapping reconcile: one active target plus at most one coalesced rerun.

Do not treat operation dispatch alone as proof of desired-state convergence.
Verify target health and the active snapshot revision.

Confirmation grants are exact, 60-second, and one-shot. They are consumed
before dispatch and never restored. Treat `not_executed` as safe to retry only
when the adapter proved dispatch did not begin; for `execution_unknown`, inspect
the real target state before manually deciding whether to issue a new call and
confirmation. Live Relay observations expire after 24 hours, while ToolHub's
payload-free daily aggregates retain 30 days.

## Stop Conditions

Pause expansion when any of the following occurs:

- schema-generation mismatch or unexpected non-empty database;
- Bridge authentication, replay, journal, or socket permission error;
- blocked/suspended relay, incomplete desired namespace projection, or fixed
  port fallback;
- Salt version mismatch, missing managed home, staging leak, or unprovable JID;
- protected-scope deletion, unmanaged reconcile deletion, or missing backup;
- plaintext secret in a browser response, operation, audit entry, journal, or
  log.

Queued targets can be cancelled. Do not kill a running destructive target step;
allow it to reach its atomic terminal state, then inspect/restore/retry.

## Whole-System Rollback

Rollback is a generation switch, not a schema migration:

1. Stop the generation-2 ToolHub container.
2. Stop generation-2 Bridge/relay services.
3. Restore the old ToolHub image/binary and old PostgreSQL volume together.
4. Restore generation-1 Agent services/packages where required.
5. Restore the previous Salt ToolHub namespace and local runtime/mcpm backups
   when generation-2 writes must be undone.
6. Start the old stack and verify its own health and delivery paths.

Never point the old application at the generation-2 volume or the new
application at the old volume. Keep the generation-2 backups intact until the
rollback decision window closes.

For a routing-governance rollback, prefer normal Preflight/Apply of the retained
`compatibility` Relay Configuration. This does not delete Contract history or
candidate revisions, restart unchanged upstreams, or revive consumed/expired
confirmation grants. A binary rollback across migration `004` must restore the
pre-`004` PostgreSQL volume at the same time; an older binary must never connect
to a database whose governance migration has been applied. Relay runtime
failures do not require deleting the pinned mcpm registry, Secrets, or active
membership.

Relay governance rollout is local to `local/shared-relay`; it does not authorize
remote Salt MCP rollout. Keep unavailable minions excluded, preserve accepted-key
targeting and the Salt `3008.x` gate, and handle remote recovery as a separate
operator-controlled canary.

## Current External Status

As of 2026-07-30, four of five accepted minions report `3008.x`; only
`racknerd-73661c5` remains unavailable. A read-only canary against
`salt:racknerd/claude` passed namespaced asset publication, extension sync,
managed-user lookup, chunked staging, fixed read execution, revision capture,
and staging cleanup. Destructive Apply/reconcile validation remains part of the
operator-controlled canary sequence above, and fleet expansion must exclude or
recover the unavailable minion.
