---
name: toolhub-data-migrations
description: Change ToolHub PostgreSQL schema, pgx store queries, transactions, encrypted secrets, jobs, or desired/actual state safely. Use when adding persisted fields, tables, migrations, or data-layer behavior.
---

# ToolHub Data and Migrations

## When to use

New columns/tables, migration files, store query/transaction changes, secrets encryption, jobs/node_tasks, or desired/actual deployment state.

## Read first

Read `AGENTS.md`, `internal/store/db.go`, all files under `internal/store/migrations/` (currently `001_initial.sql` and `002_username_credentials.sql`), `internal/domain/models.go`, `internal/store/query.go`, the relevant store file, and its tests. Read consuming HTTP/worker/Agent code before changing a JSON shape or state transition.

## Migration mechanism

- Files: `//go:embed migrations/*.sql` in `db.go`.
- `Store.Open` builds pgxpool (min 2, max 20, simple protocol) but **does not migrate**. `cmd/toolhub` must call `Store.Migrate`.
- `Migrate`: advisory lock `pg_advisory_lock(18480)`; ensures `schema_migrations(version bigint PK, applied_at)`; lexical sort of `*.sql`; version = numeric prefix before `_`; skip if version exists; apply body + insert version in one transaction.
- **No checksums, no down migrations.** Editing an already-applied file is invisible to the runner.

### Current migrations

| File | Purpose |
|------|---------|
| `001_initial.sql` | Full initial schema + seed roles/policies/market provider |
| `002_username_credentials.sql` | `users.username` (NOT NULL, format check, unique), `password_change_recommended`, lowercased email unique index; backfills usernames from email local-part |

**Never rewrite** applied files. Next change is `003_*.sql` (or higher).

## Tables (application)

`roles`, `users`, `user_roles`, `sessions`, `encrypted_secrets`, `nodes`, `node_connections`, `enrollment_tokens`, `runtimes`, `skill_sources`, `skills`, `skill_artifacts`, `skill_versions`, `deployments`, `update_policies`, `sync_policies`, `updates`, `jobs`, `node_tasks`, `mcp_servers`, `mcp_profiles`, `mcp_profile_servers`, `mcp_deployments`, `ai_providers`, `market_providers`, `audit_events`, plus `schema_migrations`.

Seeded: roles admin/operator/viewer; global update/sync policies; SkillsMP market provider.

## JSON helpers and lists

- `JSONList`: `jsonb_agg(to_jsonb(q))` over subquery → raw array.
- `JSONObject`: `to_jsonb(q)` or `ErrNotFound`.
- HTTP wraps lists as `{ items: [...] }` via `writeItems`.

## Jobs and tasks

- `EnqueueJob(kind, payload, dryRun, createdBy)` → `pending`, default `max_attempts=5`.
- `ClaimJob`: `FOR UPDATE SKIP LOCKED` on due `pending` → `running`.
- `FinishJob` → `succeeded`; `FailJob` quadratic backoff then `failed`; `CancelJob` cancels job + linked `node_tasks` in `pending`/`delivered` only.
- Job `succeeded` ≠ Agent task done ≠ deployment `in_sync`.

## Secrets

- Table `encrypted_secrets`: id, name UNIQUE, kind, ciphertext, metadata, created_by.
- Encrypt with `security.Cipher`; **AAD = secret UUID**. Re-encrypt under a new id requires new ciphertext.
- `SecretValue(id)`: decrypt by id only — **no actor/RBAC**.
- `AgentSecretValue(nodeID, id)`: only `kind='mcp-env'` AND secret referenced in `env_refs` of an **enabled desired** MCP deployment on that node.
- MCP server create embeds env secrets in the same tx as `kind='mcp-env'`, names like `mcp:{serverID}:{key}`.

## Desired / actual

**Skills `deployments`:** `desired_version_id`, `actual_version_id`, `previous_version_id`, `desired_enabled`, `actual_enabled`, `state` (`pending|in_sync|drift|conflict|failed|rolling_back|archived`). Unique `(node_id, runtime_kind, skill_id)`.

- Import → review (`ReviewSkill`) → `SetSkillTargets` upserts desired → `PendingSkillDeployments` (states pending/drift/failed/rolling_back) → agent `deploy_skill` → `CompleteTask` sets actual + `in_sync` on success.
- `ApproveUpdate` promotes candidate version and sets skill deployments desired/`pending`.
- `RollbackDeployment`: swap desired↔previous, state `rolling_back`.

**MCP `mcp_deployments`:** `desired_enabled`, `desired_hash`, `actual_hash`, `state`. Success on `apply_mcp` copies desired→actual.

`CompleteTask` projects deployment state primarily for `deploy_skill` / `apply_mcp`; other task kinds update the task row only.

## Soft-delete / archive

- Nodes/skills: `archived_at` (+ status); lists filter non-archived.
- `PurgeExpiredArchives`: hard-delete skills with `archived_at < now()-30 days` (job kind `archive_purge`).
- Protected skills block archive. MCP servers: hard delete.
- Users: `disabled`; MCP profiles: `enabled` flag.

## Upsert-only “replace”

`SetSkillTargets`, `SetMCPDeployments`, `ReplaceInventory` upsert/ON CONFLICT only — **omitted rows are not deleted**. Do not document them as full replace without changing and testing semantics.

## Safe schema workflow

1. Define the invariant and ownership of the new data.
2. Add a new monotonically numbered SQL migration; never rewrite applied files.
3. Add focused store methods with parameterized SQL; use a transaction for related writes; preserve rollback defers.
4. Update domain/API/worker types and JSON projections together (including username/login paths if touching users).
5. Test constraints, not-found, idempotency, and both **clean install** and **upgrade from prior applied versions**.
6. Run Compose startup so `Migrate` applies the new file.

## Reuse

Prefer existing store methods over new ad-hoc SQL. Use `JSONList`/`JSONObject`, job helpers, `CreateSecret`/`AgentSecretValue`, and archive patterns already in schema. Do not add an ORM.

## Prohibitions

- Handler SQL / expanding `Pool().Exec` escape hatch.
- Rewriting `001_*.sql` or `002_*.sql` after apply.
- Treating `SecretValue` as an authorized API.
- Auto-install marketplace skills via store side effects.
- Inventing soft-delete columns where `archived_at` already exists.

## Verification

```bash
go test ./internal/store/... ./internal/config/...
go test ./...
go vet ./...
make docker-config
docker compose up -d --build --wait
curl --fail http://127.0.0.1:18480/healthz
# Confirm schema_migrations contains new version on upgrade path
```

Suggested extra checks when touching secrets/jobs: ClaimJob concurrency (`SKIP LOCKED`), `AgentSecretValue` negative cases (wrong node / disabled / non-mcp-env), upgrade from DB that already has `001` only.
