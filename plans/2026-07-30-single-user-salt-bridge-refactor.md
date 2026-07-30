# ToolHub Single-User Salt Bridge And Continuous Reconcile Refactor

## Status

- Date: 2026-07-30
- Type: high-risk, cross-layer rewrite
- Compatibility: fresh PostgreSQL database and fresh ToolHub deployment only
- Schema generation: 2
- Implementation: complete; static/local acceptance and a read-only Salt 3008.x staging canary passed; one accepted minion remains unavailable

## Goal And Scope

Refactor ToolHub into a single-user control plane. Keep username/password authentication, Argon2id, server-side sessions, CSRF, secure cookies, encrypted secrets, immutable Skill artifacts, marketplace intake, and actorless audit. Remove RBAC, multi-user administration, Agent/WSS/SSH, review and approval flows, old jobs/node tasks/deployments, and the dual Profile model.

The replacement uses a root-owned Linux `toolhub-bridge` over an HMAC-authenticated Unix socket. The Bridge manages the local host through a guarded local adapter and remote nodes through fixed Salt 3008.x CLI calls. Unified Profiles contain both Skills and MCP servers. Explicit Apply operations use revision-bound preflight and destructive mirror semantics, while a five-minute reconcile loop repairs only pinned managed members and preserves content added outside ToolHub after Apply.

## Invariants

### Identity And Library

- PostgreSQL enforces exactly one `account` row. It contains username, password hash, password-change metadata, and timestamps only.
- First start requires `TOOLHUB_BOOTSTRAP_USERNAME` and `TOOLHUB_BOOTSTRAP_PASSWORD`; later starts ignore both values.
- Username or password changes revoke every session.
- Audit records have no actor identifier. They retain action, resource, outcome, request IP, redacted metadata, and timestamp.
- Library intake remains available from ZIP, Git, SkillsMP, Xiaping, explicit local Skill scan/import, and manual MCP entry.
- Salt nodes and Hermes are never Library import sources. Hermes is inventory-only.
- Skill artifacts are immutable and content-addressed, with provenance, canonical SHA-256, manifest, and scan report. There is no review/approval state.
- Update discovery imports a new fixed source revision and advances the Library current version, but never applies it to a target.
- Settings contain one global update schedule and timezone, plus `Check now`.

### Profiles, Apply, Edit, Restore, And Snapshots

- One Profile type references Skill IDs and MCP server IDs; membership never pins versions.
- Preflight resolves current Skill versions, MCP revisions, and secret references, then returns a short-lived, one-use confirmation token bound to the Profile revision, target revision, and canonical desired manifest hash.
- Explicit Apply is a destructive mirror only inside the manageable scope. Preflight lists additions, replacements, deletions, and protected exclusions.
- Runtime built-ins, `.system`, hidden/protected Skill entries, and Claude project/local/managed/plugin MCP scopes are never in destructive scope.
- Every target/runtime independently performs backup, stage, validation, and atomic replace. Fleet operations may finish `partial`.
- Every successful Apply, target edit, or Restore creates an immutable pinned desired snapshot containing exact Skill artifact versions, MCP revisions, secret IDs, content hashes, managed member IDs, and Profile revision.
- `target_desired_snapshots` stores only the active pointer and projected reconcile state. Target edits derive a new snapshot; the next explicit Profile Apply replaces it wholesale.
- Restore first backs up current state, restores the selected backup, scans the restored managed content, and pins that content in a new snapshot.
- Every Apply, edit, restore, and actual repair creates a backup before writing. Retention is 30 days and at most 10 backups per target/runtime.

### Continuous Reconcile

- Every five minutes the scheduler queues reconcile for targets with active desired snapshots.
- A target has at most one active operation target. Overlapping ticks set one `pending_rerun` bit rather than dispatching another Salt JID.
- Reconcile checks and repairs only members pinned in the active snapshot. It never deletes later unmanaged additions.
- Health is one of `healthy`, `drifted`, `repairing`, `blocked`, or `unavailable`; every round remains in operation history.
- A no-op reconcile creates no backup. A repair always creates a backup before atomic write.
- Offline, blocked, and failed targets are checked again every five minutes. Alert and audit records are created only when health or the public error reason changes.
- There is no email, webhook, or IM notification surface.
- Cancel prevents dispatch only for queued targets. A destructive target step that started must finish in an atomic terminal state.
- A missing/expired Salt JID is never guessed successful. The Bridge rescans; exact snapshot match means success, otherwise the target becomes blocked and remains safely retryable.

## Bridge, Salt, And MCP

### Bridge Trust Boundary

- `cmd/toolhub-bridge` is a Linux Go binary installed as a root-owned systemd service.
- It serves HTTP over `/run/toolhub-bridge/bridge.sock`; the socket is mode `0660` and uses a fixed shared group. The ToolHub container never mounts user homes.
- Each request is HMAC-SHA256 signed with a separate 32-byte key over method, path/query, Unix timestamp, nonce, and body SHA-256. The Bridge allows 30 seconds of clock skew and durably rejects nonce replay.
- The Bridge exposes typed operations only. It accepts no shell, arbitrary executable, Salt function, systemd unit, or filesystem path.
- A mode `0600` BoltDB journal stores request idempotency hashes, safe responses, operations, target steps, Salt JIDs, nonce replay data, and the backup catalog. It never stores plaintext secrets, editable file contents, archives, or raw Salt output.
- Operation state is `queued`, `running`, `succeeded`, `partial`, `failed`, or `cancelled`.
- Browser endpoints map fixed request shapes to Bridge methods; browser input can never select an arbitrary Bridge path or body.

