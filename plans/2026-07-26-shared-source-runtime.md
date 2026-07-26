# Shared Source Runtime, Multi-Agent Sync, and Native Deployment

Date: 2026-07-26

Status: implementation complete; managed rollout pending operator approval

## Execution record

- Repository phases 1-5 and the Phase 6 watcher/locking implementation were completed on 2026-07-26. Phase 7 native control-plane migration was intentionally not performed.
- Default shared-source topology is optimized for the management platform's primary consumers: Claude and Codex are configured by default; Hermes, Grok, and OpenClaw remain supported as explicit optional consumers.
- Focused and full Go tests, race tests, vet, Web typecheck/build/audit, OpenAPI YAML parsing, and Compose configuration validation passed.
- Migration 004 was validated against both an empty isolated PostgreSQL 16 database and an isolated database preloaded with migrations 001/002/003; store integration tests passed on both paths.
- Live-server validation used an observed-only temporary Agent config/data directory and `sync-shared --dry-run`. It detected 7 enabled and 2 disabled shared MCP servers, preserved Hermes `task-trellis` and `acemcp`, and made no live configuration or permission changes.
- Managed rollout remains blocked until an operator reviews the locally owned Hermes `grok-search` conflict, decides how to handle the package-invalid `vibe` source entry, takes rollout backups, and explicitly changes the shared manifest from mode `0644` to `0600`.
- The live Agent config was not edited, managed mode/auto-sync were not enabled, and the Docker control plane/database were not migrated or switched.

This plan supersedes the ownership assumption in plans/2026-07-26-runtime-discovery-auto-mcp-github.md for nodes that opt into shared-source mode. The existing ToolHub-managed behavior remains available for other nodes. In shared-source mode, the filesystem source is authoritative and PostgreSQL is an indexed, redacted control-plane mirror.

## Goal

Make /root/.shared the single source of truth on this server:

- Skills are discovered once from /root/.shared/skills and linked into Codex and Claude by default; Hermes, Grok, and OpenClaw can be added without changing the source model.
- MCP desired state is read from /root/.shared/mcp/servers.json and rendered into each configured consumer's native format.
- Both manual sync and low-resource automatic sync use the same Go implementation.
- Existing Agent-local Skills and MCP entries remain visible but are not overwritten.
- Hermes local-only task-trellis and acemcp entries are preserved.
- The implementation adapts to the enrolled Agent home and explicit local paths instead of assuming a Docker home or generic defaults.
- Docker remains a supported deployment, but this server can later move the control plane to a native systemd service using the existing host PostgreSQL.

## Current server baseline

The implementation must be tested against this exact topology before enabling writes:

- Agent home: /root
- Canonical Skills: /root/.shared/skills
- Primary shared-source Skill consumers:
  - Codex: /root/.codex/skills
  - Claude: /root/.claude/skills
- Optional shared-source Skill consumers:
  - Hermes: /root/.hermes/skills
  - Grok: /root/.grok/skills
  - OpenClaw: /root/.openclaw/workspace/skills
- Canonical MCP manifest: /root/.shared/mcp/servers.json
- Primary shared-source MCP consumers:
  - Codex plugin JSON: /root/.codex/.tmp/plugins/plugins/shared-mcp/.mcp.json
  - Claude JSON: /root/.claude/settings.json, mcpServers mapping
- Optional shared-source MCP consumers:
  - Hermes YAML: /root/.hermes/config.yaml, mcp_servers mapping
  - OpenClaw JSON: /root/.openclaw/workspace/config/mcporter.json
  - Grok Build: inherits Claude MCP configuration and has no separate output file
- Allowed shared Skill link targets currently include:
  - /root/.shared/skills
  - /root/.agents/skills
  - /root/.shared/vibe-skills
- Existing generators are inconsistent and are not a safe orchestration boundary:
  - generate-claude.sh and generate-codex.sh are not executable.
  - sync-all.sh skips non-executable generators.
  - pipelines can mask failures without pipefail.
  - the Hermes generator can replace local-only entries.
  - no active cron or timer currently runs update-shared.sh.

