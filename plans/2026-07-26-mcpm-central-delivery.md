# MCP Central Delivery via mcpm and Skill Materialization

Date: 2026-07-26

Status: completed and verified on 2026-07-27; decisions locked via user interview on 2026-07-26

## Relationship to prior plans

This plan supersedes the ownership model of plans/2026-07-26-shared-source-runtime.md (deleted in 2f42c47, recoverable from Git history). That plan made the filesystem (`/root/.shared`) authoritative with PostgreSQL as a mirror. This plan inverts it:

- **ToolHub PostgreSQL is the single source of truth** for MCP servers and Skills.
- `~/.shared/*`, mcpm state, and per-agent native configs are demoted to scannable/importable objects, and the `~/.shared` legacy tree is retired at the end of the rollout.
- The shared-source layer (`internal/runtime/shared_*.go`) keeps its scan/ingest capability for discovery and import; its managed write path is removed (superseded by the mcpm delivery chain).

Terminology: "Agent" means the `toolhub-agent` node daemon. Claude/Codex/Hermes platforms are "runtimes" (`domain.RuntimeClaude` etc.).

## Locked decisions

1. **Source of truth**: ToolHub PostgreSQL. Everything else is scanned/imported, never authoritative.
2. **MCP delivery chain**: DB → worker → Agent → **mcpm relay**. The Agent ensures desired servers exist in `~/.config/mcpm/servers.json` (prefer mcpm CLI; structured JSON patch as fallback; file mode 0600) and each managed runtime's native config carries exactly one anchor entry pointing at its mcpm profile.
3. **Profile granularity**: one profile per managed runtime — `toolhub-claude` and `toolhub-codex`. Server membership is expressed via mcpm `profile_tags` and maps 1:1 from ToolHub `mcp_profiles`. This preserves per-platform filtered delivery.
4. **Native anchors**:
   - Claude → `~/.claude.json` top-level `mcpServers` (the location `claude mcp add --scope user` uses; `~/.claude/settings.json` `mcpServers` is not read by Claude Code and is a dead config).
   - Codex → `~/.codex/config.toml` `[mcp_servers.toolhub-codex]` section.
   - Both writes are surgical local edits preserving all unrelated content (env block, model_provider settings), with atomic backups.
5. **Managed scope**: Claude + Codex only. Hermes stays frozen as-is (including local-only `task-trellis` and `acemcp`), OpenClaw is observed-only, Grok free-rides by reading Claude's config and needs no writer.
6. **Skills**: classic artifact pipeline. Import every shared skill into the DB as an immutable artifact, deploy **materialized copies** into each runtime's skills directory via the existing target matrix / approval / rollback machinery, dismantle the symlink farm, retire `~/.shared/skills`. Git-sourced skills (vibe, firecrawl) use the existing update-check discovery.
7. **Import seeding**: seed both runtime profiles from the **live mcpm `all-mcp` set** (zero behavior change for Codex at cutover; Claude gains working MCP for the first time). Import the `~/.shared/mcp/servers.json` entries as disabled candidates for UI review. Name conflicts: the live variant keeps the name, the legacy variant gets a `-shared` suffix (`deepwiki-shared`, `grok-search-shared`).
8. **Legacy retirement**: archive then remove `~/.shared/mcp` and `~/.shared/skills`; remove the Codex `.tmp` shared-mcp plugin, the Claude `settings.json` `mcpServers` block, the invalid snake_case `mcp_servers` block in `settings.local.json` (preserving `permissions`), and finally the legacy `all-mcp` profile.
9. **Rollout**: five phases, each behind a verification gate (below).

## Machine baseline (verified 2026-07-26)

