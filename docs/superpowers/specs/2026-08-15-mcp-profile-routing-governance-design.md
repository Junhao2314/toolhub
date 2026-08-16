# MCP Profile Routing And Governance Design

Date: 2026-08-15

> Status: the control-plane governance endpoints, revision history, and
> routing-file writes were implemented, but relay routing governance was
> removed from the relay unit on 2026-08-16 (`3cc4fac`): the
> contract-publication flow never worked and an empty bundle hid every tool.
> The running relay is a compatibility pass-through without
> `--toolhub-routing`/`--toolhub-admin-socket`; this document is retained as
> the design record of the removed flow.

## Goal

Keep one persistent local mcpm relay in which every configured upstream MCP
server runs once, while allowing each Claude or Codex task Profile to expose a
smaller, reviewed tool catalog and tighten tool-call policy.

ToolHub remains the control plane for configuration, immutable revisions,
health, contract review, Apply/Reconcile, audit, and aggregate observability.
mcpm remains the runtime data plane for MCP sessions, `tools/list`,
`tools/call`, policy enforcement, one-shot confirmation, and short-retention
runtime observations. ToolHub is never a synchronous dependency for an
ordinary MCP tool call.

This specification is the first of three implementation tracks. Model Gateway
and Agent Profiles will receive separate specifications and implementation
plans after this track is approved.

## Scope

This track adds:

- one global Relay Configuration that replaces the user-facing `shared-mcp`
  infrastructure Profile;
- client-specific task Profiles that combine exact Skill pins with MCP server,
  tool-visibility, and risk-policy pins;
- session-time Profile selection for native Claude and Codex clients;
- filtered `tools/list` and policy-checked `tools/call` in mcpm;
- immutable observed MCP contract revisions and explicit contract review;
- server-level and optional tool-level visibility controls;
- global risk policy plus Profile rules that may only tighten it;
- one-shot confirmation for high-risk calls;
- payload-free relay telemetry and ToolHub aggregates;
- Chinese-first UI wording for the Profile, relay, contract, and confirmation
  workflows.

This track does not add:

- a second relay, per-Profile MCP processes, or per-client upstream processes;
- an Agent runtime, Agent Profile, Model Gateway, or model-provider routing;
- ToolHub-mediated proxying of normal MCP requests;
- automatic acceptance of new or changed tools;
- content logging, prompt logging, argument logging, result logging, or raw
  error persistence;
- remote Salt relay installation or enforcement of local relay policy on
  native remote MCP connections;
- MCP elicitation as a required client capability.

## Existing Invariants Preserved

- `local/shared-relay` remains the only local MCP runtime target.
- Claude and Codex share one fixed relay endpoint and one mcpm runtime.
- Local Claude/Codex Skill targets remain runtime-specific.
- Profile revisions, MCP Config revisions, desired snapshots, and accepted
  contract revisions are immutable.
- Library updates and contract observations never Apply automatically.
- Every host write creates a backup first; no-op reconcile creates none.
- One target has at most one queued/running operation and at most one coalesced
  reconcile rerun.
- Bridge methods remain typed and path-authoritative. No generic command,
  arbitrary unit, arbitrary path, or second operation queue is introduced.
- Secrets remain encrypted in PostgreSQL, referenced by UUID, and ephemeral at
  the worker-to-Bridge boundary. BoltDB and telemetry never contain plaintext
  secrets or MCP payloads.

## Ownership Boundary

```text
Configuration and observation plane

Browser
  -> ToolHub API and PostgreSQL revisions
  -> durable operation worker
  -> typed HMAC Bridge request
  -> atomic relay bundle and fixed systemd control
  -> health, configuration consistency, aggregate metrics, audit

Runtime data plane

Claude or Codex native MCP client
  -> shared mcpm relay
  -> bind one Published Profile Revision
  -> filtered tools/list
  -> policy check and optional one-shot confirmation
  -> exactly one configured upstream MCP server process
```

ToolHub owns:

- Relay Configuration and Profile editing;
- immutable revision history and Secret bindings;
- Preflight, Apply, Restore, Reconcile, backups, and health projection;
- observed-contract review and rename confirmation;
- global policy configuration;
- confirmation UI and payload-free audit records;
- 30-day aggregate observability.

