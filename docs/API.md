# API Notes

The contract is [api/openapi.yaml](../api/openapi.yaml). Browser endpoints use an HttpOnly `toolhub_session` cookie. Unsafe requests require `X-CSRF-Token`; the token is returned by login and the public session probe.

Main namespaces:

- `/api/v1/auth`, `/users`, `/audit`, `/settings`
- `/api/v1/nodes`, `/skills`, `/sources`, `/deployments`, `/updates`, `/discoveries`, `/shared-sources`, `/sync`, `/reconcile`, `/jobs`
- `/api/v1/market`, `/recommendations`, `/mcp`, `/profiles`, `/targets`
- `/agent/v1/enroll`, `/connect`, `/artifacts`, `/secrets`

Errors use `{ "error": { "code", "message", "requestId" } }`. Profile activation conflicts additionally include `issues`, `skipped`, `nodeName`, and `secretKeys`. Hermes import and MCP secret-edit conflicts may include `envKeys`, `headerKeys`, `targets`, source generations/hashes, or import `status`; every such field contains identifiers or key names only. List responses use `{ "items": [...] }`. Agent WSS messages are typed envelopes; task signatures cover ID, kind, and canonical payload.

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

- `GET /discoveries` returns runtime-local Skill discoveries, one canonical `shared` discovery per importable shared Skill, and MCP runtime bindings. Hermes rows add `controlMode: "read_only_source"`, `sourceChanged`, and `importStatus`; MCP rows also expose `observedGeneration`, `envKeys`, and `headerKeys`. It never returns secret IDs, secret values, or per-node HMAC fingerprints.
- Codex/Claude MCP entries can be captured through the Agent-only protocol during first-seen baseline creation. Ordinary Hermes inventory creates or updates candidates only: it does not create a central MCP server, deployment, Profile, or encrypted secret.
- Initial mcpm discovery creates the fixed `toolhub-codex` and `toolhub-claude` profiles. Legacy `all-mcp` membership follows recognized Codex/Claude native anchors; only an ambiguous registry with no anchor evidence retains the both-runtime compatibility fallback. Deployments start as `observed`; only `POST /mcp/deployments` advances a selected node/runtime to `pending` and queues `mcp_sync`.
- `PUT /mcp/profiles/{id}/servers` replaces membership only for those fixed managed profiles. Membership edits refresh desired hashes/bindings but preserve `observed` state until the matching runtime is explicitly deployed.
- Unique legacy shared-manifest MCP entries are imported as disabled `shared-import` candidates. When the same node already exposes an equivalent non-shared server with the same runtime name, transport, command, arguments, and URL, the shared duplicate is suppressed or archived; same-name entries with different endpoints remain reviewable candidates. The browser shows import provenance and conflict state.
- `POST /discoveries/{id}/adopt-skill` is administrator-only and rejects Hermes. For a shared-source discovery the Agent uploads a safely packaged immutable snapshot without writing the legacy tree. The resulting Skill remains pending review, then deploys as a materialized copy through ordinary per-runtime targets.
- `POST /discoveries/{id}/import-skill` is administrator-only and accepts `{expectedSha256}` for a Hermes discovery. It queues a signed read-only snapshot task; the Agent never writes `.toolhub-managed.json`. A first import creates pending review. Re-import replaces the latest unapproved version or creates an ordinary `updates` candidate when the Skill is already approved, so desired state never changes before approval.
- `POST /discoveries/{id}/import-mcp` is administrator-only and accepts `{observedGeneration, confirmSecrets}`. A secret-bearing candidate first returns `409 secret_confirmation_required` with `envKeys`/`headerKeys`; confirmation authorizes one generation-pinned inventory scan and one-time capture for that candidate only. Each successful re-import creates a new enabled `hermes-import` server and leaves every existing server, Profile, and deployment unchanged.
- Hermes import conflicts use `source_changed`, `secret_confirmation_required`, `import_in_progress`, or `agent_upgrade_required`. The two import endpoints require the Agent capability `hermes_read_only_import_v1` published by a fresh inventory.
- `POST /deployments/{id}/rollback` swaps to the recorded previous approved version. For a first successful deployment with no previous version, rollback advances desired state to disabled and the Agent safely removes the managed directory; a later target update can re-enable the retained version.
- `GET /shared-sources` and `GET /shared-sources/{id}` expose redacted node-local source state for discovery/import review only. There is no shared-source sync or targets writer API.
- A shared Skill whose package scan fails carries the reason in `lastError`; it remains blocked on its own discovery row without blocking import of healthy siblings.
- `POST /reconcile` queues scoped `sync` and `mcp_sync` jobs. Accepted selector fields are `nodeIds`, `skillIds`, `profileIds`, and `mcpDeploymentIds`.
- `202 Accepted` and a succeeded Job mean orchestration/dispatch completed. Actual deployment state changes only after the corresponding Agent task succeeds.