Before implementation rollout, re-read the live files and produce redacted fixtures. Never copy plaintext secrets into tests, plans, logs, Git, API responses, jobs, or audit metadata.

## Scope and invariants

### Ownership

- shared-file is a node-scoped authority mode, not a global ToolHub switch.
- In shared-file mode, servers.json is MCP desired state. A scan updates the database mirror; database reconciliation must never restore an older DB copy over a newer valid manifest.
- /root/.shared/skills is the canonical Skill namespace. Consumer directories are links and local exceptions, not independent copies.
- Existing ToolHub-managed nodes continue to use approved DB desired state and the current deploy/apply paths.
- Paths and allowed symlink targets are configured locally in the Agent configuration. Browser APIs cannot supply arbitrary filesystem paths.

### Safety

- No arbitrary shared shell script is executed through an Agent task.
- Agent tasks remain closed, typed, canonical-JSON signed, and idempotent.
- Existing real directories, unknown symlinks, and unknown MCP entries are never overwritten.
- A stale link or managed MCP entry is removed only when the Agent previously created it and its current value still matches the expected value.
- Writes use compare-and-swap fingerprints, same-directory temporary files, fsync where supported, atomic rename, and a last-known-good backup.
- Skill names reject empty names, path separators, dot/dot-dot, .system, cycles, and destinations outside configured roots.
- Top-level source symlinks are followed only when their resolved targets are within explicit allowed roots.
- Job success remains orchestration success only. Consumer actual state changes only from Agent results or a subsequent inventory report.

### Secrets

- In shared-file mode, env and HTTP header values stay on the node. The Agent reports only key names and keyed fingerprints and renders locally.
- The control plane must never receive inline values from servers.json.
- servers.json and generated configs that contain values must be mode 0600. A permission mismatch is reported before managed mode is enabled.
- Extend normalized MCP descriptors with header key names and a secret fingerprint covering both env and headers.
- Existing ToolHub-managed MCP continues to use encrypted secrets. Add header_refs and secret kind mcp-header for centrally managed HTTP Authorization or other header values.
- Agent secret authorization must allow mcp-env and mcp-header only when the requested secret is referenced by an enabled desired MCP deployment on that node.
- Browser list/detail APIs return refs/key names and redacted status only, never values.

### Resource use

- Reuse the existing toolhub-agent service; do not add another daemon or container for synchronization.
- Watch only the MCP manifest parent/file and the top level of the shared Skills directory. Skill content changes already propagate through symlinks and do not require recursive link reconciliation.
- Debounce filesystem events, serialize reconciliation per shared source, and retain a periodic safety inventory as fallback.
- Do not run a full hash scan for every filesystem event.

## Target architecture

### Runtime and source model

- Consumer runtime kinds are codex, claude, hermes, grok, and openclaw.
- shared is a synthetic Skill deployment target only. It points at the canonical shared Skills root and is not an MCP runtime.
- A node may have zero or more shared sources. This server initially has one source named root-shared.
- Consumer rows describe configured paths and capabilities:
  - skills link destination
  - MCP destination and renderer format
  - inherited MCP source, used by Grok Build
  - enabled scopes
- Auto-detection without local config is read-only observed mode.
- Filesystem writes require an explicit Agent configuration mode of managed.