mcpm owns:

- upstream process lifecycle after a Relay Configuration is Applied;
- session binding and `tools/list` filtering;
- call-time policy evaluation and deny/confirm/allow enforcement;
- pending confirmation challenges and one-shot grants;
- high-risk no-retry behavior and ambiguous-result classification;
- at most 24 hours of payload-free runtime observations.

The Bridge owns only guarded host delivery and fixed relay controls. It does
not interpret tool policy or become an MCP proxy.

## Domain Model

### MCP Config Revision

An MCP Config Revision is the immutable launch or connection configuration for
one server: transport, command or URL, arguments, Secret slots, and integrity
hash. It is not inferred from an upstream package version and is independent
of the tools currently observed from the server.

An optional upstream version string may be displayed as provenance, but it is
never used as the revision identity or as proof that the tool contract is
unchanged.

### Observed Contract Revision

An Observed Contract Revision is an immutable canonical observation of one
running server's tool names, input/output schemas when available, annotations,
and presentation metadata. ToolHub stores the normalized contract and its
hash, not a sample request or response.

Each MCP server has:

- a latest observed contract revision;
- an accepted contract revision used by Profiles and runtime policy;
- a review state when the two differ.

Observation never advances the accepted pointer automatically.

### Relay Configuration Revision

The Relay Configuration Revision is the immutable global desired set of MCP
Config Revisions that the single shared runtime starts. All Applied task
Profiles must reference the same Config Revision for a given server. A
Preflight conflict is raised if two candidate Published Profile Revisions
would require different Config Revisions for one process.

The existing `shared-mcp` Profile is migrated once into this control-plane
object. It no longer appears beside user task Profiles. Its members and Secret
bindings are preserved, and the Relay page becomes the place to edit, inspect,
Preflight, and Apply the runtime set.

### Task Profile And Published Profile Revision

A task Profile has a stable ID, client kind (`claude` or `codex`), category,
display variant, exact Skill version pins, exact MCP Config Revision pins, exact
accepted Contract Revision pins, Tool Visibility, and risk-tightening rules.

Internal names remain stable, for example `claude-coding` and `codex-coding`.
The UI groups them under a translated category such as `编程` and shows the
client and variant as separate fields instead of encoding the entire hierarchy
in one visible prefix.

Saving creates a Profile Revision but does not expose it to sessions. Apply
publishes an exact revision into the relay routing bundle. A stable Profile ID
always resolves to the latest successfully Published Profile Revision, never
to an un-applied draft.

Many Profiles may be published simultaneously. Applying one Profile replaces
only that Profile's published pointer and preserves every other published
Profile.

### Tool Identity And Visibility

A tool identity is scoped by stable MCP server ID and observed tool name. The
relay's client-visible namespace remains deterministic and collision-free.

Visibility is configured first at server level:

- `显示全部已确认工具`;
- `只显示所选工具`;
- `全部隐藏`.

The server row is the primary control, so a large server such as ACEMCP can be
enabled or hidden as one unit. Expanding the row reveals optional per-tool
overrides. Server-level changes update all tools except explicit overrides;
the UI always shows the resulting effective count before save.

`tools/list` returns only tools that are:

- in the bound Published Profile Revision;
- present in its accepted Contract Revision;
- visible under the effective server/tool rule;
- not paused by a later incompatible contract observation;
- not denied by the effective global-plus-Profile policy.

Visibility is not authorization. `tools/call` always evaluates policy again.

## Relay Runtime And Session Binding

All MCP servers in the Applied Relay Configuration start once and remain
available for reuse. Hiding a server or tool in a Profile never starts, stops,
or restarts its process. Opening more Claude or Codex sessions adds relay
connections, not duplicate upstream processes.

The relay receives one atomically replaced routing bundle containing:

- Relay Configuration revision and hash;
- every Published Profile ID and exact revision;
- accepted Contract revisions and normalized tool identities;
- Tool Visibility and effective risk-policy inputs;
- the explicit Default Profile ID, if configured;
- no plaintext Secret and no Skill content.

At MCP session initialization:

1. The client supplies a stable Profile ID through its native MCP endpoint
   configuration.
