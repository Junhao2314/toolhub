---
name: toolhub-agent-integrations
description: Extend or troubleshoot ToolHub Agent enrollment, WSS tasks, SSH fallback, runtime inventory/deployment, Skill intake, SkillsMP, MCP reconciliation, workers, or platform service packaging. Use when a change crosses node or external-service boundaries.
---

# ToolHub Agent and Integrations

## When to use

Agent enrollment/WSS/SSH, runtime deploy/inventory, Skill package safety, SkillsMP market, MCP reconcile, worker job kinds, packaging services, or any change that crosses node/external boundaries.

## Read first

Read `AGENTS.md`, `README.md`, `docs/SECURITY.md`, `docs/ROLLOUT.md`, `internal/protocol/task.go`, `internal/agenthub`, `internal/agentclient`, `internal/runtime`, `internal/remote`, `internal/skills`, `internal/market`, `internal/worker`, `cmd/toolhub-agent/main.go`, and the relevant packaging file.

## Agent CLI and transport

| Command | Role |
|---------|------|
| `enroll` | HTTPS server + one-time token; writes agent config (loopback HTTP allowed) |
| `run` | WSS runner with reconnect backoff |
| `scan` | read-only `runtime.ScanAll` → JSON |
| `run-task` | signed task from `--stdin` or `--file` under `$TMPDIR/toolhub-task-*` only |

**Hub inbound (agent → hub):** `heartbeat`, `inventory`, `task_result` only.
**Hub outbound:** `task`, `error`; server ping 30s; 90s read deadline.
Auth: `X-ToolHub-Node-ID` + `Bearer` agent token. On connect: mark online, deliver pending (`pending`/`delivered`).

**Signatures:** `id + "\n" + kind + "\n" + canonicalJSON(payload)` then HMAC-SHA256 hex (`protocol.TaskSigningBytes` + `security.SignPayload`/`VerifyPayload`). Per-node 32-byte task key from enroll. Local agent history (`task-history.json`, 30-day prune) returns prior result for repeated task IDs.

**Closed agent task kinds** (`Executor.Execute`): `scan_inventory`, `deploy_skill`, `apply_mcp`, `adopt_skill` only.

## SSH fallback

`internal/remote/ssh.go`: pinned known_hosts, `BatchMode`, `IdentitiesOnly`, `StrictHostKeyChecking=yes`, SFTP put, fixed `toolhub-agent run-task --file …`, 90s timeout, 2MiB output cap. **Not a remote shell.**

## Worker job kinds and selectors

| Job kind | Behavior |
|----------|----------|
| `inventory_scan` | Create `scan_inventory` task; finish after deliver attempt |
| `skill_import` | Git import via `skills.ImportGit` + store import |
| `skill_adopt` | Signed `adopt_skill` task; Agent safely packages/uploads a discovered Skill and writes the marker only after import |
| `update_check` | Discover candidates only (no desired mutation); respects `skillIds` / scope |
| `sync` / `rollback` | `syncSkills` over pending skill deployments |
| `mcp_sync` | All pending MCP deployment IDs → `apply_mcp` |
| `mcp_health` | **Stub** returns `queued_on_next_reconcile` |
| `archive_purge` | `PurgeExpiredArchives` (30-day skill hard delete) |

Default seeded schedules: update `0 2 * * *`, sync `30 3 * * *`, timezone `Asia/Shanghai`. Scheduler reloads every 5 minutes from store policies; enqueues with `scopeType`/`scopeId`.

### Selector consumption (verified)

| Producer payload | Worker consumption | Result |
|------------------|--------------------|--------|
| `POST /sync` `nodeIds`/`skillIds` | filters both | **Used** |
| Scheduled `scopeType`/`scopeId` | skill→skillIds; source/node_group filters | **Used** |
| `setSkillTargets` `{"skillIds": [id]}` | `skillIds` | Scoped Skill sync |
| `rollback` plural node/skill/deployment IDs | node/skill filters | Scoped rollback reconcile |
| `mcp_sync` plural node/profile/deployment IDs | all three | Scoped MCP reconcile |
| `mcp_health` `serverId` | stub only | **Ignored** |