Suggested Agent configuration shape (the minimum managed set is Codex + Claude; the other consumers below are optional):

    "sharedSources": [
      {
        "name": "root-shared",
        "mode": "managed",
        "autoSync": true,
        "skillsRoot": "/root/.shared/skills",
        "mcpManifest": "/root/.shared/mcp/servers.json",
        "allowedSkillRoots": [
          "/root/.shared/skills",
          "/root/.agents/skills",
          "/root/.shared/vibe-skills"
        ],
        "consumers": {
          "codex": {
            "skillsPath": "/root/.codex/skills",
            "mcpPath": "/root/.codex/.tmp/plugins/plugins/shared-mcp/.mcp.json",
            "mcpFormat": "codex-plugin-json"
          },
          "claude": {
            "skillsPath": "/root/.claude/skills",
            "mcpPath": "/root/.claude/settings.json",
            "mcpFormat": "claude-settings-json"
          },
          "hermes": {
            "skillsPath": "/root/.hermes/skills",
            "mcpPath": "/root/.hermes/config.yaml",
            "mcpFormat": "hermes-yaml"
          },
          "grok": {
            "skillsPath": "/root/.grok/skills",
            "mcpInherits": "claude"
          },
          "openclaw": {
            "skillsPath": "/root/.openclaw/workspace/skills",
            "mcpPath": "/root/.openclaw/workspace/config/mcporter.json",
            "mcpFormat": "openclaw-mcporter-json"
          }
        }
      }
    ]

The exact JSON tags should follow existing config conventions. Defaults derive from config.Paths.Home; the /root values above are rollout data, not hard-coded package constants.

### Skills state model

- Scan the canonical source once and record each top-level entry with:
  - logical name
  - source path and resolved path
  - directory hash
  - directory or symlink type
  - allowed/blocked resolution status
  - external or ToolHub-managed ownership
- Record consumer link state separately:
  - expected source target
  - actual link target or conflicting object type
  - managed flag
  - in_sync, missing, drift, conflict, or blocked state
- Runtime-local Skills that are not links to the canonical source remain separate read-only discoveries.
- Existing shared entries are external and read-only by default. Adoption imports an immutable snapshot but does not rewrite or mark an external target.
- ToolHub may deploy an approved artifact to runtime shared only when the target is absent or already recorded as ToolHub-managed.
- Deploying or disabling a ToolHub-managed shared Skill also reconciles only its owned consumer links.
- External edits under /root/.shared/skills are observed source changes. ToolHub records and scans them but never rolls them back from a DB snapshot.

### MCP state model

- Parse servers.json into a strict normalized manifest:
  - enabled flag
  - stdio, SSE, or streamable HTTP transport
  - command and args, or URL
  - env key names
  - HTTP header key names
  - non-secret configuration fingerprint
  - keyed secret fingerprint
- Disabled servers remain in the source manifest but are absent from rendered managed blocks.
- Shared-file servers are mirrored into MCP inventory with authority shared-file and credential mode node-local.
- Shared-file MCP does not create ordinary mcp_deployments. Per-consumer desired/actual fingerprints live on bindings sourced from the manifest and rendered files.
- Existing ToolHub profiles and mcp_deployments remain unchanged for authority toolhub.
- Browser CRUD against a shared-file server returns 409 source_file_authoritative. The first implementation exposes sync and status, not a second editor.

### Renderer and merge model

- Implement renderer-specific Go adapters for:
  - Claude settings JSON mcpServers
  - Codex shared plugin .mcp.json
  - Hermes YAML mcp_servers
  - OpenClaw mcporter JSON
  - Grok inheritance validation
- Preserve every unknown top-level field and every unknown MCP server.
- Treat names originating from servers.json as the managed set.
- On first baseline, classify matching entries as managed candidates but perform a dry run before adoption.
- On later writes:
  - update a managed entry only if its current managed fingerprint equals the last expected fingerprint
  - remove a disabled/deleted entry only if it was previously managed and remains unchanged
  - report a conflict instead of overwriting a concurrent local edit
- Hermes task-trellis and acemcp are unknown local entries and therefore survive every render.
- Use YAML node-level editing for Hermes so unrelated document sections and comments are preserved as far as the YAML library permits.
- Keep one timestamped last-known-good backup per target during rollout; define bounded retention before general release.

### Synchronization model

Manual paths use one shared reconciler:

- Local CLI:

      toolhub-agent sync-shared --config /etc/toolhub-agent/agent.json --source root-shared --scope all --dry-run

- Browser/API:
  - POST /api/v1/shared-sources/{id}/sync
  - body scopes: skills, mcp, or both; optional dryRun
- Global POST /api/v1/reconcile also enqueues shared sync for matching nodes.