### Salt Remote Driver

- Only accepted Salt keys are discovered. Remote capability requires Salt Master/minion `3008.x`.
- ToolHub reuses `/srv/salt/states` and never edits `/etc/salt/master`.
- Repository assets live under namespaced `_modules/toolhub.py`, `_states/toolhub.py`, and `toolhub/` SLS/assets. Bridge publishes them atomically by content hash and invokes only fixed `saltutil.sync_modules` and `saltutil.sync_states` functions when required.
- Dynamic manifests, archives, edit bundles, and secret payloads use root-only staging plus fixed-argv `salt-cp --chunked`, and are removed immediately after a target terminal state.
- All Salt execution uses `exec.CommandContext` with fixed argv. JSON parsing accepts per-minion streaming objects as well as aggregate JSON.
- Each remote destructive mutation dispatches with `--async`, persists the JID, and polls fixed `salt-run jobs.lookup_jid`/`jobs.list_job` calls. Discovery and read-only scans use fixed synchronous calls.
- Cancellation does not rely on `term_job` or `kill_job`.
- The managed user is a global username with optional per-node override. Local uses OS lookup; remote uses `user.info` and blocks when a canonical home cannot be resolved.
- Local Go and remote Salt adapters share DTOs, path/protected-entry rules, revision checks, backup behavior, and atomic replace outcomes.

### Local Shared MCP Relay

- Local MCP is target `local/shared-relay`; local Skills remain `local/claude` and `local/codex`.
- ToolHub owns one mcpm Profile and registry named `toolhub`. Claude and Codex connect to the same HTTP relay.
- `toolhub-mcpm-relay.service` runs as the configured local managed user with `mcpm profile run --http --host 127.0.0.1 --port <port> toolhub`.
- Default endpoint is `http://127.0.0.1:6276/mcp`. The port is configurable; mcpm auto-find is forbidden. A port conflict is `blocked`.
- Bridge may control only the fixed relay unit with start, stop, restart, status, and health actions.
- A user stop records persistent `intentional_paused`. Reconcile still checks registry/config drift but does not start the relay and does not affect local Skill reconcile.
- Registry/profile changes restart and health-check the relay. Failure restores prior files/service configuration, restarts the old relay, and fails/blocks the target operation.
- ToolHub owns exactly one user-scope relay anchor under Codex `mcp_servers` and Claude user MCP. It preserves non-ToolHub and non-user scopes.
- Missing/incompatible mcpm or unhealthy relay blocks local MCP Apply/repair. ToolHub never installs/upgrades mcpm and never falls back to native per-server local configuration.

### Remote MCP

- Remote Claude and Codex use native managed-user MCP configuration.
- Claude mutations are limited to user entries in `~/.claude.json`; Codex mutations are limited to `mcp_servers` in `~/.codex/config.toml`.
- Project/local/managed/plugin scopes and protected entries are always excluded.
- Secret values are write-only in the UI. Explicit local capture reads them once, encrypts them immediately, and never places plaintext in browser responses, audit metadata, Bridge journals, or logs.

## Public Contracts And Storage

### Bridge API

`api/bridge-openapi.yaml` and `internal/bridgeprotocol` define:

- health, node refresh, and target scan;
- Apply/edit/restore preflight and commit;
- reconcile request/status;
- backup list and retention GC;
- relay status/start/stop/restart/health;
- operation get/cancel.

Every mutation requires a caller idempotency key.

### Browser API And UI

- Auth is username-only. Roles, users, and Access endpoints are absent.
- Profiles expose unified Skill/MCP membership, preflight, and confirmed Apply.
- Targets expose inventory, scan, desired revision, health, drift summary, sandboxed editor, backups/restore, and relay controls.
- Operations expose list/get/cancel/retry-failed-targets.
- Agent, enrollment, SSH, deployments, rollback, old reconcile, review/approval, MCP Profile, Profile activation, and RBAC routes are removed.
- Navigation is Overview, Skills, Marketplace, MCP, Profiles, Targets, Operations, Settings, Account.

### Fresh Schema

- Initial schema is rewritten and seeds `app_meta.schema_generation = 2`.
- Before migrations or HTTP listen, any non-empty database without generation 2 fails with an instruction to create a fresh PostgreSQL volume.
- Generation 2 includes account, sessions, encrypted secrets, nodes/runtime snapshots, Library, unified Profiles, operations/targets, immutable desired snapshots, active snapshot pointers, backups, settings/providers, alerts, and actorless audit.
- It excludes roles/users, Agent/enrollment/SSH/node tasks, deployments, old jobs, review/update approvals, MCP delivery Profiles, Profile activations, and shared-source ownership.
- Desired manifests are versioned JSONB with canonical hashes, explicit validation, and secret-reference-only enforcement.
- No legacy users, nodes, jobs, deployments, or audit records are migrated.