- `toolhub-agent` systemd unit active; `toolhub` + `postgres` containers running; agent home `/root`.
- **Claude Code currently has no effective MCP**: `~/.claude.json` `mcpServers` is empty; `~/.claude/settings.json` holds only `env` (ANTHROPIC_* values that must be preserved); `~/.claude/settings.local.json` contains an ineffective snake_case `mcp_servers.all-mcp` block plus a `permissions.allow` list that must survive cleanup.
- **Codex** loads MCP via `[mcp_servers.all-mcp] command=mcpm args=["profile","run","all-mcp"]` in `config.toml`, and additionally the ToolHub-authored plugin `~/.codex/.tmp/plugins/plugins/shared-mcp/.mcp.json` (regenerated 2026-07-26 15:49, author "ToolHub") still carries the 7 legacy shared servers → double-load risk until cleaned.
- **mcpm**: `/root/.local/bin/mcpm`; store `~/.config/mcpm/servers.json` (currently 0644 with plaintext env keys — tighten to 0600). Live `all-mcp` members: `sequential-thinking`, `memory`, `deepwiki` (npx), `context7`, `trellis`, `desktop-commander`, `grok-search` (uv, local checkout). mcpm supports remote servers (`--type remote --url`, headers) and multi-profile grouping, so HTTP/SSE servers can live in mcpm.
- **Legacy manifest** `~/.shared/mcp/servers.json` (0600): `tavily`, `arxiv-paper`, `deepwiki` (HTTP), `amap` (SSE), `cloudbase`, `gitnexus`, `grok-search` (uvx git, different credentials), `playwright` (disabled). Conflicts with live set on `deepwiki` and `grok-search`.
- **Skills**: `~/.claude/skills` and `~/.codex/skills` are symlink farms into `/root/.shared/skills`, which itself mixes real directories and links into `/root/.agents/skills` and `/root/.shared/vibe-skills`. Unmanaged real directories exist alongside (`codex-build`, `codex-review` in `.claude`; `acemcp-incremental-sync`, `dist` in `.codex`) and must not be touched. The `vibe` skill is known package-invalid and stays skipped.
- No system cron or systemd timer runs `update-shared.sh`; `generate-claude.sh` / `generate-codex.sh` are not executable. During the sustained cutover gate, an application-managed Hermes scheduler job named `restore-mcp-config` was discovered outside those system schedulers. It restored the legacy `all-mcp` entries every two minutes and had to be backed up and retired before ToolHub could remain authoritative.
- Prior rollout backups exist under `/var/lib/toolhub-agent/backups/shared-rollout-20260726-primary/`.

## Target architecture

```
PostgreSQL (desired state: mcp_servers, mcp_profiles, mcp_deployments, skills)
   │  worker jobs (mcp_sync / sync)
   ▼
toolhub-agent (signed typed tasks: apply_mcp extended, deploy_skill)
   │
   ├─ mcpm adapter: ensure servers + profile_tags in ~/.config/mcpm/servers.json
   │     prefer mcpm CLI; fallback structured JSON patch; chmod 0600
   ├─ Claude anchor: ~/.claude.json mcpServers.toolhub-claude = mcpm profile run toolhub-claude
   ├─ Codex anchor:  ~/.codex/config.toml [mcp_servers.toolhub-codex]
   └─ Skills: materialized artifact copies per runtime (existing deployer)
```

- Inventory (6h + on connect) additionally scans `~/.config/mcpm/servers.json` and both anchor files; local edits are drift and are restored on reconcile.
- Secrets: env values are encrypted in the DB via the existing one-time capture flow; plaintext is materialized only into the node-local mcpm store (0600). Never in API responses, jobs, audit metadata, or logs.
- Grok reads Claude's config automatically; Hermes `config.yaml` is not written.

## Phased rollout

Every phase takes atomic backups before writing and has an explicit gate; do not start the next phase until the gate passes.

### Phase 1 — scan, import, seed (zero writes)

- Extend inventory to ingest the mcpm store; import live servers (7) and legacy shared servers (7, `-shared` suffix on conflicts) into `mcp_servers`; capture env secrets via one-time capture.
- Create `toolhub-claude` / `toolhub-codex` profiles seeded with the live set; legacy imports remain disabled candidates.
- Import all shared skills as artifacts (Git provenance for vibe/firecrawl upstreams; `vibe` expected to stay package-invalid/skipped).
- Gate: UI review shows imported servers/profiles matching the live sets byte-for-byte on command/args/env names; no file writes occurred.

### Phase 2 — Codex cutover

- Write `[mcp_servers.toolhub-codex]` anchor; remove the `all-mcp` entry from `config.toml`; delete the ToolHub-authored `.tmp` shared-mcp plugin directory.
- Gate: a fresh Codex session lists tools from the `toolhub-codex` profile; no duplicate servers; `config.toml` model settings intact.

### Phase 3 — Claude cutover

- Write the `toolhub-claude` anchor into `~/.claude.json` `mcpServers`; remove the dead `mcpServers` block from `settings.json` (preserve `env`); remove the invalid `mcp_servers` block from `settings.local.json` (preserve `permissions`).
- Gate: `claude mcp list` (or a fresh session) shows the profile tools; settings files retain their unrelated content.

### Phase 4 — Skills materialization

- Approve and deploy materialized copies per the target matrix, replacing owned symlinks; unmanaged real directories untouched; dangling links owned by the old farm removed.
- Gate: both runtimes list the expected skills from real directories; `ls -la` shows no remaining managed symlinks into `/root/.shared/skills`.

### Phase 5 — legacy retirement

- Archive `~/.shared/mcp` and `~/.shared/skills` into the Agent backup tree, then remove them; remove the legacy `all-mcp` profile and stray tags from mcpm; keep shared-source scanning available for future nodes.
- Gate: both runtimes still healthy after archive+removal; backups verified restorable.

Rollback: phases 2–3 are reversed by restoring the anchor/config backups; phase 4 by the per-node rollback transition (a first successful deployment becomes disabled, while later versions swap previous↔desired); phase 5 by restoring the archive.

