# Runtime Discovery, Automatic MCP Takeover, Skill Adoption, and Public Release

Date: 2026-07-26

## Goal and invariants

- MCP discovery is automatic: Agents report normalized descriptors, ToolHub securely captures only requested secret values, and the observed configuration becomes the initial desired/actual baseline.
- After takeover, ToolHub is the source of truth. Local edits, secret changes, and deletion are drift; manual or scheduled reconciliation restores desired state.
- Skill discovery remains read-only. An administrator must explicitly adopt a discovered Skill, review its immutable snapshot, approve it, and target it before deployment.
- Inventory runs on connect and every six hours. Skill update discovery remains at 02:00; Skill and MCP reconciliation run daily at 03:30 and remain manually triggerable.
- Secret plaintext is never persisted in ordinary JSON, audit metadata, jobs, logs, or browser responses. Persisted secret values use the existing Cipher with record-ID AAD.
- Agent work remains a closed, signed task protocol. No arbitrary shell execution is introduced.
- Existing migrations are immutable; schema changes are additive in `003_runtime_discoveries.sql`.
- Generated `cmd/toolhub/dist` changes are not staged or committed.

## Implementation checklist

### Persistence and discovery

- [ ] Add Skill discovery and MCP runtime binding tables with last-seen, missing, drift, fingerprints, and adoption/binding references.
- [ ] Add MCP `runtime-auto` provenance/origin fields and capture-token persistence suitable for expiry and one-time consumption.
- [ ] Add store transactions for inventory upsert, missing marking, MCP matching/reuse, baseline creation, drift projection, encrypted secret capture, and discovery list/detail operations.
- [ ] Match MCP servers by normalized non-secret configuration, environment key set, and per-node task-key HMAC secret fingerprint; split same-name differing configurations with a node/runtime suffix.

### Agent protocol and runtime

- [ ] Extend inventory with normalized Codex/Claude/Hermes MCP descriptors without secret values.
- [ ] Calculate stable HMAC-SHA256 fingerprints over canonical secret maps using the node task key.
- [ ] Add authenticated descriptor and one-time capture endpoints; bind tokens to node/runtime/identity and reject expiry, replay, cross-node use, and identity mismatch.
- [ ] Add signed `adopt_skill` execution, safe local directory packaging, authenticated upload, backend ZIP rescan/hash validation, and marker creation only after successful import.
- [ ] Reject protected, `.system`, symlinked, escaped, or hash-mismatched Skill adoption.
- [ ] Change the Agent periodic inventory interval from ten minutes to a testable six-hour default while preserving connect and manual scans.

### Reconciliation and schedules

- [ ] Add scoped manual reconcile for both Skills and MCP.
- [ ] Make the 03:30 schedule enqueue both `sync` and `mcp_sync`; keep 02:00 update discovery approval-free.
- [ ] Align producer/consumer selector fields on plural IDs for Skill targeting, rollback, MCP deployment, and manual reconcile.
- [ ] Emit redacted system audit events for takeover, capture, drift, adoption, and reconciliation.

### API, UI, and documentation

- [ ] Add `GET /api/v1/discoveries`, `POST /api/v1/discoveries/{id}/adopt-skill`, and `POST /api/v1/reconcile` with correct RBAC/CSRF behavior.
- [ ] Document browser APIs in OpenAPI/API docs and Agent-only descriptor/capture/upload contracts in protocol documentation.
- [ ] Show last scan, auto-managed MCP count, pending Skill adoption, drift, and missing status on Nodes.
- [ ] Add a Discovered Skills view and manual Adopt action.
- [ ] Show MCP auto-managed provenance, node/runtime bindings, and drift without MCP adopt/approve controls.
- [ ] Show the six-hour inventory, 02:00 update, and 03:30 reconciliation schedules plus global Reconcile now in Settings.

### Publication

- [ ] Add an MIT License for Junhao2314 and update README for public clone, Compose, Agent enrollment, automatic MCP takeover, and security behavior.
- [ ] Audit tracked files and full history for secrets, private keys, `.env`, large files, and local runtime data.
- [ ] Create separate reviewable feature and public-metadata commits.
- [ ] Create/push public `github.com/Junhao2314/toolhub`, verify `PUBLIC`, default branch `main`, and confirm GitHub Actions succeeds.

## Validation strategy

- Focused unit tests for normalization, HMAC stability/isolation, capture token security, encrypted persistence boundaries, matching/baseline/drift behavior, Skill adoption safety/marker timing, six-hour cadence, dual scheduling, and selectors.
- Full Go tests, race tests for concurrency-sensitive packages, `go vet`, web typecheck/build, Docker config, Compose health, API smoke, and Playwright.
- Migration verification on both clean install and an upgrade database containing only migrations 001/002.
- Secret leakage inspection across database JSON, jobs, audit events, HTTP responses, and logs.
- Final `git diff --check` plus explicit generated-file exclusion.

## Rollback notes

- Migration 003 has no down migration; rollback application binaries only after confirming older code safely ignores the additive tables/columns.
- Automatic MCP baseline creation must be transactionally idempotent so retries do not duplicate servers, profiles, deployments, secrets, or bindings.
- If capture fails, leave the unknown discovery uncaptured and unmanaged; never persist partial plaintext or bind an incomplete server.
- If Skill adoption fails before import/hash validation, leave the runtime directory unmodified and unmanaged.
- Scheduled reconciliation can be disabled through existing policy controls if rollout exposes unexpected drift behavior; manual reconciliation remains scoped.