Automatic sync:

- toolhub-agent starts the watcher only for locally configured managed sources with autoSync true.
- MCP file create/write/rename events trigger MCP reconcile after debounce.
- shared Skills top-level create/remove/rename events trigger link reconcile after debounce.
- Startup and periodic inventory provide a fallback for missed events.
- The watcher calls the same SharedReconciler used by the CLI and signed task executor.
- Automatic local results are immediately followed by a redacted inventory report when connected.
- A per-source lock file in the Agent data directory prevents the service, CLI, and signed task from writing concurrently.

## Persistence changes

Add migration internal/store/migrations/004_shared_sources.sql. Do not edit migrations 001, 002, or 003.

### New tables

- shared_sources
  - node_id, name, mode, auto_sync
  - skills_root, mcp_manifest_path
  - config_fingerprint, source_fingerprint
  - status, last_scan_at, last_sync_at, last_error
  - unique node/name
- shared_consumers
  - source_id, consumer_kind
  - skills_path, mcp_path, mcp_format, inherits_from
  - skills_enabled, mcp_enabled
  - expected_fingerprint, actual_fingerprint, state, last_error
  - unique source/consumer
- shared_skill_links
  - source_id, consumer_id, skill_name
  - source_path, resolved_source_path, target_path
  - expected_target, actual_target, managed, state, last_seen_at
  - unique source/consumer/skill name

Do not add a parallel sync-run state machine. Reuse jobs and node_tasks for requested work, and use the source/consumer/link rows for desired-versus-actual projection.

### Existing table changes

- Expand runtime constraints deliberately:
  - runtimes: codex, claude, hermes, grok, openclaw
  - deployments and skill_discoveries: add grok, openclaw, and synthetic shared
  - mcp_deployments, mcp_runtime_bindings, and mcp_capture_tokens: add grok and openclaw, but not shared
- mcp_servers:
  - add authority with toolhub or shared-file
  - add nullable shared_source_id
  - add header_refs JSONB
  - add credential_mode with toolhub-secret or node-local
- mcp_runtime_bindings:
  - add header_keys
  - add desired_fingerprint and actual_fingerprint if the current config fingerprint fields cannot represent both sides clearly

Constraint migration must preserve current rows and use named constraints. Verify both a clean database and upgrade from an applied 001/002/003 database.

## Protocol and orchestration changes

### Domain and protocol

- Add shared-source inventory DTOs and MCP HeaderKeys to internal/domain/models.go.
- Add task kind sync_shared to internal/agentclient.Executor.
- Add a typed payload containing source name/ID, scopes, dryRun, and expected source fingerprint.
- The task never contains arbitrary paths, shell text, inline env values, or inline header values.
- Resolve paths from the local Agent config, verify the expected fingerprint, then call SharedReconciler.
- Persist task history so duplicate task IDs return the prior result.

### Producer-consumer trace

- HTTP sync handler enqueues job kind shared_sync with plural sourceIds/nodeIds and scopes.
- Worker validates selectors, loads the target node/source, and creates signed sync_shared tasks.
- WSS and pinned SSH use the existing delivery paths.
- Executor runs the same reconciler as local auto/manual CLI.
- CompleteTask projects shared source/consumer status from the redacted result.
- Automatic local watcher results arrive through the next inventory projection, not a fabricated succeeded job.

### Store behavior

- ReplaceInventory upserts shared sources, consumers, link state, and shared MCP bindings transactionally.
- Omitted shared rows are marked missing only within the scanned source/node scope; do not accidentally delete unrelated sources.
- Add store methods for list/detail/status, scoped sync selection, and CompleteTask projection.
- Keep all new SQL in internal/store. Do not add handler Pool().Exec calls.
- Audit manual sync requests, mode/status transitions, conflicts, and secret-redacted source changes.

## API and UI changes

### API