## Execution record (completed 2026-07-27)

All five phases completed on the single `project-host` node. The machine baseline originally recorded seven live mcpm members; `ACEMCP` was present in the cutover inventory, so the final ToolHub-authoritative set contains eight servers.

### Final state

- PostgreSQL contains 22 approved Skills and 44 Codex/Claude deployments; all 44 are enabled, generation-matched, and `in_sync`.
- `toolhub-codex` and `toolhub-claude` each contain the same eight servers: `ACEMCP`, `context7`, `deepwiki`, `desktop-commander`, `grok-search`, `memory`, `sequential-thinking`, and `trellis`. Both fixed deployments are generation 9 and `in_sync`; all 16 runtime bindings have central ownership.
- `~/.config/mcpm/servers.json` and both native anchors are mode `0600`. Codex has only `[mcp_servers.toolhub-codex]`; Claude has only `mcpServers.toolhub-claude`. The dead Claude settings blocks, the legacy Codex plugin, the `all-mcp` profile/tags, and both legacy shared trees are absent.
- Both runtime Skill roots contain 44 materialized managed directories and no symlink into `/root/.shared/skills`. The four pre-existing unmanaged directories named in the baseline remain present.
- The verified rollback archive is `/var/lib/toolhub-agent/backups/mcpm-central-delivery-20260726.xFgzsi/phase5-legacy-retirement`. Its checksums and both tar archives were re-read successfully. The removed Hermes job registry and script are preserved under its `hermes-restore-mcp-config/` subdirectory.

### Follow-up corrections made during rollout

- Central MCP deployment and mcpm import now clear stale `shared_source_id` ownership, and integration coverage proves a later shared-source scan cannot reclaim the binding.
- Skill backup moves fall back safely on `EXDEV`, preserving symlinks or managed directories when the Agent data directory and runtime home are on different filesystems.
- Rolling back a first successful Skill deployment now advances desired state to disabled; later rollbacks continue to swap previous and desired versions. Deployment list responses expose `previousVersionId`, and the API/runbook document both cases.
- The conflicting Hermes `restore-mcp-config` job was removed only after its definition and script were backed up. The Agent then detected Codex drift, reconciled it, and remained stable across multiple former scheduler intervals.

### Verification evidence

- A final `inventory_scan` node task succeeded after legacy cleanup; both MCP deployments and all 44 Skill deployments remained `in_sync`.
- Each of the eight servers passed an independent MCP `initialize` plus `tools/list` probe. Both aggregate profiles returned 73 tools in about 18 seconds.
- Claude Code 2.1.220 reports the relay as connected, but its `claude mcp list` health command has an approximately 10-second cold `tools/list` window and reports a timeout for this 18-second aggregate startup. Direct protocol probes prove the relay and tools are healthy; connection-only output is not treated as the gate.
- `go test ./...`, `go vet ./...`, focused race tests for `internal/runtime` and `internal/store`, PostgreSQL integration tests, web typecheck/build, Compose config validation, and Linux/macOS/Windows Agent cross-builds all passed.
- The final repository handoff is one `main` branch and one worktree; the temporary detached delivery worktree is removed after the task commit is integrated.

## Code changes

- `internal/runtime`: mcpm adapter (CLI invocation + structured JSON patch), surgical editors for `~/.claude.json` (JSON merge) and `~/.codex/config.toml` (TOML section replace), inventory MCP path additions; remove the shared managed-write path, keep scan/ingest.
- `internal/protocol` + `internal/agentclient`: extend the `apply_mcp` payload for the mcpm target within the closed typed task protocol (task kinds stay `scan_inventory`, `deploy_skill`, `apply_mcp`, `adopt_skill`; no arbitrary shell).
- `internal/store`: migration only if profile→runtime mapping needs new columns; verify `mcp_profiles`/`mcp_deployments` (profile+node) suffice first. Never rewrite applied migrations; next free number applies.
- `internal/worker`: `mcp_sync` producer/consumer selectors keep the existing plural field contract (`nodeIds`, `profileIds`, `deploymentIds`, `scopeType`, `scopeId`).
- `web/src`: import review UI (enable/disable candidates, conflict badges), per-runtime profile membership matrix; reuse `api` client, `useData`, shared UI primitives.
- `api/openapi.yaml` + `docs/API.md` updated with any new endpoints/fields.
- Tests: fixtures use redacted values only; cover both WSS and SSH task paths, idempotency, and preserve-unrelated-content editor behavior.

## Non-goals

- Managed delivery for Hermes/OpenClaw/Grok (observed-only this iteration).
- Multi-node rollout beyond this machine.
- Real `mcp_health` probes (stub stays).
- Any change to Hermes local `task-trellis` / `acemcp` entries.
