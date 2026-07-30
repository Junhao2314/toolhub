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
- `/profiles` is the only Profile model. Membership contains Skill IDs and MCP
  server IDs and does not pin versions.

Unmanaged Skills found in a scanned `local/claude` or `local/codex` target can
be imported with `POST /targets/{id}/skill-import`. The request is bound to the
stored target revision and content hash; the worker rechecks both before
creating an immutable `local` artifact.

Local native MCP intake is a two-step flow. `POST
/targets/{id}/mcp-import/preflight` returns sanitized server fields, secret key
names, and five-minute one-use confirmation tokens. It never returns values.
After explicit browser confirmation, `POST /mcp/import` consumes one token; the
worker captures the still-matching native entry once and encrypts its values
immediately. Salt targets and Hermes cannot be Library intake sources.

`POST /profiles/{id}/preflight` accepts 1-100 target IDs. It resolves current
Library revisions and returns a per-target destructive diff plus a five-minute,
one-use confirmation token. `POST /profiles/{id}/apply` atomically consumes the
tokens and queues one fleet operation. A changed Profile, changed target,
expired token, reused token, or mismatched manifest returns `409`.

## Targets And Snapshots

`POST /nodes/refresh` creates a durable `refresh` operation and returns `202`.
The control worker asks the Bridge for the local node and accepted Salt keys,
then persists the discovery result; clients observe completion through
`/operations` and reload `/nodes` or `/targets`.

`PATCH /nodes/{id}` sets a Salt node's managed-username override. Sending an
empty `managedUsername` clears the override and makes every target on that node
inherit the global Setting again. Local nodes cannot be overridden. Existing
desired snapshots remain bound to their original username and require an
explicit Apply or target edit before destructive work resumes.

`GET /targets/{id}` returns the target projection, latest runtime inventory,
target revision, and optional active immutable desired snapshot. The browser
never submits a raw manifest to target edit: `POST /targets/{id}/edit` accepts
Skill/MCP IDs and the observed target revision; the server resolves and validates
the canonical manifest.

Local targets are:

- `local/claude` and `local/codex`: Skills only.
- `local/shared-relay`: MCP only, including the shared mcpm registry and the
  Claude/Codex relay anchors.
- `local/hermes`: read-only inventory.

Salt nodes expose Claude/Codex writable Skill+MCP targets and read-only Hermes.
Project/local/managed/plugin Claude MCP scopes, protected entries, hidden Skill
entries, `.system`, and ToolHub-reserved names are outside writable scope.

Restore accepts a catalog backup ID and expected target revision. It backs up
current state, restores atomically, scans the restored content, and pins a new
desired snapshot so the next reconcile does not reverse the restore.

Desired snapshots bind the managed username and, for `local/shared-relay`, the
fixed relay port used when the snapshot was created. Changing either Setting
does not silently redirect an old snapshot into another home or port. Apply,
edit, Restore, and reconcile report `revision_conflict` until an explicit
Profile Apply or target edit creates a snapshot with the new binding.

## Operations

All asynchronous work uses `/operations`; there is no separate jobs API.
Operation states are `queued`, `running`, `succeeded`, `partial`, `failed`, and
`cancelled`. Per-target states omit `partial`.

`GET /operations` is the compact history projection. `GET /operations/{id}`
adds each target step's Bridge operation ID, Salt JID, redacted safe result, and
timestamps. Raw Salt output and secret values are never part of either response.

Only queued target work is cancellable. A destructive target step that reached
`running` completes its atomic terminal transition. `retry-failed` creates a new
operation of the original kind containing only failed targets.

The five-minute scheduler creates reconcile operations for active desired
snapshots. One target has at most one queued/running operation target; overlap is
coalesced into one pending rerun. Update discovery follows the singleton cron and
timezone in Settings. Backup GC is scheduled daily with fixed 30-day/10-per-target
retention.

## Relay

`POST /targets/{id}/relay/{start|stop|restart|health}` queues fixed controls for
`local/shared-relay`. Stop persists `relayIntentionalPaused=true`; periodic
reconcile still repairs registry/anchor drift but does not start the service.
Start/restart clear the pause intent. Arbitrary unit names are impossible.

## Removed Routes

Generation 2 has no Agent/enrollment/WebSocket/SSH, users/roles/access,
deployments/rollback, old jobs, review/approval, MCP delivery Profile,
activation, shared-source ownership, or legacy reconcile routes.
