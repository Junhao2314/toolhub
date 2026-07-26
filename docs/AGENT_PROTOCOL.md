# Agent Discovery Protocol

Agent routes use `X-ToolHub-Node-ID` plus `Authorization: Bearer <agent-token>`. They are outside browser sessions and CSRF middleware.

## Inventory descriptor phase

`POST /agent/v1/discoveries/descriptors` accepts the normal runtime inventory plus normalized MCP descriptors. Each descriptor contains transport, command, arguments, URL, environment key names, a SHA-256 fingerprint of the canonical non-secret configuration, and an HMAC-SHA256 fingerprint of the canonical secret map under the per-node task key. Ordinary inventory never contains environment values.

The response contains `captureRequests` only for unknown MCP identities that require secret values. Each request has a short-lived, single-use token bound to node, runtime, original server name, identity, and both fingerprints.

## Secret capture phase

`POST /agent/v1/discoveries/capture` accepts the capture token, its bound runtime/name/identity, and the requested secret map. The backend verifies expiry, replay, node binding, identity, environment key set, and the per-node HMAC fingerprint. It compares candidate encrypted secrets in memory, creates or reuses a `runtime-auto` server, encrypts any new values with record-ID AAD, and creates the node/runtime baseline. Plaintext is never returned, audited, logged, or stored in ordinary JSON.

## Skill adoption upload

The `skill_adopt` worker job creates a signed `adopt_skill` task containing `discoveryId`, `runtime`, canonical `path`, and expected canonical ZIP SHA-256. The Agent rejects escaped/protected/symlinked content, builds the bounded canonical ZIP, then uploads it to `POST /agent/v1/discoveries/{discoveryID}/skill` with `X-ToolHub-Task-ID` and `X-Content-SHA256`.

The backend authorizes the active task, rescans the ZIP with the standard limits, verifies the discovery hash, and creates an immutable node-source snapshot in pending review. Only after a successful response does the Agent atomically write `.toolhub-managed.json`.

## Closed task and message kinds

- Agent tasks: `scan_inventory`, `deploy_skill`, `apply_mcp`, `adopt_skill`.
- Agent-to-hub messages: `heartbeat`, `inventory`, `task_result`.
- Hub-to-Agent messages: `task`, `error`.

Task signatures continue to cover task ID, kind, and canonical JSON payload. SSH fallback still uploads the same signed task and invokes only `toolhub-agent run-task`.
