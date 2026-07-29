# Rollout Runbook

1. Start ToolHub with a new PostgreSQL volume, a generated 32-byte master key, and a unique bootstrap password.
2. Bind Docker to loopback and configure Tailscale Serve plus Tailnet ACLs. Verify `/healthz` only through intended paths.
3. In Nodes, claim the default `project-host` entry and run its generated Agent command on the machine containing this project. For the packaged Linux service, enroll with `--config /etc/toolhub-agent/agent.json`, an explicit runtime `--home`, and `--data-dir /var/lib/toolhub-agent`; keep that home aligned with the service `User=` and writable runtime-path exceptions. Connection immediately runs inventory; confirm Codex, Claude, Hermes, symlink, protected-Skill, and MCP findings. Codex/Claude MCP can be automatically captured and baselined without rewriting existing configuration; Hermes remains candidate-only.
4. From Skills → Discovered, adopt one low-risk non-Hermes local Skill. Confirm the snapshot enters pending review and the Agent writes the managed marker only after import succeeds. For Hermes, use Import and confirm no marker or source file changes. Alternatively import a Git/ZIP Skill into Library.
5. Approve the Skill. The target matrix lists `project-host` first and preselects only eligible runtimes found in its inventory; Hermes is absent. Confirm that single-node canary and run sync manually.
6. Confirm the Agent created the content-addressed cache, management marker, and deployment backup. Exercise rollback and confirm actual state returns to the prior version.
7. Enable scheduled update checks. Approve one candidate and verify that discovery alone does not mutate desired state.
8. Enable the default `03:30 Asia/Shanghai` reconciliation only after the canary and restore path are verified. It enqueues both Skill and MCP sync; inventory refreshes every six hours independently.
9. Back up PostgreSQL and retain Agent backup directories through the operational rollback window.

Rollback is always a new desired-state transition to the recorded previous approved version. Do not edit ToolHub-managed runtime directories while reconciliation is enabled.

## Hermes read-only migration (Agent first)

Migration `011_hermes_read_only_imports.sql` is intentionally one-way and must be rolled out Agent-first:

1. Deploy and restart the Agent version that advertises `hermes_read_only_import_v1` and rejects Hermes `deploy_skill`, `adopt_skill`, and `apply_mcp`. This Agent remains compatible with the old control plane.
2. Confirm no Hermes writer task remains running before upgrading the control plane. Check `node_tasks` in `pending`, `delivered`, or `running` for a Hermes runtime/discovery; let the guarded Agent fail or complete delivery, then verify the active count is zero.
3. Back up PostgreSQL. Upgrade the control plane and allow migration 011 to run. It preserves central Skills, MCP servers, encrypted secrets, provenance, and audit history; makes historical Hermes deployments/activations inert; and cancels active Hermes writer/adoption work without deleting node files or old markers.
4. Wait for fresh Agent inventory so the control plane records the capability and current generations. Verify Hermes Runtime View is read-only with zero drift, then test one explicit Skill import and one MCP import with key-name confirmation.
5. Confirm imported Skills enter review with no targets, imported MCP servers are enabled but belong to no Profile, and a later Hermes source edit sets `sourceChanged` without queuing sync or touching the node.

There is no down migration. Application rollback must retain the Agent writer guard and the migrated database. Restoring old Hermes writer semantics requires restoring the pre-upgrade PostgreSQL backup; do not try to reinterpret `legacy_read_only` rows in place.

## Central MCP and shared-Skill migration

PostgreSQL is authoritative. Shared sources and native runtime configuration are discovery/import inputs only. The Agent writer supports materialized Skills plus fixed mcpm profiles for Codex and Claude; Hermes/OpenClaw stay outside MCP delivery and Grok continues to inherit Claude.

Before starting, confirm the systemd unit can write the enrolled home paths. Atomic replacement of top-level files such as `~/.claude.json` requires write access to the home directory itself; `ProtectHome=read-only` cannot be reopened by a nested `ReadWritePaths=` exception. Keep `ProtectSystem=strict`, set `ProtectHome=false`, and allow-list the enrolled home plus the Agent data directory. Record hashes, modes, owners, symlink targets, and secure backups for the mcpm registry, Claude/Codex configs, the legacy shared trees, and any ToolHub-authored plugin.

Also enumerate every out-of-band writer that can edit the native MCP files, including application-managed schedulers that do not appear in system cron or systemd. Back up and retire legacy restore jobs before the cutover; do not use an immutable file flag because the Agent must remain able to reconcile approved desired state.

