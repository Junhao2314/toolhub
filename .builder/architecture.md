# ToolHub Generation-2 Architecture

ToolHub is a single-user Linux control plane. One Go HTTP binary embeds the
React UI and owns PostgreSQL state. A separate root-owned Linux Bridge performs
typed host operations over an HMAC-authenticated Unix socket. Salt is the only
remote execution transport.

## Process Boundaries

```text
Browser
  -> ToolHub /api/v1 (session + CSRF)
       -> PostgreSQL (Library, Profiles, operations, snapshots, audit)
       -> HMAC HTTP over /run/toolhub-bridge/bridge.sock
            -> local guarded filesystem adapter
            -> fixed toolhub-mcpm-relay.service controls
            -> fixed Salt CLI driver -> accepted Salt 3008.x minions
```

There is no Agent binary, WSS/enrollment, SSH fallback, RBAC, multi-user API,
deployment table, review/approval state, legacy jobs, or Profile activation.

## Core State

- `account` is a schema-enforced singleton. Username/password changes revoke
  all sessions.
- Skill artifacts are immutable and content-addressed; Library current versions
  can advance without changing an active desired snapshot.
- One Profile references Skill IDs and MCP server IDs. It does not pin versions.
- Preflight resolves exact current versions/revisions/secrets and issues a
  five-minute one-use token bound to Profile revision, target revision, and
  canonical manifest.
- Apply/edit/Restore create immutable pinned desired snapshots. Only the target
  pointer and projected health are mutable.
- All asynchronous work uses `operations` and `operation_targets`.

## Target Model

The local node has `local/claude` and `local/codex` Skill targets,
`local/shared-relay` for MCP, and read-only `local/hermes`. Each Salt node has
Claude/Codex writable Skill+MCP targets and a read-only Hermes inventory target.

Apply and target edit perform a destructive mirror only inside manageable
scope. Runtime built-ins, hidden/protected entries, `.system`, ToolHub-reserved
members, and non-user Claude MCP scopes are always excluded.

Local MCP uses one mcpm Profile named `toolhub`, one HTTP relay, and one
`toolhub-relay` anchor in each Claude/Codex user config. Remote MCP writes the
native Claude/Codex user scopes directly.

## Operation Flow

```text
HTTP mutation
  -> store creates operation + target rows transactionally
  -> worker claims one target (SKIP LOCKED)
  -> resolve artifacts and decrypt active secret references
  -> typed Bridge request with idempotency key
  -> local atomic adapter or Salt async JID
  -> backup (only before a write) -> stage -> validate -> atomic replace
  -> terminal target result
  -> pin snapshot / update health / record backup
  -> aggregate fleet status (including partial)
```

A target has at most one queued/running operation. Cancel marks only queued
targets cancelled. A running destructive step completes atomically.

## Reconcile

Every five minutes the scheduler queues targets with an active desired
snapshot. An overlapping tick marks one pending rerun instead of dispatching a
second target operation. Reconcile verifies/repairs pinned managed members and
preserves content added after Apply. No drift means no backup and no write.

Health is `healthy`, `drifted`, `repairing`, `blocked`, or `unavailable`.
Offline/failed targets are checked again on the next tick. Alerts/audit are
written only for meaningful state/error changes.

## Trust Boundaries

- Browser authentication is username/password + Argon2id + session + CSRF.
- MCP/provider secrets are encrypted with XChaCha20-Poly1305 and referenced by
  UUID in desired manifests.
- Bridge requests sign method, URI, timestamp, nonce, and exact body hash with
  an independent 32-byte key. Nonces and idempotency are durable.
- The container mounts no managed home. The Bridge has typed allowlists only.
- Salt commands use fixed argv/functions and per-minion literal IDs. Dynamic
  bundles are root-only, chunked, and removed after terminal handling.
- BoltDB stores safe recovery metadata, never plaintext secrets, archives,
  editable contents, or raw Salt output.

## Fresh Schema

`internal/store/migrations/001_initial.sql` is the generation-2 initial schema.
`Store.Migrate` rejects every non-empty database that does not already contain
`app_meta.schema_generation=2` before applying SQL. Old data is not migrated.

## Rollback

Target Restore creates a backup of current state, restores a catalog entry, and
pins the restored managed content as a new desired snapshot. A whole release
rollback restores the old application, old database volume, old delivery
packages, Salt namespace, and runtime backups together.
