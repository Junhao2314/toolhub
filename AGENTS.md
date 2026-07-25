# ToolHub AI Development Guide

ToolHub is a Tailnet-oriented operations console for managing Codex, Claude, and Hermes Skills and MCP configuration across nodes. It is a Go modular monolith with an embedded React/Vite UI, a PostgreSQL source of truth, and a separate cross-platform Go Agent.

Module path: `github.com/toolhub-dev/toolhub` (Go 1.22). Web: React 18 + Vite 8 + TypeScript 5.7 (Node 22 in CI/Docker). Database: PostgreSQL 16.

## Project map

- `cmd/toolhub`: control-plane entrypoint; loads configuration, opens/migrates PostgreSQL, bootstraps the admin and project-host node, then starts API, WebSocket hub, workers (concurrency 4), and scheduler. Embeds SPA from `cmd/toolhub/dist`.
- `cmd/toolhub-agent`: node CLI/service entrypoint; supports `enroll`, `run`, `scan`, and signed `run-task` execution (plus `agentservice` platform wrappers).
- `internal/httpapi`: Chi REST transport, validation, response envelopes, authentication, CSRF, and RBAC boundaries.
- `internal/store`: pgx queries, transactions, embedded SQL migrations, jobs, nodes, Skills, MCP, secrets, audit, and desired/actual state.
- `internal/security` and `internal/policy`: password/session/CSRF handling, encryption, redaction, task signing, username normalization, and policy resolution helpers (policy package is library-only today; scheduler loads schedules from the store).
- `internal/runtime`: runtime inventory and managed Skill/MCP filesystem operations.
- `internal/agenthub`, `internal/agentclient`, `internal/remote`: Agent WSS protocol, enrollment/task execution, and pinned SSH fallback.
- `internal/skills` and `internal/market`: package safety/provenance and SkillsMP search/import support.
- `internal/worker`: queued job execution plus scheduled update discovery and sync reconciliation.
- `internal/ai`: OpenAI-compatible structured recommendations (never auto-install).
- `web/src`: manually routed React pages, shared UI primitives, data hook, and API client.
- `api/openapi.yaml`: checked-in HTTP contract for `/api/v1`; `docs/API.md` explains the common envelope and auth expectations.
- `packaging`, `Dockerfile`, `compose.yaml`, `.github/workflows/ci.yml`: platform services, container build, local deployment, and CI.

## Before changing code

Read the smallest relevant set first:

- Always read this file, the target package, its tests, and the matching project skill under `.agents/skills`.
- For cross-layer work, read `.builder/architecture.md`, `cmd/toolhub/main.go`, `cmd/toolhub-agent/main.go`, `internal/domain/models.go`, `internal/protocol/task.go`, and `internal/worker/worker.go`.
- For API changes, read `api/openapi.yaml`, `docs/API.md`, `internal/httpapi/api.go`, and the relevant handler/store methods.
- For schema changes, read `internal/store/migrations/` (currently `001_initial.sql` and `002_username_credentials.sql`), `internal/store/db.go`, and the relevant store transaction code.
- For UI changes, read `web/src/App.tsx`, `web/src/api/client.ts`, `web/src/hooks/useData.ts`, `web/src/components/ui.tsx`, and the target page.
- For security-sensitive changes, read `docs/SECURITY.md` plus `internal/security`, `internal/policy`, and the relevant auth/middleware/store code.

Use these project skills when their trigger matches:

- `.agents/skills/toolhub-architecture`
- `.agents/skills/toolhub-local-development`
- `.agents/skills/toolhub-testing-quality`
- `.agents/skills/toolhub-api-backend`
- `.agents/skills/toolhub-frontend`
- `.agents/skills/toolhub-data-migrations`
- `.agents/skills/toolhub-security-auth`
- `.agents/skills/toolhub-agent-integrations`

## Core invariants

- Updates discover candidates only; approval creates the desired version, and sync reconciles approved desired state.
- Deployments track desired, actual, and previous versions. Rollback is a new desired-state transition (store swaps previous↔desired), not an in-place edit of managed runtime files.
- Imported Skill artifacts are immutable and identified by source commit, canonical SHA-256, provenance, manifest, and scan report.
- Agent tasks use a closed typed protocol and HMAC signatures over canonical JSON (`TaskSigningBytes` in package `internal/protocol` plus `SignPayload` in package `internal/security`). Do not add arbitrary shell execution.
- Agent task kinds accepted by the agent `Executor` are only: `scan_inventory`, `deploy_skill`, `apply_mcp`.
- Worker job kinds in `internal/worker/worker.go` are: `inventory_scan`, `skill_import`, `update_check`, `sync`, `rollback` (same handler as sync), `mcp_sync`, `mcp_health` (currently a no-op stub), `archive_purge`.
- Existing Codex/Claude/Hermes runtime content is read-only during inventory/onboarding. Managed activation requires ToolHub's marker and uses content-addressed cache, staging, backup, and rename replacement.
- Secrets are encrypted at rest, referenced by ID, and must not be returned in ordinary browser API responses or logs. The intentional plaintext exception is the authorized `/agent/v1/secrets/{secretID}` response for an enabled MCP deployment on that node.
- The control plane binds to loopback by default; Tailscale Serve/ACL is external infrastructure. Do not expose the container port publicly.