2. The relay resolves that ID against the currently loaded routing bundle.
3. The session binds the exact Published Profile Revision for its lifetime.
4. A later Profile Apply affects new sessions only.

If no Profile ID is supplied, the relay uses the explicitly configured Default
Profile. If no Default Profile exists, initialization succeeds with an empty
tool catalog. An unknown, archived, unpublished, or client-incompatible Profile
ID fails clearly and never falls back to the Default Profile.

Archiving blocks new sessions. Existing in-memory sessions may finish against
their bound revision until disconnect, except that a newly tightened global
deny rule or a newly observed incompatible schema takes effect immediately.

Relay Configuration changes are different from Profile-only changes. Because
there is exactly one process per MCP server, a Config Revision replacement or
server removal performs a guarded relay restart, disconnects current sessions,
and requires clients to reconnect. Preflight must show that impact.

## Native Client Workflow

ToolHub does not invent a launcher or wrap the Claude/Codex process. After a
successful Apply, the Profile detail page exposes `启动会话` as the primary
action. It renders the client-native invocation or configuration override for
the Profile's client kind.

The workflow is:

```text
保存
  -> 应用前检查
  -> 应用配置
  -> 启动会话
```

`复制启动命令` is a secondary button inside the successful Apply result and
Profile detail. The UI describes it as copying the native Claude or Codex
startup command for this configuration, not as applying the Profile. Exact CLI
syntax is generated by a versioned client adapter and covered by integration
tests; unsupported client versions fail Preflight instead of emitting an
unverified command.

ToolHub may also display the generated native configuration fragment, but it
does not rewrite unrelated client settings during session launch.

## Contract Observation And Update Review

Contract observation runs during explicit relay health checks, after Relay
Configuration changes, and on the existing bounded full-member cadence. It is
not a per-call probe and never sends a synthetic tool invocation.

When the latest observation differs from the accepted contract:

- a new tool is `新工具，暂未显示` and remains hidden;
- a removed tool is unavailable but its historical visibility and policy are
  retained for audit and possible rename inheritance;
- an input/output schema or safety-relevant annotation change pauses that tool;
- a presentation-only change is shown for review but does not lower policy;
- unchanged tools continue to serve from the accepted contract.

If exactly one removed tool and one added tool on the same MCP server have the
same canonical schema, ToolHub marks the pair `疑似改名`. It does not accept the
rename automatically. One-click confirmation makes the new tool inherit the
old tool's server/tool visibility and every explicit global and Profile policy
assignment, then recomputes the effective decision under the current ceiling
and advances the accepted contract. If several tools share the schema or the
pairing is otherwise ambiguous, the user must select the mapping explicitly.

Confirming a contract without confirming a proposed rename keeps the new tool
hidden. A schema match never proves semantic equivalence and never inherits
permission by itself.

### Updating An MCP Config

Editing or importing an MCP server creates a new MCP Config Revision. The Relay
page shows the affected Published Profiles before Apply. The recommended
one-click update action creates candidate Profile Revisions that advance only
that MCP Config pin while preserving Tool Visibility and risk rules, then runs
one combined Preflight.

Nothing advances in the background. On success, the operation atomically
replaces the relay files, restarts the affected runtime, verifies health, and
only then advances the Relay Configuration and Published Profile pointers. On
failure, the old runtime files and published pointers remain active; candidate
immutable revisions may remain in history but are not current.

## Risk Policy

### Decision Lattice

Every tool call resolves to one of:

```text
allow < confirm < deny
```

The global policy is the permission ceiling. A Profile may move a tool only to
the right, for example `allow -> confirm` or `confirm -> deny`. A Profile can
never lower a global decision. A newly tightened global rule invalidates
outstanding confirmation grants and applies to existing sessions immediately
after its immutable policy revision is successfully Applied to the routing
bundle. Saving a policy draft has no runtime effect.

### Classification Inputs

The default global policy uses deterministic, reviewable inputs:

1. an explicit global server/tool override;
2. accepted safety annotations and schema markers;
3. ToolHub's versioned rule catalog for destructive writes, command execution,
   privilege or credential changes, external publication, financial actions,
   and other irreversible side effects;
4. a conservative fallback when the tool cannot be classified.

