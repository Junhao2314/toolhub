# API Notes

The contract is [api/openapi.yaml](../api/openapi.yaml). Browser endpoints use an HttpOnly `toolhub_session` cookie. Unsafe requests require `X-CSRF-Token`; the token is returned by login and the public session probe.

Main namespaces:

- `/api/v1/auth`, `/users`, `/audit`, `/settings`
- `/api/v1/nodes`, `/skills`, `/sources`, `/deployments`, `/updates`, `/discoveries`, `/shared-sources`, `/sync`, `/reconcile`, `/jobs`
- `/api/v1/market`, `/recommendations`, `/mcp`
- `/agent/v1/enroll`, `/connect`, `/artifacts`, `/secrets`

Errors use `{ "error": { "code", "message", "requestId" } }`. List responses use `{ "items": [...] }`. Agent WSS messages are typed envelopes; task signatures cover ID, kind, and canonical payload.

## Browser credentials

- Login accepts `{ "identifier", "password" }`; `identifier` matches either a lowercase username or the required email address, case-insensitively. Authentication failures use one non-enumerating error.
- Usernames are normalized to lowercase and contain 3–32 letters, numbers, `.`, `_`, or `-`; `@` is reserved for email identifiers.
- `PATCH /account/username` and `PATCH /account/password` require `currentPassword`. A successful change revokes every session for that user, including the current browser session.
- Administrators create users with `passwordMode: "random" | "manual"` and reset credentials through `POST /users/{id}/password`. Random passwords are returned once as `temporaryPassword`; manual passwords are never echoed.
- Temporary passwords set `passwordChangeRecommended=true`. The flag clears only after the user changes their own password.

## Runtime discovery and reconciliation

- `GET /discoveries` returns runtime-local Skill discoveries, one canonical `shared` discovery per shared Skill, consumer link coverage, and MCP runtime bindings. It never returns MCP secret values or per-node HMAC fingerprints.
- MCP entries are automatically captured through the Agent-only protocol and have no browser Adopt/Approve action. Their first observed state is the desired/actual baseline; later local edits or deletion become drift.
- `POST /discoveries/{id}/adopt-skill` is administrator-only. It queues `skill_adopt`; the Agent uploads a safely packaged snapshot and writes the managed marker only after the backend rescans and imports the matching hash. The resulting Skill remains pending review.
- `GET /shared-sources` and `GET /shared-sources/{id}` expose redacted node-local source state, five consumer link states, MCP key names, and desired/actual renderer fingerprints. Locally configured filesystem paths are visible to authorized operators but are not browser-editable.
- `POST /shared-sources/{id}/sync` accepts `scopes: ["skills" | "mcp"]` and `dryRun`. Viewer access is read-only; operators and administrators may queue sync. A write request for an observed-only source returns `409 shared_source_observed`.
- `POST /reconcile` queues scoped `sync`, `mcp_sync`, and `shared_sync` jobs. Accepted selector fields are `nodeIds`, `skillIds`, `profileIds`, `mcpDeploymentIds`, `sharedSourceIds`, and `sharedScopes`.
- `202 Accepted` and a succeeded Job mean orchestration/dispatch completed. Actual deployment state changes only after the corresponding Agent task succeeds.

## Shared-file MCP authority

- Shared manifest rows are mirrored with `authority: "shared-file"` and `credentialMode: "node-local"`; PostgreSQL stores descriptors, key names, and fingerprints only. They do not create ordinary MCP deployments.
- ToolHub-authoritative MCP servers may contain both `env` and `headers` plaintext only in create/capture requests. Values are encrypted into `mcp-env` or `mcp-header` records; list/detail and task payloads contain references only.
- Updating or deleting a shared-file server, or adding it to a ToolHub profile, returns `409 source_file_authoritative`. Edit the node-local manifest and then scan/sync instead.
- Header names are validated as HTTP field-name tokens and stored in canonical form. Values are never included in ordinary browser responses, audit metadata, inventory JSON, or logs.

The Agent-only descriptor, capture, and Skill upload contracts are documented in [Agent Protocol](AGENT_PROTOCOL.md).