Jobs, `node_tasks`, and deployments are separate state machines. A job marked succeeded currently means orchestration/dispatch completed; it is not proof that the Agent completed the task or that actual deployment state is in sync. A disconnected running `node_task` may not be requeued automatically. Hub reconnect redelivers only `pending`/`delivered` tasks.

When changing a task or reconciliation flow, trace the producer, worker consumer, Agent executor, `CompleteTask` projection, and both WSS and SSH paths. The WSS send/mark-delivered and SSH fallback paths are not one atomic transaction.

**Selector field names are a real contract.** Worker skill sync reads `nodeIds`, `skillIds`, `scopeType`, and `scopeId`. Today:

- `setSkillTargets` enqueues `{"skillId": ...}` (singular) — **ignored** by the worker → can reconcile **all** pending skill deployments.
- `rollbackDeployment` enqueues `{"deploymentId": ...}` — **ignored** → broader pending-set reconcile (state `rolling_back` still helps the row that was swapped).
- `mcp_sync` enqueues `{"profileId": ...}` — **ignored**; worker processes all pending MCP deployment IDs.
- Manual `POST /sync` with plural `nodeIds`/`skillIds` **is** consumed.

Verify producer payload keys against `internal/worker/worker.go` before assuming an operation is scoped.

## Reuse before adding code

- Use `internal/httpapi` helpers: `API.Router` middleware, `decodeJSON`, `writeJSON`/`writeItems`/`serveList`, `writeError`, and `handleStoreError`.
- Use store `JSONList`/`JSONObject` and existing resource/query methods instead of new SQL in handlers.
- Use store `EnqueueJob` and worker processing for asynchronous operations. Job kinds must match the worker switch; agent task kinds must match `Executor.Execute`.
- Use `internal/security` helpers for passwords, tokens, encryption, redaction, signatures, and `NormalizeUsername`.
- Use `TaskSigningBytes` plus `SignPayload`/`VerifyPayload` for any task signing change.
- Use `web/src/api/client.ts` (`api` singleton), `useData`, and `web/src/components/ui.tsx` instead of creating parallel request, loading, error, modal, or form primitives.
- Use `internal/skills` scanning/import functions, runtime `Deployer`, and agentclient `Executor` for their existing safety boundaries.
- Do not invent universal API-response redaction middleware; call `RedactMap`/`RedactJSON` on the specific audit/inventory/AI paths that already use them.

## Common commands

From the repository root:

    go test ./...
    go vet ./...
    make test
    make lint
    make build
    make docker-config

For the web:

    cd web && npm ci --ignore-scripts
    cd web && npm run typecheck
    cd web && npm run build
    cd web && npm run test:e2e

For local Compose smoke:

    cp .env.example .env
    # set TOOLHUB_MASTER_KEY (32 raw or base64-32), bootstrap password; optional TOOLHUB_BOOTSTRAP_ADMIN_USERNAME (default admin)
    docker compose up -d --build --wait
    curl --fail http://127.0.0.1:18480/healthz
    TOOLHUB_SMOKE_EMAIL=... TOOLHUB_SMOKE_PASSWORD=... sh scripts/smoke-api.sh

Ports: host control plane `127.0.0.1:18480` (Compose publishes loopback only); Vite dev `127.0.0.1:18481` with proxy to `:18480`. Binaries: `bin/toolhub`, `bin/toolhub-agent`.

The local HTTP smoke profile must set `TOOLHUB_SECURE_COOKIES=false`. The normal default is secure cookies. `config.Load` requires `TOOLHUB_DATABASE_URL` and a 32-byte `TOOLHUB_MASTER_KEY`. Bootstrap email/password are required by `BootstrapAdmin` when creating the first admin; username defaults to `admin` via `TOOLHUB_BOOTSTRAP_ADMIN_USERNAME`. Optional: `TOOLHUB_SESSION_TTL` (default `12h`, range 15m–168h), `TOOLHUB_DATA_DIR` (default `/data`), `TOOLHUB_PUBLIC_URL`, `TOOLHUB_TIMEZONE` (default `Asia/Shanghai`), `SKILLSMP_API_KEY`.