Tool name or prose description alone may raise risk for review but may not
lower it. Upstream annotations may raise the decision and may not override an
explicit ToolHub ceiling. Unclassified mutating tools default to `confirm`.
Reviewed read-only tools may default to `allow`. New tools are hidden regardless
of their preliminary classification.

The UI shows the reason chain behind the effective decision so users can see
whether it came from the global rule, contract annotation, or Profile
tightening.

## One-Shot Confirmation

High-risk confirmation does not require MCP elicitation support. The first
attempt is not sent upstream. mcpm returns a structured `confirmation_required`
MCP error containing a bounded challenge ID and directs the user to ToolHub.

The pending challenge binds:

- exact tool identity;
- Published Profile Revision;
- MCP Config Revision;
- accepted Contract Revision;
- global policy revision/effective decision;
- canonical SHA-256 of the complete arguments;
- a five-minute challenge expiry.

Argument JSON is canonicalized with RFC 8785 JSON Canonicalization Scheme before
SHA-256. After the unconfirmed first attempt is hashed, its raw arguments are
not retained. mcpm may retain only a bounded, schema-aware redacted display
summary in memory until expiry. Raw arguments, results, prompts, raw upstream
errors, and persistent session identifiers are never stored or returned through
the ToolHub API.

The ToolHub confirmation dialog shows the Profile, MCP server, tool, risk
reason, argument hash, and the safe redacted summary. Confirmation does not ask
for the account password, but it still requires the existing authenticated
ToolHub session and CSRF protection. The user must type the exact Profile name.
Approval creates a 60-second grant for the exact binding above.

Challenge IDs are unguessable and usable only through the local authenticated
control boundary. mcpm bounds pending challenges per Profile and globally,
rate-limits creation, and evicts expired entries so an untrusted MCP client
cannot grow memory without limit.

The client must issue the same call again. mcpm recomputes the canonical
argument hash, checks every bound revision, atomically consumes the grant before
dispatch, and sends the call upstream at most once. A changed argument, changed
revision, expired grant, second attempt, or different tool requires a new
confirmation.

Confirmation approval is a control operation through ToolHub and the typed
Bridge/relay management boundary. ToolHub is not in the synchronous upstream
tool-call path.

### High-Risk Result Semantics

High-risk calls are never automatically retried, including connection setup
failure, relay restart, client reconnect, or upstream timeout.

- A proven pre-dispatch failure is shown as `未执行`; another attempt requires a
  new confirmation.
- A normal upstream result is returned once.
- A timeout, disconnect, or transport failure after dispatch where completion
  cannot be proven is shown as `执行结果未知`.

For `执行结果未知`, the UI instructs the user to inspect actual state before
creating and confirming a new attempt. The consumed grant is never restored.

## Apply, Reconcile, And Failure Boundaries

### Profile Apply

Profile Apply is a durable multi-target operation:

1. resolve the exact draft Profile Revision;
2. verify client kind, Secret bindings, Relay Configuration compatibility,
   accepted contracts, visibility rules, and global policy ceiling;
3. Preflight the runtime-specific Skill target;
4. render the complete relay routing bundle while preserving other Published
   Profiles;
5. Apply the Skill projection first;
6. atomically publish the relay routing bundle;
7. mark the Profile Revision published only after both projections succeed.

The Profile remains unavailable to `启动会话` until the whole operation
succeeds. If Skill Apply succeeds but relay publication fails, the operation is
`partial`; the previous Published Profile Revision remains active and retry
targets only the failed relay step. Existing sessions continue on the old
revision.

A Profile that references a server or MCP Config Revision absent from the
Applied Relay Configuration fails Preflight. Hidden tools and hidden servers do
not bypass this revision-consistency check.

### Relay Configuration Apply

Relay Configuration Apply backs up and atomically replaces the mcpm registry,
relay environment, native client anchors, and routing bundle. Process restart
and member health validation occur before the desired pointer advances. A
failed restart restores the prior files and keeps the prior desired revision.

### Reconcile

Reconcile checks:

- Relay Configuration and routing-bundle hashes;
- required native Claude/Codex anchors;
- fixed unit and fixed port health;
- Published Profile revision presence;
- accepted versus latest observed contract state.

