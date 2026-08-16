# Browser API

The checked contract is [`api/openapi.yaml`](../api/openapi.yaml). Browser
routes use `/api/v1`; `/healthz` is the only non-API HTTP route.

## Authentication

`POST /auth/login` accepts only `username` and `password`. A successful response
sets the HttpOnly `toolhub_session` cookie and returns the account projection,
CSRF token, and session expiry. There are no email identities, roles, or user
administration endpoints.

Every authenticated unsafe method requires `X-CSRF-Token`. JSON decoding is
size-limited, rejects unknown fields, and accepts exactly one object. The common
error shape is:

```json
{
  "error": {
    "code": "revision_conflict",
    "message": "Target changed after preflight",
    "requestId": "..."
  }
}
```

Mutations documented with `Idempotency-Key` require an 8-200 character key.
Reusing a key with a different canonical request returns `409`.

## Library And Profiles

- `GET /skills`, `POST /skills/upload`, and `POST /skills/import` manage
  immutable content-addressed Skill artifacts. Git/SkillsMP/Xiaping imports are
  operations. Import and update discovery never Apply.
- `GET|POST /mcp/servers` and `PUT|DELETE /mcp/servers/{id}` manage the MCP
  Library. Responses expose only `envKeys` and `headerKeys`, never values.
- MCP input secret semantics are write-only: a non-empty value sets/replaces;
  an existing key with an empty value is retained; an omitted key is removed.
- `/profiles` is the only Profile model. Each immutable revision pins exact
  Skill versions and MCP revisions; current Library pointers are used only for
  new Profiles or explicit Refresh.
- Profile list/detail responses and revision history expose the pinned
  `mcpGovernance` and `toolRules` arrays so an editor can round-trip a governed
  revision without weakening or dropping policy. Current Profile responses also
  expose the optional Published revision ID, revision number, and timestamp;
  comparing that pointer with `currentRevisionId` distinguishes a draft from the
  revision active in the Relay. Empty governance and rule collections are `[]`.
- `GET /profiles?includeArchived=true` includes reversible archived Profiles
  after active rows. `POST /profiles/{id}/archive` requires the current
  revision; `POST /profiles/{id}/restore` creates an `archived_restore`
  revision; `POST /profiles/{id}/purge` is irreversible and succeeds only when
  no snapshot, pending operation, or bundle fingerprint still references the
  archived Profile.
- Bundle imports that contain MCP slots expose pending write-only bindings via
  `GET /profiles/{id}/secret-bindings`. Complete them with
  `POST /profiles/{id}/secret-bindings` using `{revision, values}` where
  `values` is keyed by the returned `slotHash`; plaintext values are encrypted
  immediately and never returned.

Unmanaged Skills found in a scanned `local/claude` or `local/codex` target can
be imported with `POST /targets/{id}/skill-import`. The request is bound to the
stored target revision and content hash; the worker rechecks both before
creating an immutable `local` artifact.

Local native MCP intake is a two-step flow. `POST
/targets/{id}/mcp-import/preflight` returns sanitized server fields, secret key
names, and five-minute one-use confirmation tokens. It never returns values.
After explicit browser confirmation, `POST /mcp/import` consumes one token; the
worker captures the still-matching native entry once and encrypts its values
immediately. Salt targets and Hermes MCP intake cannot be Library sources.
Local Hermes Skills use opaque IDs through the capped batch route
`POST /targets/{id}/skill-imports`; items commit independently and a partial
operation exposes failed-only retry data.

Profile Bundles are ZIP uploads. Standard export/import carries no Secret
values; `export-secrets` requires current-password reauthentication and sends
an explicit plaintext backup with `Cache-Control: no-store`. Preview stores
only a short-lived hash-bound token; import re-uploads the same bytes.

`POST /profiles/{id}/preflight` accepts 1-100 unique target IDs including the
Profile's local client target. Before calling any target preflight or issuing a
token, it asks the Bridge to inspect that managed user's fixed Claude/Codex
adapter. Missing, unsafe, invalid, timed-out, or below-floor clients return a
stable `409` reason code and issue no tokens. Multiple approved paths that
resolve to different executable inodes return
`native_client_resolution_ambiguous`; aliases of the same inode are
deduplicated. A successful request resolves current Library revisions and
returns a per-target destructive diff plus a five-minute, one-use confirmation
token. Bridge preflight responses are accepted only when their target and
manifest hashes are valid, the manifest hash matches the server-rendered
manifest, and the bounded diff references exact desired members; an invalid
response returns `502` and issues no token. `POST /profiles/{id}/apply`
atomically consumes the tokens and queues one
fleet operation. A changed Profile, changed target, expired token, reused
token, or mismatched manifest returns `409`.

