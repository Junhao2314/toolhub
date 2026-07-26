# Agent Discovery Protocol

Agent routes use `X-ToolHub-Node-ID` plus `Authorization: Bearer <agent-token>`. They are outside browser sessions and CSRF middleware.

## Inventory descriptor phase

`POST /agent/v1/discoveries/descriptors` accepts `AgentInventory`: the five consumer runtimes (`codex`, `claude`, `hermes`, `grok`, `openclaw`) plus optional redacted `sharedSources`. Each MCP descriptor contains transport, command, arguments, URL, environment/header key names, a SHA-256 fingerprint of canonical non-secret configuration, and an HMAC-SHA256 fingerprint under the per-node task key. Secret fingerprint entries are namespaced as `env:<name>` and `header:<canonical-name>`; legacy env-only Agent captures remain accepted during rolling upgrades. Ordinary inventory never contains values.

Each shared-source inventory item reports its local configured mode, auto-sync flag, source paths, source/config fingerprints, canonical Skill descriptors, consumer link state, MCP descriptors, and per-consumer desired/actual render fingerprints. Shared manifest values and rendered env/header values are never transmitted.

The response contains `captureRequests` only for unknown MCP identities that require secret values. Each request has a short-lived, single-use token bound to node, runtime, original server name, identity, and both fingerprints.

## Secret capture phase

`POST /agent/v1/discoveries/capture` accepts the capture token, its bound runtime/name/identity, and separate `env` and `headers` maps. The backend verifies expiry, replay, node binding, identity, both key sets, and the per-node HMAC fingerprint. It compares candidate encrypted secrets in memory, creates or reuses a `runtime-auto` server, encrypts new values as `mcp-env` or `mcp-header` with record-ID AAD, and creates the node/runtime baseline. Plaintext is never returned, audited, logged, or stored in ordinary JSON.

## Skill adoption upload

The `skill_adopt` worker job creates a signed `adopt_skill` task containing `discoveryId`, `runtime`, canonical `path`, and expected canonical ZIP SHA-256. The Agent rejects escaped/protected/symlinked content, builds the bounded canonical ZIP, then uploads it to `POST /agent/v1/discoveries/{discoveryID}/skill` with `X-ToolHub-Task-ID` and `X-Content-SHA256`.

The backend authorizes the active task, rescans the ZIP with the standard limits, verifies the discovery hash, and creates an immutable node-source snapshot in pending review. Only after a successful response does the Agent atomically write `.toolhub-managed.json`.

## Closed task and message kinds

- Agent tasks: `scan_inventory`, `deploy_skill`, `apply_mcp`, `adopt_skill`, `sync_shared`.
- Agent-to-hub messages: `heartbeat`, `inventory`, `task_result`.
- Hub-to-Agent messages: `task`, `error`.

Task signatures continue to cover task ID, kind, and canonical JSON payload. SSH fallback still uploads the same signed task and invokes only `toolhub-agent run-task`.

## Shared-source synchronization

`sync_shared` contains only `sourceId`, configured `sourceName`, `scopes` (`skills` and/or `mcp`), `dryRun`, and an optional expected source fingerprint. It never contains filesystem paths, shell text, inline env values, or inline headers. The Agent resolves paths from its local configuration, verifies the expected fingerprint, and calls the same `SharedReconciler` used by:

- `toolhub-agent sync-shared --config ... --source ... --scope skills|mcp|all [--dry-run]`
- the debounced local `fsnotify` watcher for managed sources with `autoSync: true`
- signed WSS or SSH-delivered tasks

The result contains redacted source/consumer state, actions, and conflicts. Duplicate task IDs return the prior 30-day history record. After a successful shared sync, the connected Runner performs a fresh inventory scan; automatic watcher results are projected by that inventory and do not fabricate a succeeded Job.