## Implementation Checklist

- [x] Freeze Browser/Bridge contracts, snapshot manifest, error catalog, protected-scope rules, and fixtures.
- [x] Replace schema and implement singleton account, sessions, actorless audit, encrypted secrets, operations, snapshots, backups, settings, and generation gate.
- [x] Implement HMAC Unix Bridge, durable journal/idempotency/recovery, local adapter, and backup GC.
- [x] Implement Salt asset publisher, fixed CLI runner, streaming JSON, async JID polling, staging cleanup, and remote adapter.
- [x] Package mcpm relay systemd service, registry/Profile writer, native anchors, relay health, rollback, and intentional pause.
- [x] Implement unified Profile resolution, revision-bound preflight, destructive Apply, edit, restore, and snapshot transitions.
- [x] Implement five-minute coalesced reconcile, health projection, state-change audit, and in-app alerts.
- [x] Replace Browser API/UI and remove all Agent/RBAC/deployment/approval callers and dead code.
- [x] Update OpenAPI, API/security/deployment docs, Compose socket/GID mount, Salt/systemd installation, and fresh deployment runbook.

## Verification And Acceptance

- Auth: singleton enforcement, username login, non-enumerating/timing-safe failures, CSRF, revocation, throttling, cookies, actorless audit.
- Schema: fresh bootstrap, legacy refusal, and plaintext secret/invalid manifest rejection.
- Bridge: tamper, timestamp, replay, idempotency, restart recovery, redaction, and fixed allowlists.
- Apply/snapshot: revision conflict, protected exclusion, exact destructive diff, atomic target outcomes, partial fleet, failed-only retry, immutable pinning, edit/restore derivation, and Profile overwrite.
- Reconcile: no-op without backup, backup before repair, unmanaged additions preserved, target coalescing, offline recovery, and state-change dedupe.
- Backups: simultaneous 30-day and 10-item retention, plus restore stability.
- Salt: 3008.x gate, sync functions, streaming JSON, async JID, cache miss, timeout, partial return, and staging cleanup.
- Filesystem: traversal, symlink, device, binary, oversize, escaped realpath, protected roots, ownership, and modes.
- MCP: secret key-only reads, replace/remove, capture confirmation, redaction, shared relay clients, port conflict, intentional pause, crash recovery, rollback, missing/unhealthy block.
- UI: desktop/mobile navigation, preflight confirmation, partial results, health states, relay controls, and masked secret editing.

Quality gates:

```bash
cd /root/docker/toolhub
go test ./internal/security/... ./internal/store/... ./internal/bridgeprotocol/... ./internal/bridge/... ./internal/runtime/...
go test ./internal/httpapi/... ./internal/worker/... ./cmd/toolhub-bridge/...
python3 -m unittest discover -s packaging/salt/tests -p '*_test.py'
go test ./...
go test -race ./...
go vet ./...
go build -o bin/toolhub ./cmd/toolhub
go build -o bin/toolhub-bridge ./cmd/toolhub-bridge
cd web && npm ci --ignore-scripts && npm audit --audit-level=high && npm run typecheck && npm run build
cd .. && make docker-config
```

Integrated acceptance starts Bridge first and uses a fresh PostgreSQL volume. On 2026-07-30 the isolated API/Playwright smoke passed, as did a read-only `salt:racknerd/claude` canary covering namespaced asset publication, extension sync, `user.info`, chunked staging, fixed read execution, 64-character revision capture, and staging cleanup. The destructive async JID path is statically tested and remains part of the operator-controlled Apply/reconcile canary.

## Rollout, Rollback, And Assumptions

- Before rollout, back up the old PostgreSQL volume, local runtime homes, mcpm registry/config, and Salt state tree. They are whole-system rollback inputs, not schema migration inputs.
- Install/start Bridge before ToolHub so the Unix socket exists. Canary local targets, then one non-critical minion, before selecting the fleet.
- Whole-system rollback restores the old image/binary, old database volume, Agent services, Bridge/systemd package, and ToolHub Salt namespace as a unit. Never cross-connect generation-1 and generation-2 applications/databases.
- ToolHub never edits `/etc/salt/master`, deletes Hermes-owned content, opens a Bridge TCP listener, or mounts user homes into the container.
- Four of the five currently accepted minions report Salt `3008.x`; `racknerd-73661c5` remains unavailable. Destructive Apply/reconcile validation is intentionally left to the rollout canary with an operator-selected Profile; the typed read-only delivery path has passed against `racknerd`.

## External Contract Record From Planning

- Retrieval tools: `mcp__toolhub_codex__grok_search_web_search`, `mcp__toolhub_codex__grok_search_web_fetch`
- Model: `grok-chat-fast`
- Retrieval date: 2026-07-30
- Queries covered Salt custom modules/sync, `salt-cp`, roots, JSON output, async JID/cache/cancel semantics, accepted keys/targeting/`user.info`, and Claude Code MCP scopes/config/transports. Codex MCP configuration was checked against the official Codex manual.
