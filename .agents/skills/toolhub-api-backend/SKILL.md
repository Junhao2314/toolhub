---
name: toolhub-api-backend
description: Add, modify, or troubleshoot ToolHub generation-2 Browser and Bridge HTTP APIs, Chi handlers, OpenAPI contracts, strict validation, response envelopes, idempotent operations, session/CSRF boundaries, and UI wiring. Use for REST behavior, route/schema changes, or control-plane actions.
---

# ToolHub API And Backend

Read `AGENTS.md`, `api/openapi.yaml`, `docs/API.md`,
`internal/httpapi/api.go`, `internal/httpapi/middleware.go`, the handler/store
methods, and `web/src/api/client.ts`. For Bridge changes also read
`api/bridge-openapi.yaml`, `internal/bridge/server.go`,
`internal/bridgeprotocol`, and `internal/bridgeclient`.

Browser routes use `/api/v1`. Public routes are username/password login and the
session probe. Every other route requires a server-side session; every unsafe
authenticated method requires `X-CSRF-Token`. There are no role gates or Agent
routes.

Reuse `decodeJSON`, `writeJSON`, `writeItems`, `writeError`,
`handleStoreError`, and `requestIdempotencyKey`. Keep errors in
`{error:{code,message,requestId}}`. JSON input is bounded, rejects unknown
fields, and accepts one object.

Create asynchronous work through store operation transactions. Supported
control work includes imports, update checks, and backup GC. Target work
includes Apply/edit/Restore, scan, reconcile, and fixed relay actions. Return
`202` with the durable operation. Preserve per-target requests for failed-only
retry.

Never let the browser choose a Bridge path/body. Map each handler to one typed
client method. The Bridge derives commit kind from `/targets/apply|edit|restore`
and accepts only fixed relay actions. Every Bridge mutation requires a caller
idempotency key and HMAC authentication.

When changing a route:

1. Update the relevant OpenAPI file and `docs/API.md` or `docs/BRIDGE.md`.
2. Add the route in the correct Chi boundary.
3. Validate IDs, counts, revisions, target/runtime compatibility, and body size.
4. Use store methods/transactions; never add handler SQL.
5. Align operation metadata with `internal/worker/worker.go` consumers.
6. Confirm secrets are key-only/write-only at the Browser boundary.
7. Update the existing web client/page and focused tests.

Do not add generic Bridge proxying, arbitrary execution, a second response
shape, bare browser `fetch`, or plaintext secret reads.

Verify:

```bash
GOCACHE=/tmp/toolhub-gocache go test ./internal/httpapi ./internal/store ./internal/worker ./internal/bridge ./internal/bridgeclient ./internal/bridgeprotocol
GOCACHE=/tmp/toolhub-gocache go test ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
cd web && npm run typecheck && npm run build
```