- GET /api/v1/shared-sources
- GET /api/v1/shared-sources/{id}
- POST /api/v1/shared-sources/{id}/sync
- Extend POST /api/v1/reconcile to enqueue shared_sync in addition to existing Skill and MCP pipelines.
- Expose shared-source capability in node/detail responses.
- Permit runtime shared as a Skill target only when that node reports a managed shared source.
- Return 409 source_file_authoritative for update/delete/deploy operations that would make PostgreSQL authoritative over a shared-file MCP server.
- Read routes remain viewer/operator/admin; sync is operator/admin; configuration paths are not browser-editable in the first release.
- Update api/openapi.yaml and docs/API.md. Document Agent-only inventory/task shapes in the protocol/rollout docs.

### UI

- Nodes: show shared source path, observed/managed mode, auto-sync status, last scan/sync, and conflicts.
- Skills:
  - show canonical shared entries once
  - label consumer link coverage across the configured primary and optional Agents
  - keep runtime-local discoveries separate
  - offer one Shared source target instead of duplicate per-consumer deployment targets on this node
- MCP:
  - label shared-file authority and node-local credentials
  - show the seven enabled source servers and per-consumer render state
  - show Grok as inherited from Claude
  - disable DB CRUD for shared-file rows with an actionable explanation
- Settings or Nodes detail: add Dry run and Sync now actions for Skills, MCP, or all.
- Preserve existing page-local DTO, api client, useData, shared components, manual routing, RBAC boundaries, and current uncommitted UI work.

## Implementation phases

### Phase 0: freeze, backup, and fixtures

- [ ] Record redacted hashes, file modes, symlink targets, and ownership for the canonical source and every consumer.
- [ ] Back up servers.json, all four generated MCP files, Hermes config, and the five Skill consumer directories without following links.
- [ ] Capture redacted fixture files for renderer tests.
- [ ] Confirm no current cron/timer or shell generator is writing concurrently.
- [ ] Add a feature flag/config mode so all new behavior starts observed-only.

### Phase 1: persistence and contracts

- [ ] Add migration 004 and store integration coverage.
- [ ] Add domain inventory/state types and runtime-kind constants instead of repeated string checks.
- [ ] Add header-key normalization and central header secret refs.
- [ ] Add API contracts and read-only shared-source endpoints.

### Phase 2: Agent discovery

- [ ] Add local config parsing, home-relative defaults, validation, and read-only auto-probe.
- [ ] Scan the canonical Skills root once, safely resolve allowed top-level symlinks, and classify consumer links/local entries.
- [ ] Parse servers.json without transmitting values and report redacted descriptors/fingerprints.
- [ ] Add Grok and OpenClaw runtime discovery.

### Phase 3: Skills reconciliation

- [ ] Implement owned-link state in the Agent data directory.
- [ ] Implement dry-run, create, update, and safe stale-link removal with CAS checks.
- [ ] Add synthetic shared deployment support for approved ToolHub artifacts.
- [ ] Preserve all unowned directories/symlinks and report conflicts.
- [ ] Add local sync-shared CLI scope skills.

### Phase 4: MCP reconciliation

- [ ] Implement the four renderers plus Grok inheritance check.
- [ ] Implement managed-key merge, local exception preservation, CAS conflict handling, atomic writes, permissions, and backups.
- [ ] Add local sync-shared CLI scope mcp/all.
- [ ] Replace the old scripts only with thin compatibility wrappers after the Go path passes dry-run and canary validation.

### Phase 5: jobs, tasks, API, and UI

- [ ] Add shared_sync job and sync_shared signed task end to end.
- [ ] Project results through CompleteTask for both WSS and SSH.
- [ ] Add manual sync and global reconcile behavior with plural selectors.
- [ ] Add UI status, dry run, sync, and shared-file read-only behavior.
- [ ] Update documentation and rollout instructions.

### Phase 6: automatic sync and server canary

- [ ] Add debounced watcher and per-source locking.
- [ ] Enable managed mode first with autoSync false.
- [ ] Run dry-run and resolve every reported conflict.
- [ ] Apply MCP only and verify all enabled servers plus Hermes local exceptions.
- [ ] Apply Skills links and verify all five consumers, including Grok and OpenClaw.
- [ ] Enable autoSync, edit one harmless enabled flag and add/remove a disposable test Skill to prove event-driven reconciliation.
- [ ] Observe for at least one scheduled inventory cycle before retiring the old cron/script path.

