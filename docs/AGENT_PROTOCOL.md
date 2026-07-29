# Agent Discovery Protocol

Agent routes use `X-ToolHub-Node-ID` plus `Authorization: Bearer <agent-token>`. They are outside browser sessions and CSRF middleware.

## Inventory descriptor phase

`POST /agent/v1/discoveries/descriptors` accepts `AgentInventory`: the five consumer runtimes (`codex`, `claude`, `hermes`, `grok`, `openclaw`), optional redacted `sharedSources`, and optional `mcpImports`. Each MCP descriptor contains transport, command, arguments, URL, environment/header key names, a SHA-256 fingerprint of canonical non-secret configuration, and an HMAC-SHA256 fingerprint under the per-node task key. Secret fingerprint entries are namespaced as `env:<name>` and `header:<canonical-name>`; legacy env-only Agent captures remain accepted during rolling upgrades. Ordinary inventory never contains values. A current Agent advertises `hermes_read_only_import_v1` in the Hermes runtime's open `config.capabilities` array so older control planes can ignore it safely.

`mcpImports` contains read-only candidates from the mcpm registry and legacy shared manifests. mcpm entries report target runtime membership derived from `toolhub-codex`, `toolhub-claude`, or recognized native runtime anchors for the initial `all-mcp` seed. If no native anchor evidence exists, the legacy both-runtime fallback is retained for compatibility. Shared-manifest entries are disabled candidates. Import provenance, profile tags, and key names are included; values are not.

Each shared-source inventory item reports its local source paths, source/config fingerprints, canonical Skill descriptors, legacy consumer observations, and MCP key names. Shared-source mode is normalized to observed/read-only; the Agent has no shared-source writer.

The response contains `captureRequests` for unknown non-Hermes identities that require baseline values, plus a Hermes identity only when an administrator has pinned that discovery generation for import. Ordinary Hermes scans never receive capture authorization. Each request has a short-lived, single-use token bound to node, runtime, original server name, identity, fingerprints, purpose, and, for Hermes, the discovery binding.

## Secret capture phase

`POST /agent/v1/discoveries/capture` accepts the capture token, its bound runtime/name/identity, and separate `env` and `headers` maps. The backend verifies expiry, replay, node binding, identity, both key sets, and the per-node HMAC fingerprint. It then either creates/reuses a `runtime-auto` baseline, imports an `mcpm-import` / disabled `shared-import` server, or completes the one requested `hermes_snapshot`. A Hermes re-import always creates a new enabled central server and no Profile membership. New values are encrypted as `mcp-env` or `mcp-header` with record-ID AAD. Plaintext is never returned, audited, logged, or stored in ordinary JSON.

## Skill adoption upload

The `skill_adopt` worker job creates a signed `adopt_skill` task containing `discoveryId`, `runtime`, canonical `path`, and expected canonical ZIP SHA-256. The Agent rejects escaped/protected/symlinked content, builds the bounded canonical ZIP, then uploads it to `POST /agent/v1/discoveries/{discoveryID}/skill` with `X-ToolHub-Task-ID` and `X-Content-SHA256`.

The backend authorizes the active task, rescans the ZIP with the standard limits, verifies the discovery hash, and creates an immutable node-source snapshot in pending review. For a normal runtime discovery, only after a successful response does the Agent atomically write `.toolhub-managed.json`. A shared-source discovery remains an import-only source and receives no marker.

Hermes uses a separate `skill_snapshot_import` worker job and signed `import_skill_snapshot` task with the same bounded canonical packaging/upload boundary. The payload fixes `discoveryId`, `runtime: "hermes"`, canonical path, and SHA-256; upload goes to `POST /agent/v1/discoveries/{discoveryID}/skill-snapshot`. Both Agent and backend verify task identity, and `markerWritten` is always false.

## Closed task and message kinds

- Agent tasks: `scan_inventory`, `deploy_skill`, `apply_mcp`, `adopt_skill`, `import_skill_snapshot`.
- Agent-to-hub messages: `heartbeat`, `inventory`, `task_result`.
- Hub-to-Agent messages: `task`, `error`.

Task signatures continue to cover task ID, kind, and canonical JSON payload. SSH fallback still uploads the same signed task and invokes only `toolhub-agent run-task`.

The Agent and runtime layers reject `deploy_skill`, `adopt_skill`, and `apply_mcp` when they would write Hermes. Only `scan_inventory` and the marker-free `import_skill_snapshot` read Hermes state.

## MCP delivery task

`apply_mcp` contains deployment/profile IDs, desired generation/hash, runtime, fixed `mcpmProfile`, enabled state, and typed server references. Secret values are never inline; only `envRefs` and `headerRefs` IDs are signed into the payload.

The Agent accepts managed MCP delivery only for Codex and Claude and requires the exact runtime/profile mapping (`toolhub-codex` or `toolhub-claude`). It resolves authorized secrets, applies a conflict-safe structured patch to `~/.config/mcpm/servers.json` (using an isolated mcpm CLI render only when semantically identical), writes mode `0600`, and then repairs exactly one native relay anchor. Claude uses top-level `~/.claude.json` `mcpServers`; Codex uses `[mcp_servers.toolhub-codex]` in `~/.codex/config.toml` with `startup_timeout_sec = 60`, extending Codex's default 10-second MCP startup window for a cold aggregate. If the anchor edit fails, the mcpm registry is restored.

Duplicate task IDs return the prior 30-day history record. After a successful `apply_mcp` or `deploy_skill`, a connected Runner immediately submits a fresh inventory before the final task result. SSH fallback decodes and executes the same signed typed payload through `toolhub-agent run-task`.
