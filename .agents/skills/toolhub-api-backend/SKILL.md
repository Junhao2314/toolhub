---
name: toolhub-api-backend
description: Add, modify, or troubleshoot ToolHub Go HTTP API handlers, routes, OpenAPI contracts, validation, response envelopes, jobs, and RBAC. Use when changing REST behavior or connecting UI actions to the control plane.
---

# ToolHub API and Backend

## When to use

New or changed REST endpoints, OpenAPI/docs contract work, job enqueue from handlers, RBAC/CSRF issues, or wiring UI actions to the control plane.

## Read first

Read `AGENTS.md`, `api/openapi.yaml`, `docs/API.md`, `internal/httpapi/api.go`, `internal/httpapi/middleware.go`, the target handler file, and the store methods it calls. Check the matching `web/src` page and `web/src/api/client.ts` when the browser uses the endpoint. For async work, also read `internal/worker/worker.go` payload structs.

## Route groups (`API.Router`)

| Mount | Auth | Notes |
|-------|------|-------|
| `GET /healthz` | none | DB ping |
| `POST /agent/v1/enroll` | enrollment token in body | one-time |
| `GET /agent/v1/connect` | hub | WSS |
| `GET /agent/v1/artifacts/{versionID}` | `X-ToolHub-Node-ID` + Bearer | |
| `GET /agent/v1/secrets/{secretID}` | same agent headers | intentional plaintext MCP value |
| `POST /api/v1/auth/login`, `GET /api/v1/auth/session` | public | login rate limit 10/10m |
| `/api/v1/*` | `authenticate` + `verifyCSRF` | session cookie |

Inside authenticated `/api/v1` (session only, **no role**): logout, me, csrf, overview, account credential self-service.

**Read** (`admin|operator|viewer`): nodes, skills, sources, deployments, updates, jobs, market search, MCP list endpoints.

**Ops** (`admin|operator`): enrollment, node update/archive/scan/connections, skill import/upload/archive/targets, rollback, update check, sync, cancel job, recommendations, MCP CRUD/health/deploy.

**Admin only**: users, audit, skill review, update approve, settings, AI providers.

Login accepts **username or email** (`identifier`). CSRF: GET/HEAD/OPTIONS skip; unsafe methods need `X-CSRF-Token` matching session hash. Cookie: `toolhub_session` HttpOnly, SameSite=Strict, Secure from config.

## Reuse

- `decodeJSON` — `MaxBytesReader` + `DisallowUnknownFields` + single-object body (`auth.go`)
- `writeJSON` / `writeItems` (`{items}`) / `writeError` (`error.{code,message,requestId}`) / `handleStoreError` (`ErrNotFound`→404 else 500)
- `serveList` for list handlers
- `EnqueueJob` for async work; return **202** + job body for queued ops

## Job enqueue map (202 + job)

| Route | Handler | Job kind | Notes |
|-------|---------|----------|-------|
| `POST /nodes/{id}/scan` | `scanNode` | `inventory_scan` | |
| `POST /skills` | `importSkill` | `skill_import` | |
| `POST /discoveries/{id}/adopt-skill` | `adoptDiscoveredSkill` | `skill_adopt` | administrator-only |
| `POST /skills/{id}/deployments` | `setSkillTargets` | `sync` | plural `skillIds` selector |
| `POST /updates` | `checkUpdates` | `update_check` | |
| `POST /sync` | `syncNow` | `sync` | plural `nodeIds`/`skillIds` **are** read by worker |
| `POST /deployments/{id}/rollback` | `rollbackDeployment` | `rollback` | scoped node/skill selectors after store swap |
| `POST /mcp/deployments` | `deployMCPProfile` | `mcp_sync` | plural profile/deployment selectors |
| `POST /reconcile` | `reconcileNow` | `sync` + `mcp_sync` | queues both pipelines |
| `POST /mcp/servers/{id}/health` | `checkMCPHealth` | `mcp_health` | stub in worker |

**Exceptions:** `uploadSkill` → **201** sync import (not a job). `approveUpdate` → **202** but **only** `ApproveUpdate` (no enqueue).

State-then-enqueue is **not atomic** for set targets / rollback / MCP deploy. Document failure if store commit succeeds and enqueue fails.

## OpenAPI gaps

- Covers `/api/v1` only (`servers.url: /api/v1`).
- Omits `/healthz` and all `/agent/v1/*`.
- Schemas often allow `additionalProperties: true`; runtime `decodeJSON` rejects unknown fields.
- Thin schemas; no role annotations; no job-kind enum. OpenAPI is **not** a full route inventory.

## Legacy SQL (do not copy)

Direct `Pool().Exec` / ad-hoc SQL still in:

- `updateNode`, `archiveNode` (`resources.go`)
- `updateSettings` policy UPDATEs; `getSettings` ad-hoc via `JSONObject` (`settings.go`)

New persistence belongs in `internal/store` methods/transactions.

## Implement a change

1. Define/update contract in `api/openapi.yaml` and `docs/API.md` first.
2. Add the route in the correct Chi group in `api.go`.
3. Decode with `decodeJSON`; validate IDs, limits, origins, files, state transitions.
4. Reuse store methods, `EnqueueJob`, security helpers, existing error mapping — **no new handler SQL**.
5. Align job payload keys with `worker.go` consumers (`skillIds` not `skillId`, etc.).
6. Use 202 for queued work; preserve item/error envelopes.
7. Update browser client/page only after the server contract is clear.
8. Prefer structured store error mapping over raw `err.Error()` on new endpoints where possible.

## Other gotchas

- **202 ≠ agent done.** Job success is orchestration only.
- `searchMarket` may write raw provider JSON (not always `{items}`); 429/502 special codes.
- `recommend` always includes `automaticInstall: false` — never auto-install.
- `agentSecret` is intentional plaintext for authorized MCP env only.
- CSRF required on all unsafe `/api/v1`, including logout.
- Many mutators still map store errors to 400 + `err.Error()` (message leak risk).

## Prohibitions

- Parallel decoders/envelopes or second error shapes.
- Expanding `Pool().Exec` patterns.
- Putting agent routes under session CSRF.
- Assuming OpenAPI completeness or OpenAPI `additionalProperties` == runtime.
- Assuming job `succeeded` means deployment actual state synced.

## Verification

```bash
go test ./internal/httpapi/...
go test ./...
go vet ./...
# user-facing:
TOOLHUB_SMOKE_EMAIL=... TOOLHUB_SMOKE_PASSWORD=... sh scripts/smoke-api.sh
cd web && npm run typecheck && npm run build
```