## Change-specific paths

- New API behavior: handler in `internal/httpapi`, store/service call, `api/openapi.yaml`, web client/page if used by the UI, and focused Go/UI verification.
- New persisted state: next numbered SQL migration under `internal/store/migrations/`, store methods/transaction, domain types or JSON shape, API contract, and migration-backed Compose verification. Never rewrite applied migrations.
- New Agent capability: domain/protocol task kind, control-plane enqueue/authorization, agent executor/runner, runtime implementation, and idempotency/error tests across WSS and SSH.
- New Skill flow: `internal/skills` safety/provenance first, review/approval and desired-state transitions second; never install marketplace results automatically.
- New UI page/action: `App.tsx` navigation and role gate, shared primitives, `api` client methods, page data/error/reload behavior, and Playwright smoke coverage when layout/auth/navigation is affected.

## Generated and high-risk files

- `cmd/toolhub/dist` is copied from `web/dist` by `make web` and Dockerfile; do not hand-edit generated assets.
- Applied migrations under `internal/store/migrations/` must not be rewritten. Add the next numbered file (for example `003_*.sql`). `Store.Migrate` embeds `migrations/*.sql`, uses advisory lock `18480`, and records versions in `schema_migrations` **without checksums or down migrations**.
- `.env` and credentials are local runtime state. Do not commit secrets or put real values in examples, logs, tests, or screenshots.
- Treat `internal/security`, `internal/httpapi/middleware.go`, `internal/protocol/task.go`, `internal/runtime/deploy.go`, `internal/skills/package.go`, and SSH fallback as high-risk boundaries. Preserve their tests and add regression coverage before changing behavior.
- Redaction is used in audit metadata, inventory persistence, and AI input sanitization, but there is no single universal API-response redaction middleware. Verify the full caller path before treating a `docs/SECURITY.md` claim as enforcement.
- Store `SecretValue` reads and decrypts by ID without actor authorization; use the caller-specific `AgentSecretValue` boundary for Agent MCP access and add authorization before exposing any other secret.
- `make lint` runs `gofmt -w` and `make web` removes/copies generated dist assets; treat both as mutating build targets. Prefer `npm ci --ignore-scripts` for CI parity (`make web` currently uses bare `npm ci`).

## Known limits and current rough edges

- The OpenAPI file currently describes the `/api/v1` browser API, not `/healthz` or the `/agent/v1` enrollment, WebSocket, artifact, and secret endpoints.
- HTTP coverage is mostly focused unit coverage; there is no broad router/auth/CSRF/role integration suite or real database fixture in the repository. Packages without `*_test.go` include most of `store`/`httpapi`/`worker`/`agenthub`/`agentclient`/`remote`/`ai`.
- Existing `internal/httpapi/resources.go` and `settings.go` still contain direct `Pool().Exec` calls. Do not expand that pattern; move new persistence behavior into `internal/store`, and treat refactoring those paths as a separate risk-reviewed change.
- Some handlers update state and enqueue a job in separate operations (`setSkillTargets`, `rollbackDeployment`, `deployMCPProfile`). When changing those flows, verify the failure semantics instead of assuming atomicity.
- Playwright has no webServer setting. The backend must already be running; tests use `TOOLHUB_E2E_EMAIL` and `TOOLHUB_E2E_PASSWORD`, pin Chrome at `/usr/bin/google-chrome`, and run workers=1 (desktop + mobile projects).
- Compose healthchecks only Postgres; app readiness is `GET /healthz`. Compose publishes only `127.0.0.1:18480`.
- CI (`.github/workflows/ci.yml`): `go-test`, matrix `agent-build` (linux/mac/windows), `web` (audit+typecheck+build), `container-smoke` (compose + `scripts/smoke-api.sh` + Playwright). Race tests are not gated. Prefer Makefile/CI over historical `docs/workflows/**` for current gates.
- `mcp_health` job returns a stub (`queued_on_next_reconcile`) and does not run real health probes today.
- `SetSkillTargets`, `SetMCPDeployments`, and `ReplaceInventory` are upsert-only; omitted rows are not deleted.

## Completion checklist

- Confirm every referenced path and command still exists.
- Run focused tests first, then the relevant make/CI command.
- Run `gofmt` on changed Go files and web typecheck/build for UI changes.
- Update `api/openapi.yaml` and `docs/API.md` when the contract changes.
- Check `git diff` for accidental generated files, secrets, migration rewrites, or unrelated edits.