Reconcile repairs pinned configuration and preserves later unmanaged content
under the existing rules. It never accepts a new contract, advances a Profile,
changes visibility, or lowers risk. Contract differences are reported as
`工具定义有变化`, not repaired as file drift.

## Persistence And API Boundaries

Use the next numbered PostgreSQL migration; do not rewrite
`001_initial.sql`. The detailed schema may follow local naming conventions but
must model these durable records explicitly:

- Relay Configuration heads and immutable revisions;
- Profile client/category metadata and immutable per-revision MCP visibility
  and risk rules;
- Published Profile pointers;
- immutable observed Contract revisions and accepted pointers;
- stable contract-tool records and confirmed rename relationships;
- immutable global policy revisions;
- payload-free daily telemetry aggregates;
- payload-free audit events for contract acceptance and confirmation decisions.

Pending challenges and one-shot grants are not PostgreSQL records. They live in
the relay's bounded short-retention state and contain no raw payload. A restart
invalidates them safely.

Browser APIs manage configuration, Preflight/Apply, contract review, aggregate
queries, and confirmation decisions. Private Bridge APIs carry typed relay
bundle, observation, health, aggregate, and confirmation-control DTOs. Browser
and Bridge OpenAPI documents must match every new route and DTO.

No SQL belongs in handlers. Store transactions own revision creation,
optimistic concurrency, pointer advancement, and audit writes.

## Observability And Retention

mcpm retains at most 24 hours of payload-free runtime observations needed for
live UI and aggregation:

- time bucket;
- Profile ID and exact revision;
- MCP server and tool stable identity;
- allow/confirm/deny decision and reason code;
- confirmed, rejected, expired, executed, failed, or unknown outcome;
- duration and bounded transport/error class;
- no arguments, results, prompts, raw errors, Secret values, or persistent
  session ID.

ToolHub periodically pulls and idempotently aggregates these observations into
30-day daily buckets. Aggregate rows contain counts, latency summaries, and
bounded outcome/error classes only. Raw relay observations expire even when an
aggregate pull fails. The UI clearly distinguishes `未验证`, `正常`,
`配置不一致`, `工具定义有变化`, `已暂停`, and `不可用`.

The English implementation terms remain in API enums and developer details,
but user-facing Chinese avoids unexplained words such as `drift`. The preferred
label is `配置不一致`; `Contract Revision` is displayed as `工具定义版本` and
`Risk Policy` as `调用规则`.

## UI Information Architecture

### Profiles

The Profile editor is one cohesive workflow, not separate Skill and MCP
Profile types:

- `基本信息`: client, category, variant, description;
- `Skills`: exact pinned versions and replacement semantics;
- `MCP 工具`: server-level visibility with expandable tool overrides;
- `调用规则`: effective global decision plus Profile tightening;
- `应用状态`: draft revision, Published revision, target consistency, and
  `应用前检查` / `应用配置` / `启动会话` actions.

No nested cards are required. Dense tables, expandable rows, tabs, toggles,
checkboxes, and explicit status labels support repeated operational use.

### Shared Relay

The Relay page owns:

- the global running MCP set and exact Config revisions;
- one process/status row per upstream server;
- Relay Configuration Preflight and Apply;
- latest versus accepted tool definition versions;
- new/changed/removed tools and suspected rename review;
- affected Published Profiles before an update;
- live payload-free outcomes and 30-day aggregates;
- pending high-risk confirmations.

The primary runtime update action is `应用中继更新`. Background discovery may
prepare an update and affected-Profile preview, but it may not Apply it.

## Compatibility And Migration

1. Add the new schema without rewriting generation-2 history.
2. Import the current `shared-mcp` revision as Relay Configuration revision 1,
   preserving exact MCP Config revisions and Secret bindings.
3. Add explicit client kind and category metadata to existing ordinary
   Profiles using deterministic migration rules from their existing names;
   ambiguous names require review and remain unpublishable.
4. Seed each existing ordinary Profile with every server in Relay Configuration
   revision 1, pin the first accepted observed contract, and use server-level
   `显示全部已确认工具`. This preserves the pre-migration visible catalog until
   the user deliberately reduces it.
