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
- `PUT /skills/{skillID}/tags` replaces a Skill's normalized tag set (lowercase
  slugs, unique, at most 50). The `required` tag means every non-`shared`
  Profile revision must include that Skill; a new revision that omits a
  required Skill is rejected. Existing revisions remain immutable.
- `GET|POST /mcp/servers` and `PUT|DELETE /mcp/servers/{id}` manage the MCP
  Library. Responses expose only `envKeys`, `headerKeys`, and the applied Relay
  `enabled` projection, never secret values.
- MCP input secret semantics are write-only: a non-empty value sets/replaces;
  an existing key with an empty value is retained; an omitted key is removed.
- MCP create/update also accepts `customJson` in the standard client shape
  `{ "mcpServers": { "name": { "type": "stdio|http|sse", ... } } }`.
  Exactly one entry is accepted; supported fields are `type`, `command`,
  `args`, `url`, `env`, `headers`, and `description`. Unknown fields and
  multiple entries are rejected, and the parsed values use the same command,
  URL, name, and secret validation as the form fields.
- `/profiles` is the only Profile model. Each immutable revision pins exact
  Skill versions; MCP membership, governance, and tool rules are not Profile
  fields. Current Library pointers are used only for new Profiles or explicit
  Skill Refresh. Saving a new revision requires every Library Skill tagged
  `required` (non-`shared` Profiles only). Legacy MCP fields are rejected by
  strict JSON decoding.
- Profile list/detail responses and revision history expose Skill pins only.
  Shared MCP state is returned by `/relay/configuration` and `/targets` relay
  projections. The optional Published revision pointer distinguishes a draft
  Skill revision from the revision last delivered to a target.
- `GET /profiles?includeArchived=true` includes reversible archived Profiles
  after active rows. `POST /profiles/{id}/archive` requires the current
  revision; `POST /profiles/{id}/restore` creates an `archived_restore`
  revision; `POST /profiles/{id}/purge` is irreversible and succeeds only when
  no snapshot, pending operation, or bundle fingerprint still references the
  archived Profile.
- MCP secret slots are completed on the MCP editor itself. Profile Bundle
  imports are Skill-only and never create MCP bindings.

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

Profile Bundles are ZIP uploads containing Skills only. MCP definitions and
write-only secrets are managed and exported from the MCP/Relay Configuration
workflow, never from a Profile. Preview stores only a short-lived hash-bound
token; importing a legacy bundle that contains MCP entries is rejected.

`POST /profiles/{id}/preflight` accepts 1-100 unique Skill target IDs including
the Profile's local client target; `local/shared-relay` is rejected because
relay configuration is independent. Before calling any target preflight or issuing a
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

Skill Profile Apply finalization publishes client Skill target snapshots only
after the selected Skill targets succeed. Relay Configuration Apply is
independent and is the only operation that changes the shared MCP set.

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
  Claude/Codex/Hermes relay anchors. Relay Configuration Apply mirrors the
  enabled set to mcpm; Profile Apply never changes this set.
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

The relay unit runs mcpm in compatibility/pass-through mode and exposes every
enabled member in the applied Relay Configuration. The applied Relay
Configuration is the only ToolHub-side source of the shared member set; Profiles
do not publish routing or call policy.

`POST /targets/{id}/relay/{start|stop|restart|health}` queues fixed controls for
`local/shared-relay`. Stop persists `relayIntentionalPaused=true`; periodic
reconcile still repairs registry/anchor drift but does not start the service.
Apply/start/restart clear the pause intent and reset suspended relay retries. Restart
uses enable, stop, fixed-port release, and start so MCPM cannot silently fall
back to another port. Target detail exposes only bounded protocol health,
capability counts, stable error codes, and retry timing. Arbitrary unit names are
impossible.

Relay Configuration separates draft/current state from applied state. `GET
/relay/configuration` returns both immutable projections; its `PUT` creates a new
current revision with optimistic revision checking and does not apply it. Relay
Configuration revisions pin an ordered set of exact MCP revisions and are
materialized by mcpm through the existing durable apply operation. MCP list/detail
projections expose whether each server is enabled in the applied revision.

The Relay Configuration projection reports current/applied revisions and a
bounded mcpm capability view. A missing governance admin socket is not by itself
an incompatibility in pass-through mode: fixed HTTP/systemd liveness is used and
configured healthy members project as `ready`. Raw Bridge/runtime errors are not
returned.

Relay Configuration Apply is revision-bound. `POST
/relay/configuration/preflight` validates the exact selected MCP revisions and
returns a target revision/hash. `POST /relay/configuration/apply` queues a
durable `relay_config_apply`; finalization advances the applied Relay revision
only if predecessor, target revision, and hash still match. mcpm remains the
sole process owner and ToolHub never exposes a generic mcpm command proxy.

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
executed. The Relay consumes a matching grant before dispatch and never restores
it. A confirmed high-risk call is therefore dispatched at most once; only a
proven pre-dispatch failure is `not_executed`, while post-dispatch ambiguity is
`execution_unknown` and requires state inspection before a manual retry.

`GET /profiles/{id}/launch` returns a command only when the current Skill-only
Profile is published for delivery, its native Skill target and independently
managed shared Relay snapshot are healthy and revision-matched, and the fixed
native adapter is supported. Skill Profile changes do not require a routing
hash match. Otherwise it returns a stable reason code and no command.

Observability remains payload-free. `GET /relay/observations/live` validates the
Relay cursor and returns no more than the requested limit, capped at 1,000 safe
observations. `GET
/relay/observations/daily?days=30&profileId=...` reads PostgreSQL aggregates for
the current database day and preceding window, capped at 31 days and 5,000 rows.
Neither surface contains arguments, results, prompts, raw errors, secret values,
or session identifiers. The in-process Relay ring is bounded to 100,000 entries
and a 24-hour TTL. ToolHub drains it into daily aggregates and removes aggregate
buckets older than 30 days.

## Removed Routes

Generation 2 has no Agent/enrollment/WebSocket/SSH, users/roles/access,
deployments/rollback, old jobs, review/approval, MCP delivery Profile,
activation, shared-source ownership, or legacy reconcile routes.
