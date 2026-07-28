---
name: toolhub-testing-quality
description: Test and troubleshoot ToolHub Go, React, API smoke, Docker, and Playwright changes. Use when fixing regressions, validating a feature, diagnosing CI failures, or choosing focused coverage.
---

# ToolHub Testing and Quality

## When to use

Regressions, feature validation, CI failures, choosing what to test, or deciding whether unit vs smoke vs e2e is enough.

## Read first

Read `AGENTS.md`, `Makefile`, `.github/workflows/ci.yml`, `scripts/smoke-api.sh`, `web/playwright.config.ts`, `web/e2e/smoke.spec.ts`, and the `*_test.go` / e2e files adjacent to the changed package.

## Verification ladder

1. Narrowest package test: `go test ./internal/<pkg>/...`
2. Full unit: `go test ./...` then `go vet ./...`
3. Concurrency-sensitive (hub/worker/session/queue): `go test -race ./...` (**not** CI-gated)
4. UI/API shape: `cd web && npm run typecheck && npm run build`
5. Dependency/CI: `cd web && npm audit --audit-level=high`
6. Integrated: `make docker-config` → Compose up → `sh scripts/smoke-api.sh` → `cd web && npm run test:e2e`

Before claiming “tested,” state which gate ran: unit / smoke-api / Playwright / compose.

## CI gates (`.github/workflows/ci.yml`)

| Job | What |
|-----|------|
| `go-test` | `go test ./...`, `go vet ./...` (Go 1.22.x) |
| `agent-build` | matrix linux/mac/windows `go build ./cmd/toolhub-agent` (`fail-fast: false`) |
| `web` | `npm ci --ignore-scripts`, `npm audit --audit-level=high`, typecheck, build (Node 22.12) |
| `container-smoke` | Compose up with fixed bootstrap + `TOOLHUB_SECURE_COOKIES=false` → smoke-api → Playwright; **always** dumps `docker compose logs` |

**Not gated:** `-race`, Go coverage, frontend unit/lint, artifact upload of reports.

Historical `docs/workflows/**` may mention race; **Makefile/CI do not run race**. Prefer CI/Makefile as truth.

## Smoke API (`scripts/smoke-api.sh`)

Requires `TOOLHUB_SMOKE_EMAIL` / `TOOLHUB_SMOKE_PASSWORD`; base `TOOLHUB_SMOKE_URL` default `http://127.0.0.1:18480`.

Steps: login 200 + extract `csrfToken` → GET overview 200 → GET nodes 200 with `"isLocal":true` → POST `/api/v1/sync` **without** CSRF expects **403** → logout with `X-CSRF-Token` expects **204**.

CI fixtures: master key `0123456789abcdef0123456789abcdef`, admin `admin@toolhub.local` / `ToolHubLocal-2026-ChangeMe` (local-only).

## Playwright

- No `webServer` — start backend first.
- `baseURL`: `TOOLHUB_E2E_URL` or `http://127.0.0.1:18480`
- Hardcoded `executablePath: '/usr/bin/google-chrome'`, `--no-sandbox`
- `workers: 1`; projects: desktop 1440×960 + mobile Pixel 7
- Env: `TOOLHUB_E2E_EMAIL`, `TOOLHUB_E2E_PASSWORD` required
- Smoke path: login → Overview → Skills (layout non-overlap + overflow) → Nodes (project host, SSH modal, enroll modal) → **zero console errors**
- Output: `test-results/playwright`, HTML under `playwright-report/`

Note: smoke-api and e2e use **different env var names** (`TOOLHUB_SMOKE_*` vs `TOOLHUB_E2E_*`) even when values match in CI.

## Existing unit coverage map

Framework: stdlib `testing` only (no testify/sqlmock/testcontainers). No shared fixtures; in-test `t.TempDir`, hand-built ZIPs, `httptest`, `t.Setenv`. **No PostgreSQL test harness.**

| Package | What is proven |
|---------|----------------|
| `config` | `TOOLHUB_LOCAL_NODE_NAME` override vs default `project-host` |
| `domain` | `Principal.HasRole` matching |
| `security` | password round-trip; AEAD context binding; recursive redaction |
| `protocol` | canonical task signing bytes (key-order stable) |
| `skills` | ZIP traversal/symlink reject; hash stability; risk signals |
| `runtime` | refuse unmanaged target; idempotent deploy |
| `store` | SSH string validation only (no DB) |
| `market` | search cache; rate-limit error |
| `policy` | most-specific enabled policy selection |
| `httpapi` | `enrollmentServerURL` origin safety only |

**No `*_test.go` today for:** `agentclient`, `agenthub`, `agentservice`, `ai`, `remote`, `worker`, most of `store`/`httpapi`, both `cmd/*`.

## AI must-know gotchas

- `go test ./...` green ≠ auth/CSRF/RBAC/DB/migration/worker/agent paths verified.
- Job `succeeded` is orchestration success; **no automated test** currently enforces that distinction.
- Smoke CSRF check is only bare POST `/sync` → 403, not a full middleware matrix.
- E2E is layout/nav smoke, not RBAC, import, deploy, MCP secrets, or Agent WSS.
- `make lint` mutates sources (`gofmt -w`); `make web` rewrites ignored generated files under `cmd/toolhub/dist`.
- Local smoke needs `TOOLHUB_SECURE_COOKIES=false`.
- Playwright requires system Chrome at the pinned path.

## Common failures

| Symptom | Check |
|---------|-------|
| Login fails in browser on local HTTP | `TOOLHUB_SECURE_COOKIES` still true |
| smoke-api exits early | missing `TOOLHUB_SMOKE_EMAIL`/`PASSWORD` or wrong bootstrap |
| Playwright cannot launch | no `/usr/bin/google-chrome` or backend not on 18480 |
| Compose up but curl fails | inspect ToolHub health with `docker compose ps` and dump `docker compose logs --no-color` |
| CSRF 403 in client | token not in `sessionStorage` key `toolhub.csrf` after login/session |
| CI web job fails audit | dependency vulnerability ≥ high |

## Debug method

1. Read API error envelope `error.code` + `requestId`; do not infer from UI text alone.
2. Reproduce with Compose + `scripts/smoke-api.sh`; dump compose logs.
3. For UI, use CI credential env names and preserve console-error + layout assertions in `smoke.spec.ts`.
4. For queue/Agent, inspect job and node-task status transitions, signatures, retry/cancel, offline delivery, SSH fallback, and selector scope (`skillId` vs `skillIds` mismatch).
5. Distinguish orchestration success (`jobs`) from actual state (`deployments` after `CompleteTask`).

## Reuse

Reuse package-local `*_test.go` patterns (`t.TempDir`, `httptest`, `t.Setenv`), `scripts/smoke-api.sh`, `web/e2e/smoke.spec.ts`, and CI env var names. Do not invent a second smoke suite or alternate Playwright config without need.

## Prohibitions

- Do not replace a failing integration test with a mock-only test when behavior crosses PostgreSQL, Docker, API, or browser.
- Do not invent a broad DB/auth suite without a real Postgres fixture strategy.
- Do not claim CI runs race or coverage when it does not.

## Verification

Add regression coverage for the invariant that failed, then run the relevant higher-level gate from the ladder above. For doc-only work, path/command existence + fact spot-checks are enough.