5. Observe and review current relay contracts before enabling filtered routing.
6. Publish routing in compatibility mode, validate native Claude and Codex
   clients, then enable enforcement.
7. Remove `shared-mcp` from the ordinary Profile list only after the Relay page
   can fully manage and Restore its configuration.

Current remote Salt Claude/Codex MCP Apply remains unchanged in this track.
Tool Visibility and risk enforcement are explicitly shown as local shared-relay
capabilities and are not claimed for remote native MCP connections. Extending
the relay to Salt minions requires a separate design.

## Validation Strategy

### Store And API

- migration tests on a fresh generation-2 database;
- immutable revision and optimistic-concurrency tests;
- no silent Profile, accepted-contract, or Relay Configuration advancement;
- Browser/Bridge OpenAPI parsing and handler contract tests;
- payload-redaction tests for every response, audit record, and aggregate.

### Relay

- multiple Claude/Codex sessions share one upstream process per server;
- Profile A and Profile B receive different `tools/list` results without a
  process restart;
- server-level hide and selected-tool overrides produce stable counts;
- invalid Profile IDs never fall back; no Default produces an empty catalog;
- an existing session stays on its bound Profile Revision after Profile Apply;
- global deny tightening takes effect immediately;
- every `tools/call` is checked even if the client calls a hidden tool directly;
- pending grants expire, are one-use, and fail closed after restart;
- exact argument hashing is canonical and rejects any changed argument;
- high-risk dispatch occurs at most once and ambiguous timeout returns
  `执行结果未知`;
- runtime and durable stores contain no raw payload or persistent session ID.

### Contract Governance

- new tools are hidden;
- changed schemas pause only affected tools;
- unchanged accepted tools remain usable;
- unique same-schema add/remove pairs become `疑似改名`;
- no policy is inherited before explicit rename confirmation;
- ambiguous pairs require manual mapping;
- contract acceptance cannot lower the global policy ceiling.

### Apply And Recovery

- Profile multi-target success and partial failure preserve the prior Published
  pointer until complete;
- Relay Configuration failure restores registry, anchors, environment, routing
  bundle, and desired pointer;
- restart recovery invalidates confirmation grants;
- reconcile repairs hashes without accepting contracts or changing policy;
- no-op reconcile creates no backup;
- existing one-active-operation and coalescing invariants remain enforced.

### Web

- desktop and mobile Profile/Relay workflows;
- Chinese labels fit controls and status surfaces without overlap;
- ACEMCP can be enabled/hidden as one server and expanded for tool overrides;
- confirmation requires the exact Profile name and never requests a password;
- `复制启动命令` appears only after successful Apply and is visually secondary
  to `启动会话`;
- confirmation unknown-result guidance is visible and unambiguous.

Run focused tests first, then the full relevant Go, race, PostgreSQL, web,
OpenAPI, Compose, smoke, and Playwright gates defined in `AGENTS.md`.

## Rollback

Schema additions and immutable revision history remain. Runtime rollback selects
the previous retained Relay Configuration and Published Profile pointers,
performs normal Preflight, backs up current files, and atomically restores the
previous relay bundle and registry.

Rollback does not revive expired confirmation grants, erase contract history,
or rewrite Profile revisions. If filtered routing itself is incompatible with
a client, the compatibility-mode routing bundle can be restored while the
single shared upstream process set remains unchanged.

## Acceptance Criteria

The track is complete when:

- Claude and Codex can each start native sessions bound to an explicit Profile;
- opening additional sessions does not create duplicate upstream MCP processes;
- each Profile exposes only its reviewed tool catalog while every configured
  upstream remains running once;
- server-level and tool-level visibility work together;
- new or changed contracts fail closed until review;
- suspected renames inherit visibility/policy only after one-click confirmation;
- high-risk calls require an exact one-shot confirmation, never auto-retry, and
  report ambiguous completion as `执行结果未知`;
- ToolHub exposes health, configuration consistency, contract changes, and
  payload-free aggregates without handling normal tool payloads;
- `shared-mcp` has been replaced in the ordinary UI by Relay Configuration
  without losing its desired state, Secret bindings, backup, or Restore path;
- remote behavior is not weakened or represented as locally enforced.
