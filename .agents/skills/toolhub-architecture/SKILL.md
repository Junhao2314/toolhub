---
name: toolhub-architecture
description: Navigate ToolHub's Go control plane, React UI, PostgreSQL store, Agent, runtime, worker, and protocol boundaries. Use when tracing data flow, adding cross-layer features, refactoring modules, or deciding where new code belongs.
---

# ToolHub Architecture

## When to use

Cross-layer features, refactors, “where does this belong?”, tracing a request from UI/API to Agent and back, or reconciling Job / node_task / deployment confusion.

## Read first

Read `AGENTS.md`, `.builder/architecture.md`, `README.md`, `cmd/toolhub/main.go`, `cmd/toolhub-agent/main.go`, `internal/domain/models.go`, `internal/protocol/task.go`, `internal/worker/worker.go`, `internal/store/jobs.go`, `internal/store/nodes.go` (`CreateNodeTask`/`CompleteTask`), `internal/store/reconcile.go`, `internal/agenthub/hub.go`, `internal/agentclient/executor.go`, `internal/remote/ssh.go`, and `internal/httpapi/api.go`. Then read only the package being changed and its tests.

## System shape

1. `cmd/toolhub` loads `internal/config`, creates the encryption cipher, opens and migrates `internal/store`, bootstraps the admin and project-host node, then starts `internal/agenthub`, `internal/remote`, `internal/worker` (`Run(ctx, 4)`), `Scheduler`, `internal/market`, `internal/ai`, and `internal/httpapi`. The compiled web UI is embedded from `cmd/toolhub/dist`. Mux: `/healthz`, `/api/`, `/agent/` → API; everything else → SPA.
2. `cmd/toolhub-agent` handles service startup and the `enroll`, `run`, `scan`, and `run-task` commands via `internal/agentclient`, `internal/runtime`, and `internal/agentservice`.
3. Happy path: HTTP handler → auth/validation → store and/or `EnqueueJob` → worker → `CreateNodeTask` (HMAC via `protocol.TaskSigningBytes`) → `Hub.SendTask` or `remote.Dispatch` → agent `Executor.Execute` → WSS `task_result` / SSH complete → `Store.CompleteTask` projects deployment state.

## Closed kind lists (do not invent silently)

**Worker job kinds** (`Worker.execute`): `inventory_scan`, `skill_import`, `update_check`, `sync`, `rollback` (same path as sync), `mcp_sync`, `mcp_health` (stub), `archive_purge`.

**Agent task kinds** (`Executor.Execute`): `scan_inventory`, `deploy_skill`, `apply_mcp` only.

Job kinds ≠ task kinds. Map carefully through worker → `CreateNodeTask` → executor → `CompleteTask`.

## State machines (keep separate)

| Machine | Meaning of success |
|---------|-------------------|
| `jobs` | Control-plane orchestration claimed/finished (`FinishJob` after worker return) |
| `node_tasks` | Delivery/execution of one signed task |
| `deployments` / `mcp_deployments` | Desired vs actual reconciliation outcome |

Worker often marks a Job successful after task creation/delivery, before the Agent result arrives. Offline delivery yields `pendingOffline` while the job can still succeed. A node task that reaches `running` and then loses the connection is **not** auto-requeued; reconnect redelivers only `pending`/`delivered`. Do not use Job status as the deployment health signal.

## Selector contract (verified gotcha)

Before changing targets/sync/rollback/MCP deploy, diff HTTP enqueue payloads against worker JSON structs in `worker.go`:

| Producer | Payload keys | Worker reads | Effect today |
|----------|--------------|--------------|--------------|
| `POST /sync` | `nodeIds`, `skillIds` | yes | Scoped |
| Scheduler | `scopeType`, `scopeId` | yes | Scoped |
| `setSkillTargets` | `skillId` (singular) | only `skillIds` | **Ignored → all pending skill deploys** |
| `rollbackDeployment` | `deploymentId` | no | **Broader pending-set reconcile** |
| `mcp_sync` | `profileId` | no | **All pending MCP IDs** |

## Place changes by responsibility

- Wire/domain types: `internal/domain` or `internal/protocol`; keep task kinds closed and signed.
- PostgreSQL SQL, migrations, multi-write transactions: `internal/store` only.
- HTTP transport, role gates, request decoding, envelopes: `internal/httpapi`.
- Scheduling, queue claims, retries, reconciliation orchestration: `internal/worker`.
- Node filesystem inventory/deployment: `internal/runtime`, executed only through the Agent path.
- Package scanning, hashing, provenance, Git import: `internal/skills`.
- Browser routing/pages: `web/src`; API calls only via `web/src/api/client.ts`.

## Preserve these invariants

- Update discovery does not mutate desired state. Only approval advances desired state; sync reconciles approved state.
- Deployments keep desired, actual, and previous versions. Rollback is a new desired transition.
- Existing runtime directories are onboarding inputs, not overwrite targets. Managed activation requires `.toolhub-managed.json` and uses cache, staging, backup, and rename.
- Agent tasks are typed and HMAC-signed over canonical JSON; no arbitrary remote shell.
- Secrets stay encrypted and scoped. Redaction is opt-in per call site (audit/inventory/AI), not global middleware.
- WebSocket delivery, task status persistence, and SSH fallback are separate operations; design for duplicate delivery and stale status.
- Marketplace search/recommendations must never auto-install.

## Reuse

- HTTP: `decodeJSON`, `writeJSON`, `writeItems`, `serveList`, `writeError`, `handleStoreError`, `requireRoles`
- Store: `JSONList`, `JSONObject`, `EnqueueJob`, `ClaimJob`, `FinishJob`, `FailJob`, `CancelJob`, `CreateNodeTask`, `CompleteTask`, `AgentSecretValue`
- Security: `HashPassword`/`VerifyPassword`, `Cipher`, `RandomToken`/`TokenHash`, `SignPayload`/`VerifyPayload`, `RedactMap`, `NormalizeUsername`
- Execution: `runtime.Deployer`, `skills.ScanZIP`/`ImportGit`, `agentclient.Executor`, `remote.Executor`

## Workflow for a cross-layer change

1. Name the producer–consumer matrix: handler → job kind → `CreateNodeTask` kind → executor case → `CompleteTask` projection → WSS and SSH.
2. Verify selector/payload field names end-to-end against `worker.go`.
3. Place code only in the packages above; no handler SQL; no parallel queues/deployers/clients.
4. Add focused tests on the package boundary you changed; run higher gates when the path crosses Docker/browser.

## Commands

```bash
go test ./internal/worker/... ./internal/store/... ./internal/protocol/... ./internal/agentclient/...
go test ./...
go vet ./...
cd web && npm run typecheck && npm run build   # if UI/embed path touched
```

## Prohibitions

- New job/task kinds without full chain + both transports + idempotency.
- Arbitrary remote shell or unpinned SSH.
- Handler SQL or rewriting applied migrations.
- Treating `job.succeeded` as actual deployment health.
- Auto-install from marketplace/AI recommendations.
- Hand-editing `cmd/toolhub/dist`.

## Verification

Trace forward and failure paths. Confirm job status ≠ deployment state. Exercise offline delivery and SSH fallback separately when delivery changes. Run focused Go tests then `go test ./...` / `go vet ./...`, plus web typecheck/build when the embedded UI changes.