Evidence: `internal/httpapi/skills.go` (enqueue), `internal/httpapi/mcp.go`, `internal/worker/worker.go` `syncSkills`/`syncMCP`. When changing selectors, align field names end-to-end or document the broad reconcile.

## Runtime and Skill safety

- Inventory: Codex, Claude, Hermes. Onboarding is read-only until managed marker exists.
- `runtime.Deployer`: known runtime root; slug rejects empty, `/` `\`, `.system` (does **not** reject `.` / `..` alone — validate new slug boundaries); re-scan ZIP + SHA match; content-addressed cache under data dir; refuse unmanaged dirs (no `.toolhub-managed.json`); marker + backup + atomic rename; disable only managed.
- `internal/skills`: limits (20MiB archive, 50MiB uncompressed, 10MiB/file, 2000 files); no symlinks; safe paths; exactly one `SKILL.md` + YAML frontmatter; risk signals. Git: https/ssh only, no embedded creds, no private/loopback hosts; fixed ref fetch.
- Market `Client.Search`: query 2–200 chars; page/limit; 10min cache; `ErrRateLimited`; **search only** — no install path. Recommendations must not auto-install.

## State machine gotchas

1. **Job succeeded ≠ agent done.** FinishJob after create/deliver; result includes `delivered` / `pendingOffline`. Actual state moves in `CompleteTask` on success for deploy/apply kinds.
2. **WSS not atomic:** `SendTask` WriteJSON then `MarkTaskDelivered`. SSH: upload → run → MarkDelivered → CompleteTask sequential failure windows.
3. **Stuck `running` tasks:** agent may report `running`; reconnect only reloads `pending`/`delivered`. No auto-requeue for abandoned `running`.
4. **Rollback breadth:** store swaps one deployment; worker may enqueue for all pending/drift/failed/rolling_back rows.
5. **Idempotency is local agent history**, not control-plane dedupe after history loss.
6. MCP env values are secret refs; Agent fetches `/agent/v1/secrets/{id}` only during authorized `apply_mcp`.

## Packaging

| Platform | Path |
|----------|------|
| Linux systemd | `packaging/systemd/toolhub-agent.service` |
| macOS launchd | `packaging/launchd/com.toolhub.agent.plist` |
| Windows | `packaging/windows/install-service.ps1` |

CI builds the Agent on Linux, macOS, and Windows. Rollout order (`docs/ROLLOUT.md`): inventory canary → approve → sync → then enable scheduled sync.

## Extend an Agent capability (checklist)

1. Add/keep closed task kind in `Executor.Execute`.
2. Control-plane producer: store method + `CreateNodeTask` with signing.
3. Worker case maps job kind → task kind + payload.
4. `CompleteTask` projection if desired/actual state changes.
5. Both WSS hub delivery and SSH `run-task` path.
6. Idempotency/error tests; offline + reconnect behavior.

## Reuse

Prefer `protocol.TaskSigningBytes`, `security.SignPayload`/`VerifyPayload`, `store.CreateNodeTask`/`CompleteTask`/`EnqueueJob`, `agenthub.Hub`, `remote.Executor`, `runtime.Deployer`/`ScanAll`/`ApplyMCP`, `skills.ScanZIP`/`ImportGit`, `market.Client`, and existing packaging unit files. Do not add parallel deployers, shell runners, or market install paths.

## Prohibitions

- Arbitrary shell execution or unpinned SSH.
- Direct runtime writes from onboarding.
- Provider credentials in URLs/logs.
- Automatic marketplace installation.
- New task kind without signature/authorization/idempotency and dual-transport handling.
- Assuming ignored selector fields still scope work.

## Verification

```bash
go test ./internal/protocol/... ./internal/runtime/... ./internal/skills/... ./internal/market/... ./internal/worker/...
go test ./...
go vet ./...
go build -o bin/toolhub-agent ./cmd/toolhub-agent
# integrated:
docker compose up -d --build --wait
sh scripts/smoke-api.sh
```

When UI enrollment/SSH paths change, also run Playwright. Trace producer → worker → executor → CompleteTask on both transports before claiming done.