### Phase 7: optional native control-plane migration

Keep this independent from shared-source feature rollout.

- [ ] Add packaging/systemd/toolhub.service and an environment-file example.
- [ ] Build the embedded-UI toolhub binary and install it under /usr/local/bin.
- [ ] Run the control plane as a dedicated unprivileged toolhub user; keep toolhub-agent as the local filesystem integration service.
- [ ] Create a dedicated ToolHub database/user in the existing loopback PostgreSQL 16 instance.
- [ ] Preserve the current TOOLHUB_MASTER_KEY exactly so encrypted rows remain decryptable.
- [ ] Copy any required ToolHub data volume content to /var/lib/toolhub with verified ownership and hashes.
- [ ] Take a pg_dump of the Docker database and restore it into a temporary host database.
- [ ] Smoke the native binary on an alternate loopback port against the temporary database; do not point the Agent at it.
- [ ] For cutover, stop only the Docker ToolHub writer, take a final dump, restore the dedicated host DB, start native ToolHub on 127.0.0.1:18480, then run health/API/browser smoke.
- [ ] Keep Docker containers, images, compose configuration, and volumes intact during the soak period. Do not run docker compose down -v.
- [ ] After a successful soak, stop the unused Docker PostgreSQL/ToolHub containers to remove their steady-state memory use.

## Expected repository touch points

- Schema/store:
  - internal/store/migrations/004_shared_sources.sql
  - internal/store/db.go
  - internal/store/discoveries.go
  - internal/store/mcp.go
  - internal/store/reconcile.go
  - internal/store/resources.go
  - internal/store/secrets.go
- Domain/protocol/worker:
  - internal/domain/models.go
  - internal/protocol/task.go
  - internal/worker/worker.go
- Agent/runtime:
  - cmd/toolhub-agent/main.go
  - internal/agentclient/config.go
  - internal/agentclient/discovery.go
  - internal/agentclient/executor.go
  - internal/agentclient/runner.go
  - internal/runtime/inventory.go
  - internal/runtime/deploy.go
  - internal/runtime/mcp.go
  - new focused shared source, Skill link, MCP renderer, and watcher files under internal/runtime
- API/docs:
  - internal/httpapi/api.go
  - internal/httpapi/discoveries.go
  - internal/httpapi/mcp.go
  - internal/httpapi/skills.go
  - api/openapi.yaml
  - docs/API.md
  - docs/SECURITY.md
  - docs/ROLLOUT.md
- Web:
  - web/src/App.tsx only if navigation changes
  - web/src/api/client.ts
  - web/src/pages/Nodes.tsx
  - web/src/pages/Skills.tsx
  - web/src/pages/MCP.tsx
  - web/src/pages/Settings.tsx
  - adjacent styles and Playwright smoke
- Packaging:
  - packaging/systemd/toolhub.service
  - packaging/systemd/toolhub.env.example
  - README.md

Before editing any listed file, inspect the current dirty diff and preserve unrelated user changes, especially web/src.

## Validation strategy

### Focused unit and integration coverage

- Path/config:
  - /root home and a non-root temporary home
  - explicit paths override defaults
  - invalid relative/escaped consumer paths rejected
- Skills:
  - real source directory, allowed source symlink, blocked escaped symlink, cycle
  - all five consumer links
  - unowned directory/symlink conflict
  - idempotent reconcile
  - stale owned link removal and stale modified link conflict
  - shared deploy refuses unowned existing targets
- MCP:
  - all current transports and enabled/disabled behavior
  - env/header values absent from descriptors, DB JSON, logs, and task payloads
  - Claude, Codex plugin, Hermes, and OpenClaw golden fixtures
  - Grok inheritance
  - Hermes task-trellis and acemcp preservation
  - unknown JSON/YAML fields preserved
  - concurrent managed-key edit produces conflict
  - atomic write failure restores/retains the previous file
  - target permissions are 0600
