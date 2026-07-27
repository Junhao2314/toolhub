# API Notes

The contract is [api/openapi.yaml](../api/openapi.yaml). Browser endpoints use an HttpOnly `toolhub_session` cookie. Unsafe requests require `X-CSRF-Token`; the token is returned by login and the public session probe.

Main namespaces:

- `/api/v1/auth`, `/users`, `/audit`, `/settings`
- `/api/v1/nodes`, `/skills`, `/sources`, `/deployments`, `/updates`, `/discoveries`, `/shared-sources`, `/sync`, `/reconcile`, `/jobs`
- `/api/v1/market`, `/recommendations`, `/mcp`
- `/agent/v1/enroll`, `/connect`, `/artifacts`, `/secrets`

Errors use `{ "error": { "code", "message", "requestId" } }`. List responses use `{ "items": [...] }`. Agent WSS messages are typed envelopes; task signatures cover ID, kind, and canonical payload.

## Marketplace sources

- `GET /api/v1/market/search?q=…&source=all|skillsmp|xiaping&page&limit` fans out over the configured sources and returns normalized listings: `source`, `id`, `name`, `description`, `author`, provenance URLs, and per-source metrics (SkillsMP `stars`; Xiaping `downloads`/`reviews`/`version`/`status`). A failing source never blocks the others: partial failures are reported as sanitized per-source statuses under `errors`; a total failure returns `429`/`502`.
- SkillsMP is anonymous with an optional `SKILLSMP_API_KEY` for higher quotas. Xiaping search is public and never receives the download key; `XIAPING_API_KEY` is sent only to the authenticated download endpoint. `XIAPING_BASE_URL` overrides the default `https://xiaping.coze.com` origin and must remain HTTPS.
- `POST /skills` with `kind: "xiaping"` requires `externalId` (the Xiaping skill id) and a configured `XIAPING_API_KEY` (`412 xiaping_not_configured` otherwise). The worker downloads the platform ZIP through a proxy-free, DNS-pinned public-HTTPS client, scans it under the standard package limits, and queues it for review like any other import. The provider-reported coin charge is recorded in provenance. Identical active requests return the existing job, and Xiaping imports use one attempt only so ToolHub never automatically repeats a potentially charged download; a failed import must be explicitly queued again.

## Browser credentials

- Login accepts `{ "identifier", "password" }`; `identifier` matches either a lowercase username or the required email address, case-insensitively. Authentication failures use one non-enumerating error.
- Usernames are normalized to lowercase and contain 3–32 letters, numbers, `.`, `_`, or `-`; `@` is reserved for email identifiers.
- `PATCH /account/username` and `PATCH /account/password` require `currentPassword`. A successful change revokes every session for that user, including the current browser session.
- Administrators create users with `passwordMode: "random" | "manual"` and reset credentials through `POST /users/{id}/password`. Random passwords are returned once as `temporaryPassword`; manual passwords are never echoed.
- Temporary passwords set `passwordChangeRecommended=true`. The flag clears only after the user changes their own password.

## Runtime discovery and reconciliation

- `GET /discoveries` returns runtime-local Skill discoveries, one canonical `shared` discovery per importable shared Skill, and MCP runtime bindings. It never returns MCP secret values or per-node HMAC fingerprints.
- MCP entries are captured through the Agent-only protocol. The Agent scans mcpm plus non-relay native entries; plaintext values are submitted once over the authenticated Agent route and encrypted in PostgreSQL.
- Initial mcpm discovery creates the fixed `toolhub-codex` and `toolhub-claude` profiles. The live profile membership is seeded into both with deployment state `observed`; only `POST /mcp/deployments` advances a selected node/runtime to `pending` and queues `mcp_sync`.
- `PUT /mcp/profiles/{id}/servers` replaces membership only for those fixed managed profiles. Membership edits refresh desired hashes/bindings but preserve `observed` state until the matching runtime is explicitly deployed.
- Legacy shared-manifest MCP entries are imported as disabled `shared-import` candidates. A collision keeps the live mcpm name and renames the candidate with `-shared`; the browser shows import provenance and conflict state.
- `POST /discoveries/{id}/adopt-skill` is administrator-only. For a shared-source discovery the Agent uploads a safely packaged immutable snapshot without writing the legacy tree. The resulting Skill remains pending review, then deploys as a materialized copy through ordinary per-runtime targets.
- `POST /deployments/{id}/rollback` swaps to the recorded previous approved version. For a first successful deployment with no previous version, rollback advances desired state to disabled and the Agent safely removes the managed directory; a later target update can re-enable the retained version.
- `GET /shared-sources` and `GET /shared-sources/{id}` expose redacted node-local source state for discovery/import review only. There is no shared-source sync or targets writer API.
- A shared Skill whose package scan fails carries the reason in `lastError`; it remains blocked on its own discovery row without blocking import of healthy siblings.
- `POST /reconcile` queues scoped `sync` and `mcp_sync` jobs. Accepted selector fields are `nodeIds`, `skillIds`, `profileIds`, and `mcpDeploymentIds`.
- `202 Accepted` and a succeeded Job mean orchestration/dispatch completed. Actual deployment state changes only after the corresponding Agent task succeeds.

## MCP delivery and legacy imports

- PostgreSQL is authoritative. `mcp_sync` sends a signed `apply_mcp` task whose payload names the fixed mcpm profile and contains only encrypted secret references. The Agent resolves authorized values, atomically updates `~/.config/mcpm/servers.json` at mode `0600`, then repairs the runtime's single native mcpm anchor.
- Managed delivery accepts only the exact fixed mapping: `toolhub-codex` to Codex and `toolhub-claude` to Claude. Arbitrary or mismatched profiles return `409` and are excluded from worker dispatch and Agent secret authorization.
- Shared manifest rows remain mirrored with `authority: "shared-file"` and `credentialMode: "node-local"` for read-only observation. Separate `shared-import` candidate rows are ordinary ToolHub-authoritative records after one-time capture; they remain disabled until explicitly reviewed.
- ToolHub-authoritative MCP servers may contain both `env` and `headers` plaintext only in create/capture requests. Values are encrypted into `mcp-env` or `mcp-header` records; list/detail and task payloads contain references only.
- Updating or deleting a mirrored shared-file server, or adding it to a ToolHub profile, returns `409 source_file_authoritative`. Import and edit the separate central candidate instead.
- Header names are validated as HTTP field-name tokens and stored in canonical form. Values are never included in ordinary browser responses, audit metadata, inventory JSON, or logs.

The Agent-only descriptor, capture, and Skill upload contracts are documented in [Agent Protocol](AGENT_PROTOCOL.md).
