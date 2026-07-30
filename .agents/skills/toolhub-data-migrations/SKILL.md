---
name: toolhub-data-migrations
description: Change ToolHub generation-2 PostgreSQL schema and pgx persistence for the singleton account, encrypted secrets, immutable Library artifacts, unified Profiles, durable operations, desired snapshots, backups, settings, alerts, or actorless audit. Use for tables, constraints, migrations, transactions, JSONB manifests, idempotency, or state projection.
---

# ToolHub Data And Migrations

Read `AGENTS.md`, `internal/store/db.go`, every current migration,
`internal/domain/models.go`, `internal/store/query.go`, the relevant store file,
and all consumers of a changed JSON/state shape.

Generation 2 supports fresh databases only. `Store.Migrate` acquires advisory
lock `18480`, rejects any non-empty database without
`app_meta.schema_generation=2`, then applies embedded numbered SQL migrations
transactionally and records versions in `schema_migrations`.

`001_initial.sql` is the clean generation-2 initial schema. Once released, do
not rewrite it: add the next numbered migration. Never add migration logic that
imports generation-1 users, roles, Agent state, jobs, deployments, approvals,
or audit history.

Preserve schema invariants:

- one `account` row and hash-only sessions/CSRF;
- encrypted secret ciphertext bound to its UUID as AAD;
- immutable/content-addressed Skill artifacts and explicit current versions;
- one unified Profile with monotonic revision and Skill/MCP ID membership;
- operation/target status constraints and one active row per target;
- immutable desired snapshots with schema version, canonical hash, validated
  member arrays, and secret references only;
- one active snapshot pointer with health/reconcile metadata per target;
- actorless audit and state-change alerts;
- fixed backup retention metadata and singleton settings.

Use store methods and parameterized SQL. Keep related state and operation
creation in one transaction. Use `FOR UPDATE SKIP LOCKED` for claims and retain
the unique active-target constraint. Preserve per-target request JSON for retry.
Do not add SQL to HTTP handlers or an ORM.

Treat `SecretValues` as a caller-authorized decryption boundary, not a general
read API. Clear plaintext maps after Bridge calls and never project them into
JSONB, errors, audit, or logs.

For each change, test clean install, upgrade from the immediately prior
generation-2 migration, constraints, conflict/idempotency, claim concurrency,
terminal aggregation, manifest validation, and immutable triggers as relevant.

Verify:

```bash
GOCACHE=/tmp/toolhub-gocache go test ./internal/store ./internal/config ./internal/worker
GOCACHE=/tmp/toolhub-gocache go test ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
make docker-config
```