- Watcher:
  - event debounce
  - no recursive full scan per content write
  - lock serialization between watcher, CLI, and task
  - retry without tight loop
- Store/API:
  - clean migration and 001/002/003 upgrade
  - source-scoped missing marking
  - shared-file CRUD returns 409
  - sync RBAC/CSRF and plural selector behavior
  - wrong-node and wrong-kind header secret denial
- Protocol:
  - canonical signature stability
  - duplicate task idempotency
  - WSS and SSH task result projection

### Repository gates

Run focused tests first, then:

    go test ./...
    go vet ./...
    go test -race ./...
    cd web && npm ci --ignore-scripts
    cd web && npm audit --audit-level=high
    cd web && npm run typecheck
    cd web && npm run build
    make docker-config

For integrated validation:

    docker compose up -d --build --wait
    curl --fail http://127.0.0.1:18480/healthz
    TOOLHUB_SMOKE_EMAIL=... TOOLHUB_SMOKE_PASSWORD=... sh scripts/smoke-api.sh
    cd web && TOOLHUB_E2E_EMAIL=... TOOLHUB_E2E_PASSWORD=... npm run test:e2e

### Live-server acceptance matrix

- Dry-run reports no unexpected overwrite or removal.
- Shared Skills appear once as canonical entries; Claude and Codex link states are always visible, with optional consumer states shown when configured.
- Grok and OpenClaw Skills links are created without changing local-only entries when explicitly configured.
- The seven enabled MCP servers render to the intended consumers.
- Disabled playwright and github stay absent from generated managed blocks.
- Hermes retains task-trellis and acemcp byte-for-byte at the semantic mapping level.
- Claude and Codex files that are currently absent/missing are created only after dry-run approval.
- A manual UI/API sync and local CLI sync produce the same fingerprints.
- A servers.json change triggers automatic reconcile without cron.
- No secret value appears in PostgreSQL JSON, ordinary API responses, jobs, audit events, or logs.
- CPU and memory are measured before/after; the watcher adds no extra process and negligible idle CPU.

## Rollback

### Shared-source feature rollback

- Set the local source mode to observed and autoSync false; restart toolhub-agent.
- Cancel only pending shared_sync jobs/tasks. Do not cancel unrelated Skill or MCP work.
- Restore target MCP files from verified pre-rollout backups using expected hashes.
- Restore only ToolHub-owned symlinks from the recorded topology. Never bulk-delete a consumer directory.
- Re-enable the previous scripts/alias only after confirming no Agent watcher is active.
- Migration 004 is additive and has no down migration. Older binaries may ignore its tables, but rollback must first verify there are no pending synthetic shared deployments or new-runtime tasks.
- Keep external /root/.shared source contents untouched during application rollback.

### Native deployment rollback

- Stop native ToolHub.
- If no writes occurred after cutover, restart the retained Docker stack and volume on 127.0.0.1:18480.
- If writes occurred, dump the host database and restore it into a fresh Docker PostgreSQL database before restarting the Docker control plane; keep the same master key.
- Verify /healthz, login, node connection, and one dry-run shared sync.
- Never delete Docker volumes until the native service has completed the agreed soak and a tested database backup exists.

## Completion criteria

- /root/.shared is the only authoritative Skill/MCP source on this server.
- Codex and Claude consume the shared Skills topology by default; Hermes, Grok, and OpenClaw are opt-in consumers.
- Claude and Codex receive valid native MCP output by default; optional renderers remain available and Grok inherits Claude when configured.
- Manual CLI/API sync and automatic event-driven sync share one safe implementation.
- Local exceptions and unowned files are preserved; conflicts are visible instead of overwritten.
- ToolHub stores only redacted shared-source metadata and fingerprints.
- Existing ToolHub-managed nodes remain backward compatible.
- Native systemd migration, if performed, is independently reversible and leaves Docker volumes available for rollback.
