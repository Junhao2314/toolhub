# API Notes

The contract is [api/openapi.yaml](../api/openapi.yaml). Browser endpoints use an HttpOnly `toolhub_session` cookie. Unsafe requests require `X-CSRF-Token`; the token is returned by login and the public session probe.

Main namespaces:

- `/api/v1/auth`, `/users`, `/audit`, `/settings`
- `/api/v1/nodes`, `/skills`, `/sources`, `/deployments`, `/updates`, `/discoveries`, `/sync`, `/reconcile`, `/jobs`
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

- `GET /discoveries` returns Skill discoveries and MCP runtime bindings. It never returns MCP secret values or per-node HMAC fingerprints.
- MCP entries are automatically captured through the Agent-only protocol and have no browser Adopt/Approve action. Their first observed state is the desired/actual baseline; later local edits or deletion become drift.
- `POST /discoveries/{id}/adopt-skill` is administrator-only. It queues `skill_adopt`; the Agent uploads a safely packaged snapshot and writes the managed marker only after the backend rescans and imports the matching hash. The resulting Skill remains pending review.
- `POST /reconcile` queues one scoped `sync` job and one scoped `mcp_sync` job. Accepted selector fields are `nodeIds`, `skillIds`, `profileIds`, and `mcpDeploymentIds`.
- `202 Accepted` and a succeeded Job mean orchestration/dispatch completed. Actual deployment state changes only after the corresponding Agent task succeeds.

The Agent-only descriptor, capture, and Skill upload contracts are documented in [Agent Protocol](AGENT_PROTOCOL.md).
