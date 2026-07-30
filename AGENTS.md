# ToolHub AI Development Guide

ToolHub is a single-user Linux control plane for managing Claude and Codex
Skills/MCP locally and across Salt minions. It is a Go modular monolith with an
embedded React/Vite UI, PostgreSQL 16, and a separate root-owned Linux Bridge.

Module: `github.com/Junhao2314/toolhub` (Go 1.22). Web: React 18, Vite 8,
TypeScript 5.7, Node 22 in CI. Bridge journal: BoltDB. Remote execution: Salt
Master/minions 3008.x.

## Project Map

- `cmd/toolhub`: configuration, fresh-schema migration, singleton bootstrap,
  operation workers, scheduler, HTTP API, and embedded SPA.
- `cmd/toolhub-bridge`: Linux Unix-socket service, guarded adapters, durable
  journal recovery, and fixed host integration.
- `internal/httpapi`: Chi routes, session/CSRF middleware, validation, response
  envelopes, and Browser API handlers.
- `internal/store`: generation-2 schema, pgx persistence, Library, Profiles,
  operations, snapshots, backups, settings, secrets, and actorless audit.
- `internal/bridgeprotocol`: shared typed DTOs, manifest validation, protected
  scope rules, HMAC canonicalization, operation/health/error catalogs.
- `internal/bridgeclient`: fixed-path HMAC HTTP client over the Unix socket.
- `internal/bridge`: Bridge router, BoltDB journal/idempotency, recovery, and
  local/Salt adapter selection.
- `internal/runtime`: guarded local Skill manager, shared mcpm relay/profile,
  native anchors, backups, and atomic replacement.
- `internal/saltdriver`: fixed Salt CLI argv/function allowlists, asset
  publication, chunked staging, streaming JSON, async JID polling.
- `internal/skills` and `internal/market`: immutable package scanning/import and
  SkillsMP/Xiaping discovery.
- `internal/worker`: operation/control workers, five-minute reconcile,
  coalescing, update cron, and daily backup GC.
- `web/src`: manually routed React operations UI and typed API client.
- `api/openapi.yaml`: Browser `/api/v1` contract.
- `api/bridge-openapi.yaml`: private `/v1` Bridge contract.
- `packaging/systemd` and `packaging/salt`: host units/installer and Salt tests.

There is no ToolHub Agent, enrollment/WSS/SSH fallback, RBAC, multi-user API,
deployment/job/node-task model, review/approval flow, MCP delivery Profile, or
Profile activation.

## Read Before Changing Code

- Always read this file, the target package, adjacent tests, and the matching
  project skill under `.agents/skills`.
- Cross-layer work: `.builder/architecture.md`, `cmd/toolhub/main.go`,
  `cmd/toolhub-bridge/main.go`, `internal/domain/models.go`,
  `internal/bridgeprotocol/types.go`, `internal/bridge/adapter.go`, and
  `internal/worker/worker.go`.
- Browser API: both OpenAPI files as relevant, `docs/API.md`,
  `internal/httpapi/api.go`, target handlers/store methods, and the web client.
- Schema: `internal/store/migrations/001_initial.sql`, `internal/store/db.go`,
  and the target transaction code.
- UI: `web/src/App.tsx`, `web/src/api/client.ts`, `web/src/hooks/useData.ts`,
  `web/src/components/ui.tsx`, target page, and `web/e2e/smoke.spec.ts`.
- Security: `docs/SECURITY.md`, `internal/security`, `internal/bridgeprotocol`,
  Bridge journal/server, and the full caller path for any secret.
- Host/Salt: `docs/BRIDGE.md`, `docs/SALT.md`, runtime/saltdriver packages,
  systemd units, installer, and Salt Python tests.

Use the matching project skills:

- `.agents/skills/toolhub-architecture`
- `.agents/skills/toolhub-local-development`
- `.agents/skills/toolhub-testing-quality`
- `.agents/skills/toolhub-api-backend`
- `.agents/skills/toolhub-frontend`
- `.agents/skills/toolhub-data-migrations`
- `.agents/skills/toolhub-security-auth`
- `.agents/skills/toolhub-agent-integrations` (Bridge/Salt/runtime packaging)

## Core Invariants

- PostgreSQL enforces one account. Bootstrap credentials are ignored after it
  exists; username/password changes revoke every session.
- Update discovery can import and advance Library current versions, but never
  Apply. Imported Skill artifacts remain immutable/content-addressed.
- A Profile references Skill/MCP IDs. Preflight resolves exact current
  versions/revisions/secrets into a five-minute one-use token bound to Profile
  revision, target revision, and canonical manifest.
- Apply/edit/Restore create immutable desired snapshots. The active target
  pointer and health projection are mutable; snapshot manifests are not.
- Apply mirrors only manageable scope. Protected/built-in/hidden entries,
  `.system`, and non-user Claude MCP scopes are excluded.
- Reconcile repairs pinned members only and preserves later unmanaged content.
  A no-op does not create a backup; every actual write creates one first.
- A target has at most one queued/running operation. Overlapping reconcile
  coalesces one rerun. Cancel affects queued targets only.
- Local MCP is the independent `local/shared-relay` target. Claude/Codex share
  one mcpm `toolhub` Profile and one HTTP relay; local Skill targets remain
  runtime-specific.
- Remote Claude/Codex MCP writes native user scope. Hermes is always read-only.
- Secrets are encrypted at rest, referenced by UUID in manifests, write-only in
  the browser, and ephemeral in Bridge/Salt calls.