## Targets And Snapshots

`POST /nodes/refresh` creates a durable `refresh` operation and returns `202`.
The control worker asks the Bridge for the local node and accepted Salt keys,
then persists the successful discovery result as the authoritative active Salt
inventory. Newly accepted keys create the fixed Claude, Codex, and Hermes
Target set. Salt nodes absent from that successful result are soft-archived and
their Targets disappear from active `/nodes`, `/targets`, and direct Target
lookups. Rediscovering the same minion restores the original node and Target
UUIDs, managed-username override, desired snapshot, inventory, backups, and
operation history.

Refresh is asynchronous: clients must poll `/operations/{id}` until the durable
operation is terminal, and reload `/nodes` or `/targets` only after success. A
failed Bridge accepted-key request changes no discovery projection. An empty
successful accepted-key result archives every Salt node but never the local
node or its Targets.

`PATCH /nodes/{id}` sets a Salt node's managed-username override. Sending an
empty `managedUsername` clears the override and makes every target on that node
inherit the global Setting again. Local nodes cannot be overridden. Existing
desired snapshots remain bound to their original username and require an
explicit Profile Apply before destructive work resumes.

`GET /targets/{id}` returns the target projection, latest runtime inventory,
target revision, and optional active immutable desired snapshot. The browser
Target Edit is no longer a Browser capability. Historical `target_edit` rows
remain readable for audit, while new state must come from a Profile Apply or a
verified Restore.

Local targets are:

- `local/claude` and `local/codex`: Skills only.
- `local/shared-relay`: MCP only, including the shared mcpm registry and the
  Claude/Codex/Hermes relay anchors. Its Apply mirrors Hermes' existing
  `mcp_servers` map to the single `toolhub-relay` anchor; reconcile preserves
  later unmanaged Hermes entries while repairing the anchor.
- `local/hermes`: read-only inventory.

Salt nodes expose Claude/Codex writable Skill+MCP targets and read-only Hermes.
Project/local/managed/plugin Claude MCP scopes, protected entries, hidden Skill
entries, `.system`, and ToolHub-reserved names are outside writable scope.

Restore accepts a catalog backup ID and expected target revision. It backs up
current state, restores atomically, scans the restored content, and pins a new
desired snapshot so the next reconcile does not reverse the restore.
When the active snapshot has `sourceKind=restore`,
`POST /targets/{id}/profile-adoption` can create a named Profile from that
verified restored manifest. Hermes remains read-only throughout this flow.

Desired snapshots bind the managed username and, for `local/shared-relay`, the
fixed relay port used when the snapshot was created. Changing either Setting
does not silently redirect an old snapshot into another home or port. Apply,
Restore, and reconcile report `revision_conflict` until an explicit Profile
Apply creates a snapshot with the new binding.

## Operations

All asynchronous work uses `/operations`; there is no separate jobs API.
Operation states are `queued`, `running`, `succeeded`, `partial`, `failed`, and
`cancelled`. Batch intake may also mark its single target step `partial` so
`retry-failed` can carry only failed/stale items.

`GET /operations` is the compact history projection. `GET /operations/{id}`
adds each target step's Bridge operation ID, Salt JID, redacted safe result, and
timestamps. Raw Salt output and secret values are never part of either response.

Only queued target work is cancellable. A destructive target step that reached
`running` completes its atomic terminal transition. `retry-failed` creates a new
operation of the original kind containing only failed targets.

The five-minute scheduler creates reconcile operations for active desired
snapshots. Shared-relay registry/config drift is still checked on that cadence,
while full MCP capability discovery runs every 30 minutes. A blocked relay gets
three durable retries after 5 minutes, 15 minutes, and 1 hour, then becomes
suspended until an explicit Apply, Start, or Restart resets it. One target has at
most one queued/running operation target; overlap is coalesced into one pending
rerun. Update discovery follows the singleton cron and timezone in Settings.
Backup GC is scheduled daily with fixed 30-day/10-per-target retention.