## MCP delivery and legacy imports

- PostgreSQL is authoritative. `mcp_sync` sends a signed `apply_mcp` task whose payload names the fixed mcpm profile and contains only encrypted secret references. The Agent resolves authorized values, atomically updates `~/.config/mcpm/servers.json` at mode `0600`, then repairs the runtime's single native mcpm anchor. The Codex anchor uses a managed 60-second startup timeout and drift detection includes that timeout.
- Managed delivery accepts only the exact fixed mapping: `toolhub-codex` to Codex and `toolhub-claude` to Claude. Arbitrary or mismatched profiles return `409` and are excluded from worker dispatch and Agent secret authorization.
- Shared manifest rows remain mirrored with `authority: "shared-file"` and `credentialMode: "node-local"` for read-only observation. Separate `shared-import` candidate rows are ordinary ToolHub-authoritative records after one-time capture; they remain disabled until explicitly reviewed.
- ToolHub-authoritative MCP servers may contain both `env` and `headers` plaintext only in create/capture requests. Values are encrypted into `mcp-env` or `mcp-header` records; list/detail and task payloads contain references only.
- `GET /mcp/servers` returns `envKeys` and `headerKeys`, never the corresponding reference IDs. `PATCH /mcp/servers/{id}` accepts write-only `secretChanges.env|headers.{set,remove}`. A changed value creates a new encrypted record and atomically swaps the server reference; old ciphertext remains historical and is authorized only while an already-issued active `apply_mcp` task still names that reference.
- When secret changes affect desired Codex/Claude targets, the first patch returns `409 secret_confirmation_required` with affected node/runtime and key names. Retry the same patch with `confirmTargets: true`. A server referenced by an active ToolHub Profile instead returns `409 target_managed_by_profile`; deactivate ownership before editing.
- Updating or deleting a mirrored shared-file server, or adding it to a ToolHub profile, returns `409 source_file_authoritative`. Import and edit the separate central candidate instead.
- Header names are validated as HTTP field-name tokens and stored in canonical form. Values are never included in ordinary browser responses, audit metadata, inventory JSON, or logs.

## ToolHub Profiles and runtime targets

- User-defined ToolHub Profiles live at `/profiles`; they are selection sets containing MCP server IDs and Skill IDs. They are distinct from the fixed `toolhub-codex` / `toolhub-claude` delivery channels under `/mcp/profiles`.
- `POST /profiles/{id}/preflight` validates node/runtime availability, approved members, and the fixed delivery channel without writing desired state. A non-local target receiving MCP credentials returns `409 remote_secret_confirmation_required` with the destination node name and secret key names only.
- `POST /profiles/{id}/activate` accepts `{nodeId, runtime, confirmSecrets?}` for Codex, Claude, Grok, or OpenClaw; Hermes is excluded. One Profile owns a `(node, runtime)` target at a time. Activation writes all Skill desired flags and the target-specific MCP hash in one transaction, then a one-attempt `profile_activate` job dispatches existing `deploy_skill` tasks before existing `apply_mcp` tasks. No new native MCP anchor is introduced.
- Activation state (`pending`, `active`, `partial`, or `failed`) records orchestration. As with other Jobs, `active` means all tasks were dispatched, not that every Agent result has completed; desired/actual state in `GET /targets/{nodeId}/{runtime}` remains authoritative for drift.
- Manual Skill target, MCP target, rollback, and fixed MCP membership mutations return `409 target_managed_by_profile` while a target is Profile-owned. `POST /targets/{nodeId}/{runtime}/deactivate` releases ownership but intentionally leaves current desired-state rows unchanged.
- `GET /targets/{nodeId}/{runtime}` aggregates activation ownership, effective MCP membership, Skill desired/actual state, capability notes, and drift. The Hermes view contains observed candidates and import state only, reports zero drift, and exposes no mutation controls. OpenClaw MCP is read-only; Grok explicitly shows the same-node Claude MCP set as inherited read-only state.
- MCP archive/unarchive uses `POST /mcp/servers/{id}/archive|unarchive`. Archived servers remain persisted with provenance, are hidden by default in the UI, cannot join a Profile, and are excluded from effective delivery. A server referenced by an active ToolHub Profile must be deactivated before archive, restore, enable/disable, or delete, which preserves remote-secret confirmation.

The Agent-only descriptor, capture, and Skill upload contracts are documented in [Agent Protocol](AGENT_PROTOCOL.md).
