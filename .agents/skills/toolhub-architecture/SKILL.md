---
name: toolhub-architecture
description: Trace and refactor ToolHub generation-2 flows across the Go control plane, PostgreSQL operations and desired snapshots, HMAC Unix Bridge, local runtime adapters, Salt 3008.x driver, shared mcpm relay, and React UI. Use for cross-layer changes, ownership decisions, reconcile behavior, target state, or operation lifecycle work.
---

# ToolHub Architecture

Read `AGENTS.md`, `.builder/architecture.md`, `README.md`,
`cmd/toolhub/main.go`, `cmd/toolhub-bridge/main.go`,
`internal/domain/models.go`, `internal/bridgeprotocol/types.go`,
`internal/bridge/adapter.go`, `internal/worker/worker.go`, and
`internal/httpapi/api.go`. Then read the changed package and adjacent tests.

Treat this process graph as the primary boundary:

```text
Browser -> HTTP/session/CSRF -> PostgreSQL operation
        -> worker -> typed HMAC Bridge request
        -> local adapter OR fixed Salt CLI/JID
        -> backup/stage/atomic terminal result
        -> snapshot + target health + operation projection
```

There is no Agent, WSS/enrollment, SSH fallback, RBAC, deployment table,
legacy job queue, approval state, or Profile activation. Remove references to
those paths instead of rebuilding them.

Preserve these invariants:

- Profile membership references Skill/MCP IDs; preflight pins exact current
  versions/revisions/secrets into a five-minute one-use confirmation.
- Apply/edit/Restore create immutable desired snapshots.
- Apply mirrors manageable scope; reconcile repairs pinned members and keeps
  later unmanaged additions.
- Every write creates a backup first; no-op reconcile creates none.
- One target has at most one queued/running operation; one pending reconcile
  rerun may be coalesced.
- Running destructive target work is not force-cancelled.
- Local MCP is `local/shared-relay`; local Skills remain runtime-specific.
- Hermes is read-only; remote writes require accepted Salt 3008.x and a
  canonical managed home.
- Secrets are UUID references in snapshots and ephemeral plaintext only at the
  authorized worker-to-Bridge/Salt boundary.
- BoltDB contains hashes/routing/JIDs only, never archives, secret values,
  editable contents, or raw output.

Place behavior by owner:

- contracts and protected rules: `internal/bridgeprotocol`;
- PostgreSQL state/transactions: `internal/store`;
- HTTP transport: `internal/httpapi`;
- scheduling/claims/projection: `internal/worker`;
- Bridge auth/journal/adapter routing: `internal/bridge`;
- local filesystem and relay: `internal/runtime`;
- remote fixed CLI/staging/JID: `internal/saltdriver`;
- browser workflow: `web/src`.

For every change, trace success, partial fleet failure, revision conflict,
offline/unavailable, restart recovery, cancellation timing, backup failure, and
secret redaction. Do not add a parallel queue, deployer, runner, or HTTP client.

Verify with focused package tests, then:

```bash
GOCACHE=/tmp/toolhub-gocache go test ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
cd web && npm run typecheck && npm run build
```