## Relay

`POST /targets/{id}/relay/{start|stop|restart|health}` queues fixed controls for
`local/shared-relay`. Stop persists `relayIntentionalPaused=true`; periodic
reconcile still repairs registry/anchor drift but does not start the service.
Apply/start/restart clear the pause intent and reset suspended relay retries. Restart
uses enable, stop, fixed-port release, and start so MCPM cannot silently fall
back to another port. Target detail exposes only bounded protocol health,
capability counts, stable error codes, and retry timing. Arbitrary unit names are
impossible.

Relay governance separates draft/current state from applied state. `GET
/relay/configuration` and `GET /mcp/policy` return both immutable projections;
their `PUT` routes create a new current revision with optimistic revision
checking and do not Apply it. Relay Configuration revisions pin an ordered set of
exact MCP revisions. Global Policy revisions pin the policy catalog version and
explicit tool decisions.

Relay Configuration Apply is revision-bound. `POST
/relay/configuration/prepare-profile-updates` returns the Profiles affected by a
candidate revision. It creates a candidate only while the current Profile still
matches its Published predecessor, treats an exact repeated preparation as a
no-op, and returns `409` instead of replacing an unrelated current draft. `POST
/relay/configuration/preflight` renders that candidate
with the exact selected current Profile revisions and returns the target revision
and routing hash. `POST /relay/configuration/apply` accepts those values and
queues a durable `relay_config_apply`; finalization advances the applied Relay
revision and the selected Published Profile revisions only if every predecessor
and hash still matches. `POST /mcp/policy/apply` uses the same target-revision
binding for a durable `policy_apply` operation.

Contract governance is exposed through `GET /relay/contracts`, the durable
`POST /relay/contracts/observe`, revision acceptance, and explicit rename
confirmation routes. Contract projections contain normalized definitions and
presentation metadata plus each tool's contract status and decision under the
applied Global Policy. The decision and reason chain come from the same
versioned classifier used to render relay routing; the browser does not
reimplement that classifier. Projections never contain MCP call arguments,
results, prompts, or raw transport errors. The projection returns at most 500
deterministically ordered servers and 500 most-recent rename proposals.
`POST /relay/renames/{proposalID}/confirm` accepts a unique suspected rename or
an explicitly selected ambiguous proposal. Confirming an ambiguous mapping
atomically rejects competing proposals from the same Contract transition that
reuse either selected tool identity, then creates immutable candidate Policy
and Profile revisions; it never Publishes or Applies them.

`GET /relay/confirmations` reads bounded, payload-free, in-memory challenges
from the Relay. Approval requires the exact case-sensitive Profile name and
binding hash; the handler verifies the challenge against the Profile's currently
Published revision and that immutable revision's name before sending only
`{challengeId,bindingHash}` to the Bridge. Saving a later draft does not invalidate
a running Published session, while an unpublished or superseded revision cannot
be approved. Rejection requires the binding hash. Both decisions require an
authenticated session and CSRF token but not a password. Challenges and
60-second one-shot grants are ephemeral and are not durable operations. An
approval response must contain a finite future expiry no more than 60 seconds
away; a rejection response must omit the grant expiry. A successful Relay
decision is not reported as `200` unless its synchronous audit row was
persisted. A transport failure or mismatched decision response returns
`confirmation_outcome_unknown`; clients must not retry or claim the tool was not
executed.

`GET /profiles/{id}/launch` returns a command only when the current Profile is
Published, its native Skill target and shared Relay snapshot are healthy and
revision-matched, the routing hash is current, and the fixed native adapter is
supported. Otherwise it returns a stable reason code and no command.

Observability remains payload-free. `GET /relay/observations/live` validates the
Relay cursor and returns no more than the requested limit, capped at 1,000 safe
observations. `GET
/relay/observations/daily?days=30&profileId=...` reads PostgreSQL aggregates for
the current database day and preceding window, capped at 31 days and 5,000 rows.
Neither surface contains arguments, results, prompts, raw errors, secret values,
or session identifiers.

## Removed Routes

Generation 2 has no Agent/enrollment/WebSocket/SSH, users/roles/access,
deployments/rollback, old jobs, review/approval, MCP delivery Profile,
activation, shared-source ownership, or legacy reconcile routes.