- BoltDB must never store plaintext secrets, archives, editable config, or raw
  Salt output. Recovery metadata is hashes/routing/JIDs only.
- Bridge exposes typed operations over a `0660` HMAC Unix socket. No shell,
  arbitrary executable/path/Salt function/systemd unit, or TCP listener.
- Salt discovery uses accepted keys only; writes require `3008.x`, a canonical
  managed home, fixed argv/functions, synced extensions, and chunked staging.
- ToolHub never edits `/etc/salt/master`, writes Hermes, or mounts managed homes
  into the container.

## Data And Operation Flow

For every state-changing path, trace:

```text
Browser handler
  -> transactional operation/target creation
  -> worker claim and request metadata
  -> exact Bridge client method + idempotency key
  -> Bridge route/path-authoritative kind
  -> local adapter or Salt stage/dispatch/JID poll
  -> target result + backup
  -> snapshot/health/operation projection
  -> UI and audit exposure
```

Operation status is `queued`, `running`, `succeeded`, `partial`, `failed`, or
`cancelled`. Target health is `healthy`, `drifted`, `repairing`, `blocked`, or
`unavailable`. A fleet operation can be partial; always preserve per-target
terminal results.

## Reuse Before Adding Code

- HTTP: `decodeJSON`, `writeJSON`, `writeItems`, `writeError`,
  `handleStoreError`, `requestIdempotencyKey`.
- Store: existing operation/snapshot/profile/library transactions; do not add
  SQL in handlers.
- Security: password/token/cipher/redaction helpers and manifest secret-ID
  extraction.
- Bridge: existing DTOs, error catalog, manifest validation, journal safety,
  `bridgeclient.Client`, and fixed adapter interfaces.
- Runtime: `Manager`, `RelayManager`, guarded path helpers, backup/atomic-write
  helpers.
- Salt: `Driver` allowlists, `PublishAssets`, `Stage`, `Dispatch`, `Poll`, and
  streaming JSON parser. Never build a parallel generic runner.
- Web: `api` singleton, `useData`, and shared UI primitives.

## Schema Rules

Generation 2 intentionally rewrote the clean initial migration. Once deployed,
`001_initial.sql` is an applied migration and must not be rewritten again; add
the next numbered migration for generation-2 changes.

`Store.Migrate` first inspects every non-empty database. Missing or non-2
`app_meta.schema_generation` must fail before applying SQL or starting HTTP.
Never add an in-place generation-1 data migration.

Desired manifest JSONB must retain schema version, canonical hash validation,
explicit structure validation, and secret-reference-only semantics.

## Common Commands

From repository root:

```bash
GOCACHE=/tmp/toolhub-gocache go test ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
GOCACHE=/tmp/toolhub-gocache go build -o bin/toolhub ./cmd/toolhub
GOCACHE=/tmp/toolhub-gocache go build -o bin/toolhub-bridge ./cmd/toolhub-bridge
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s packaging/salt/tests -p '*_test.py'
cd web && npm ci --ignore-scripts
cd web && npm audit --audit-level=high
cd web && npm run typecheck && npm run build
make docker-config
```

Integrated fresh smoke:

```bash
docker compose up -d --build --wait
curl --fail http://127.0.0.1:18480/healthz
TOOLHUB_SMOKE_USERNAME=admin TOOLHUB_SMOKE_PASSWORD=... sh scripts/smoke-api.sh
TOOLHUB_E2E_USERNAME=admin TOOLHUB_E2E_PASSWORD=... \
  sh -c 'cd web && npm run test:e2e'
```

Playwright requires a live backend and `/usr/bin/google-chrome`. Vite runs on
`127.0.0.1:18481` and proxies API calls to `18480`.

`make lint` runs `gofmt -w`; `make web`/`make build` rewrite ignored embedded
dist files. Use those mutating targets knowingly. Never hand-edit
`cmd/toolhub/dist` or commit `.env`, runtime keys, generated dist, BoltDB, or
Playwright output.

## High-Risk Boundaries

Treat auth/session middleware, encryption/secret reads, desired-manifest
validation, Bridge HMAC/replay/idempotency, journal recovery, filesystem path
guards, atomic replacement/rollback, Salt argv/staging/JID recovery, and relay
systemd control as high-risk. Add a focused regression test before changing an
invariant, then run the full relevant gates.

Do not infer universal redaction. Audit, operation metadata, browser responses,
Bridge journal, logs, and Salt bundles are separate boundaries and each caller
must be checked.

## Known External Limit

Four of the five currently accepted Salt minions report `3008.x` and are
online. `racknerd-73661c5` still times out and is projected as unavailable. On
2026-07-30, a read-only `salt:racknerd/claude` canary passed asset publication,
module/state sync, managed-user lookup, chunked staging, fixed Salt execution,
revision capture, and staging cleanup. Fleet rollout must exclude or recover
the unavailable minion; do not weaken the 3008.x gate or accepted-key
targeting to hide that condition.

## Completion Checklist

- Confirm Browser and Bridge OpenAPI match routes and DTOs.
- Run focused tests, then full Go/Python/web gates.
- Run Compose config and feasible fresh smoke/Playwright.
- Inspect `git diff` for secrets, generated output, stale legacy surfaces,
  accidental migration rewrites, or unrelated changes.
- Report exactly what ran and any external acceptance blocker.