### Phase 1 — scan, import, and seed (no runtime writes)

1. Complete the Agent-first Hermes gate above, then deploy the control plane and wait for a fresh inventory.
2. Confirm live mcpm members appear as `mcpm-import` servers and each fixed profile follows the recognized native legacy anchor for its runtime. If the registry has no native anchor evidence, confirm the documented both-runtime compatibility fallback. Deployments remain `observed`.
3. Confirm legacy shared-manifest entries appear as disabled `shared-import` candidates. The live definition keeps a conflicting name; the candidate receives `-shared`.
4. Adopt importable shared Skills into immutable pending-review artifacts. A package-invalid Skill stays blocked and does not prevent healthy siblings from importing.

Gate: compare command/args/URL/key names and profile membership with the pre-rollout files. No runtime file timestamp or hash may have changed.

### Phase 2 — Codex cutover

1. Explicitly deploy the `toolhub-codex` profile to the canary node. Wait for the `apply_mcp` node task and deployment state, not merely the parent Job.
2. Confirm `~/.config/mcpm/servers.json` is mode `0600`, contains the desired `toolhub-codex` tags, and preserves unrelated definitions/tags.
3. Confirm `~/.codex/config.toml` contains one `[mcp_servers.toolhub-codex]` relay with `startup_timeout_sec = 60`, no recognized legacy `all-mcp` relay/subsections, and unchanged model/provider settings.
4. Archive the old ToolHub-authored `shared-mcp` plugin only after its `plugin.json` identity is validated.

On memory-constrained nodes, begin with only the required profile members and add servers after measuring steady-state RSS. Each stdio member starts once per active runtime client; assigning the same member to Codex and Claude does not share one subprocess.

Gate: a fresh Codex session exposes the expected tools once, with no duplicate servers. Keep the Agent mcpm and anchor backups until the full migration is accepted.

### Phase 3 — Claude cutover

1. Explicitly deploy the `toolhub-claude` profile and wait for actual deployment state.
2. Confirm top-level `~/.claude.json` has exactly one `mcpServers.toolhub-claude` relay.
3. Remove only the dead `mcpServers` block from `~/.claude/settings.json` and the invalid `mcp_servers` block from `settings.local.json`; preserve `env`, `permissions`, and every unrelated field.

Gate: `claude mcp list` or a fresh Claude session exposes the expected tools, and unrelated settings remain byte/semantic equivalent. If the CLI health command connects but its short discovery window expires during a cold aggregate startup, verify the exact relay with a direct MCP `initialize` plus `tools/list` probe using a recorded timeout; connection alone is not a passing gate.

### Phase 4 — materialize Skills

1. Review and approve imported shared Skill artifacts.
2. Assign explicit runtime targets; the retired `shared` deployment target must not be used.
3. Deploy one low-risk canary first, then the remaining approved matrix. Managed symlink-farm entries may be replaced only through the normal conflict-safe deployer; unmanaged real directories remain untouched.

Gate: expected Skills are real managed directories in each runtime, no managed link still points into the legacy shared tree, and rollback succeeds for the canary.

### Phase 5 — retire legacy sources

1. Archive `~/.shared/mcp` and `~/.shared/skills` into the Agent backup tree without following symlinks; verify the archive is readable and restorable.
2. Remove the archived legacy trees and dangling links owned by the old farm. Do not remove unrelated runtime directories.
3. Remove the legacy `all-mcp` profile/tag only after both fixed profiles are healthy.
4. Run a fresh inventory and full Skill/MCP reconciliation.

Gate: Codex and Claude remain healthy after legacy removal, materialized Skills remain present, mcpm has no stray `all-mcp` membership, and the archive can be restored.

### Rollback

- Phases 2–3: restore the timestamped mcpm and native-anchor backups, then verify the legacy relay before retrying.
- Phase 4: use the normal per-deployment rollback transition; a first deployment rolls back to disabled, while later versions swap previous and desired. Do not hand-edit managed directories.
- Phase 5: restore the verified archive and recorded symlink layout. Never bulk-delete or recreate an entire runtime Skills root.
- Migration 011: keep the new Agent guard during application rollback. Restore the pre-upgrade PostgreSQL backup only if old Hermes writer semantics are explicitly required.
- Stop at the first failed gate. A succeeded Job is not proof of a successful Agent task or in-sync deployment.
