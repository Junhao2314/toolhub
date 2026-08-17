# ToolHub / mcpm MCP ownership and Skill-only Profile refactor

Date: 2026-08-17

## Goal

Make ToolHub's browser the authoritative configuration workflow for the shared
MCP service while keeping mcpm as the sole relay runtime/process owner. A user
must be able to add, edit, enable, disable, or remove an MCP from ToolHub; the
result must be delivered to mcpm through the existing typed Bridge path and the
shared relay must report the actual per-member runtime state. Profiles must
contain Skills only.

The stale `shared-mcp` Profile/governance path must stop influencing relay
health, Profile editing, Profile Apply, or startup. Requirements and operator
docs must describe the new ownership boundary.

## Baseline and evidence

- Current branch is clean at `56a3fa9` and the database is generation 2 with
  migrations through `012`.
- Current ordinary Profiles already have zero MCP members; the legacy
  `shared-mcp` Profile still owns nine ToolHub MCP records (six currently
  configured in mcpm plus `desktop-commander`, `memory`, and
  `sequential-thinking`).
- `toolhub-mcpm-relay.service` is active and mcpm 2.15.0 serves the `toolhub`
  profile. The control plane currently projects all relay members as
  `mcpm_incompatible` because its full probe still requires the removed
  governance contract/admin routing bundle.
- Library cleanup candidates with no reference from any active Profile head are
  the ten archived Skills: `baoyu-format-markdown`, `baoyu-translate`,
  `baoyu-url-to-markdown`, `codex-build`, `codex-review`, `grill-me-codex`,
  `grill-with-docs-codex`, `slides`, `using-superpowers`, and
  `workflow-runner`. They still occur in historical Profile revisions.
- Live MCP/Skill deletion is persistent-state mutation. Before executing the
  purge, take a database/host backup and obtain scoped confirmation for the
  exact MCP names, Skill slugs, audit/history rows, and host cache/process
  cleanup listed below.

## Requirement Ready Check

- Requirement source: user clarification on 2026-08-17.
- Goal: ToolHub web edits shared MCP configuration; mcpm owns runtime;
  Profiles manage Skills only; update requirements docs.
- Required behavior: add/edit/enable/disable/delete MCP; durable delivery;
  accurate relay member status; no Profile MCP membership.
- Acceptance criteria: listed in Verification and Data cleanup sections.
- Decision resolved: the user confirmed the exact destructive live-data and
  host cleanup scope on 2026-08-17. The purge ran only after a restorable
  database dump and dry-run counts were recorded.

## Architecture decision

### Canonical owner

- ToolHub owns the declarative MCP configuration exposed by the browser:
  immutable MCP revisions, encrypted secret references, and the enabled set
  represented by the current/applied Relay Configuration revision.
- mcpm owns the materialized registry, upstream process lifecycle, relay HTTP
  endpoint, and per-server runtime health. ToolHub never becomes an MCP proxy
  and never infers process health from a stale Profile manifest.
- The existing `relay_configuration_revisions` / `relay_configuration_state`
  owner is retained and becomes the only ToolHub-side desired MCP set. The
  legacy `shared-mcp` Profile is removed from ordinary Profile semantics and
  is not used as a relay input.

### Profile contract

- `Profile`, `ProfileRevision`, `ProfileInput`, canonical hashes, bundle
  import/export, preflight/apply manifests, and the web Profile editor expose
  Skills only.
- Profile Apply creates Skill target work only. It never schedules relay
  configuration work and never waits on relay governance finalization.
- MCP visibility/policy/routing governance, published Profile routing, and
  payload-free MCP confirmation/telemetry paths are retired unless a later
  approved requirement explicitly reintroduces them; no compatibility fallback
  is added.

### MCP editing workflow

- Keep the existing MCP Library CRUD and write-only secret semantics.
- Add an explicit enabled/disabled projection to the MCP/Relay configuration
  response (or equivalent membership state in the Relay Configuration editor).
- A browser save/delete/enable/disable operation creates one durable
  `relay_config_apply` operation through the existing store/worker/Bridge path.
  The browser never selects a Bridge route or sends arbitrary mcpm commands.
- The Bridge writes only the fixed mcpm registry/managed client anchors and
  controls the fixed systemd unit. mcpm remains responsible for starting,
  stopping, and reusing exactly one upstream process per enabled server.

### Runtime health contract

- Add a small fixed mcpm health/status contract that is available in
  compatibility/pass-through mode and does not require routing governance.
  It returns the configured member name/id, enabled state, lifecycle state,
  capability counts when known, and a bounded error class.
- Relay status projection uses this contract plus the fixed HTTP/systemd
  checks. A healthy mcpm process with healthy members cannot be rewritten to
  `unavailable` merely because the retired routing bundle is absent.
- Disabled members are shown as disabled/configured-off, not as process
  failures; enabled members are `ready` or `unavailable` according to mcpm.

## Compatibility boundary

- Add a new numbered generation-2 migration; never rewrite `001_initial.sql`
  or an already-applied migration.
- Existing MCP revisions and encrypted Secret records remain usable until the
  explicit purge is executed. Existing relay configuration state is migrated
  from the current `shared-mcp` MCP set once, then future writes use the
  Relay Configuration owner directly.
- Existing Skill-only Profile heads remain semantically valid. Historical
  Profile MCP rows/governance data are either removed by the approved purge or
  explicitly quarantined outside the active model; they must not be read by
  new code.
- Existing target snapshots containing legacy MCP manifests are treated as
  historical records during migration. New Skill Apply snapshots contain no
  MCP members; relay configuration snapshots are owned by the relay config
  operation.

## File map and implementation tasks

### 1. Contract/domain/store split

Files: `internal/domain/models.go`, `internal/store/profiles.go`,
`internal/store/library.go`, `internal/store/relay_governance.go`,
`internal/store/snapshots.go`, `internal/store/operations.go`, relevant store
tests.

- Remove MCP pins/governance/tool rules from the active Profile input/output and
  canonical Profile hash while preserving Skill pin immutability and required
  Skill validation.
- Make relay configuration creation/update/delete/enable/disable operate on
  the Relay Configuration revision and current/applied pointers only.
- Ensure idempotency, optimistic revision checks, one active target operation,
  and encrypted write-only Secret handling remain transactional.
- Make all new Profile Apply/Refresh/Bundle paths reject MCP fields rather than
  silently accepting or dropping them.
- Add focused tests for Skill-only revisions, MCP-free manifests, relay config
  membership toggles, stale revision conflicts, and secret redaction.

### 2. PostgreSQL migration and one-time cleanup

Files: `internal/store/migrations/013_*.sql`, `internal/store/db.go`, migration
tests, cleanup/store tests.

- Migrate the current `shared-mcp` MCP set into the Relay Configuration state
  if it is not already the applied set.
- Remove the legacy Profile MCP membership/governance owner from active reads
  and enforce the Skill-only invariant for newly written revisions.
- Provide a transactional, auditable maintenance path for the explicitly
  approved purge. It must remove the three named MCPs and all dependent
  revisions/contracts/relay references/secret records/history that are no
  longer needed, and remove the ten unreferenced Skills including historical
  Profile revision references and now-orphaned artifacts/sources. Immutable
  triggers must be handled by a narrowly scoped migration function, not by
  weakening runtime triggers.
- Delete only related audit/history/operation/snapshot rows approved by the
  cleanup scope; retain account/session/security records and unrelated Skill,
  MCP, Profile, backup, and audit data.
- Add preflight counts and post-cleanup assertions so a rerun is idempotent and
  cannot delete a newly referenced Skill/MCP.

### 3. Bridge/runtime and mcpm health

Files: `internal/bridgeprotocol/types.go`, `internal/bridge/adapter.go`,
`internal/bridge/server.go`, `internal/bridgeclient/client.go`,
`internal/runtime/relay.go`, `internal/runtime/relay_probe.go`,
`internal/runtime/mcpm_admin.go`, `mcpm/src/mcpm/commands/profile/run.py`,
`mcpm/src/mcpm/toolhub/admin.py`, `packaging/systemd/toolhub-mcpm-relay.service`,
packaging tests.

- Keep typed fixed relay configuration mutations and remove any dependency on
  a Profile routing bundle for ordinary pass-through operation.
- Expose the bounded mcpm member-health operation through the existing fixed
  admin socket/contract, without restoring the retired routing-governance
  flags or adding a generic command proxy.
- Replace `unavailableRelayMembers` fallback behavior for compatibility mode
  with actual mcpm member results; preserve bounded errors, HMAC/idempotency,
  journal redaction, fixed paths, and systemd sandboxing.
- Make relay restart/reconcile apply the enabled set atomically, stop removed
  upstreams through mcpm, and verify the fixed port and per-member health.
- Add runtime tests covering healthy members, disabled members, one failed
  member, stale registry/config, mcpm restart, and admin-contract failure.

### 4. Browser API/OpenAPI

Files: `internal/httpapi/api.go`, MCP/relay handlers and tests,
`api/openapi.yaml`, `api/bridge-openapi.yaml`, `docs/API.md`, `docs/BRIDGE.md`,
`web/src/api/client.ts`.

- Keep `/mcp/servers` as the browser configuration surface, extend its
  response/input contract for enabled state, and route every unsafe change
  through the existing CSRF/idempotency/store operation helpers.
- Remove MCP membership/governance fields from Profile request/response schemas
  and reject unknown legacy fields where strict decoding applies.
- Return relay status/member health from the mcpm-backed projection with clear
  enabled/disabled/ready/unavailable semantics.
- Align Browser and Bridge OpenAPI contracts and update API documentation.

### 5. React UI and acceptance flows

Files: `web/src/pages/MCP.tsx`, relay configuration/status components,
`web/src/features/profiles/ProfileGovernanceEditor.tsx`, Profile pages/types,
`web/src/hooks/useData.ts`, `web/src/api/client.ts`, shared UI/types/styles,
`web/e2e/smoke.spec.ts`.

- Make MCP page the complete shared-relay configuration editor: add, edit,
  enable/disable, delete, write-only secrets, operation progress, and current
  mcpm member health.
- Remove Profile MCP tabs, governance/rule editing, MCP pins, and relay
  governance-only launch/confirmation affordances. Keep Skill selection,
  preflight, Apply, and target-specific Skill flows.
- Show disabled members distinctly from unavailable processes and prevent an
  accidental delete while an active relay operation is running.
- Update Playwright fixtures and add desktop/mobile coverage for the full MCP
  lifecycle and Skill-only Profile save/apply.

### 6. Requirements and operator documentation

Files: `CONTEXT.md`, `.builder/architecture.md`, `README.md`,
`docs/API.md`, `docs/BRIDGE.md`, `docs/SECURITY.md`, `docs/DEPLOYMENT.md`,
`docs/ROLLOUT.md`, `docs/CONFIG_MIGRATION.md`, `docs/SALT.md`,
`api/openapi.yaml`, `api/bridge-openapi.yaml`, and a new dated requirements
spec under `docs/superpowers/specs/`.

- Record the canonical boundary: ToolHub browser/DB is the declarative MCP
  configuration plane; mcpm owns shared process lifecycle/relay; Profiles own
  Skills only.
- Document add/edit/enable/disable/delete behavior, secret handling,
  operation/retry semantics, health states, and rollback/backup behavior.
- Mark the old MCP-in-Profile routing-governance design as superseded and
  remove stale claims about Profile-published routing, enforced governance, or
  Profile Apply controlling MCP membership.
- Document the exact one-time cleanup scope and post-migration verification.

## Data destruction guard

The following are persistent-state and host-state mutations and are not part of
the implementation plan's automatic first pass:

- ToolHub MCP names: `desktop-commander`, `memory`, `sequential-thinking`.
- Historical MCP revisions, relay configuration references, contracts,
  telemetry/observations, encrypted Secret rows, snapshots, operations, and
  audit events that exist solely for those three names or the retired
  `shared-mcp` Profile governance path.
- Library Skills: the ten exact unreferenced slugs listed in Baseline and all
  historical Profile revision references/artifacts/sources that become orphaned.
- Host mcpm registry/profile entries, running child processes, npm/uv caches,
  and any runtime Skill directories for those exact names, after a read-only
  presence check.

Execution record (completed 2026-08-17): scoped confirmation covered the live
Docker PostgreSQL volume and root-host mcpm/runtime state. A restorable dump was
created at `/var/tmp/toolhub-mcpm-cleanup.a78TTx/toolhub.dump` with SHA256
`8067a42fd1488e34646dda80e7f298ee4a4e81defe59e26df5a87f885859afee4`. Dry-run
counts were reviewed before migrations 013-015 ran. A follow-up dump was created
at `/var/tmp/toolhub-text-processing-cleanup.dump` with SHA256
`4ab41c362f9507801aace0aa91ae5b818edcac466e28c64f2c49fc792bcb809c`; migration
017 removed the two archived `*-text-processing` Profiles and their 10 history
revisions (19 current/180 historical membership rows). No expanded names or
unrelated account/session/security data were touched. Postconditions are recorded in
`docs/aegis/work/2026-08-17-mcpm-toolhub-mcp-profile-separation/90-evidence.md`.

## Verification strategy

1. Focused Go tests for store migrations/Profile invariants, relay runtime/admin
   health, Bridge journal/HMAC/idempotency, worker operation projection, and
   HTTP contracts.
2. `GOCACHE=/tmp/toolhub-gocache go test ./...` and `go vet ./...`.
3. `go test -race ./...` for worker/journal/relay shared-state changes.
4. Salt unit tests (no behavior change expected) and `make docker-config`.
5. `cd web && npm audit --audit-level=high && npm run typecheck && npm run build`.
6. Fresh Compose migration test plus API smoke; verify Profile payloads have no
   MCP fields, MCP edits create one relay operation, and relay member health is
   not globally `unavailable` when mcpm is healthy. Authenticated API smoke and
   desktop/mobile Playwright now pass using the current singleton account via
   ignored `.env.local`; the bootstrap value remains separate and is not used
   for existing accounts.
7. Host acceptance: `mcpm ls`, `mcpm profile ls`, fixed relay status, exact
   upstream process count for enabled members, and absence of the three removed
   names. Real Salt canary remains external and is not required for local MCP
   acceptance.
8. Lingering-reference scans (`rg`) and SQL assertions proving the retired
   Profile governance path is not on the active request path.

## Risks and rollback

- Relay config writes affect the live shared service. Every write must retain
  the existing Bridge backup/atomic replacement/rollback sequence; no direct
  host edits are allowed.
- Profile/schema changes can strand old snapshots. Migrate or quarantine them
  before enforcing the new validation; never silently reinterpret a v2 manifest.
- Purging immutable history/artifacts is irreversible. Restore the database
  volume and host relay backup as the rollback unit if the explicit purge is
  approved; do not attempt ad-hoc row recreation.
- If mcpm health contract is unavailable, report bounded relay health failure;
  do not mark all members ready or add a generic process probe fallback.

## TDD Route

- Mode: off
- Decision: skipped
- Strict authority: none requested
- Test posture: diagnostic reproduction plus focused post-change regression
- Reason: the user requested a cross-layer refactor, not strict test-first TDD.
- Verification: focused packages, full Go/Web gates, Compose smoke, and host
  relay checks above.

## Execution readiness view

- Intent lock: browser-managed MCP configuration; mcpm-owned runtime; Skill-only
  Profiles; requirements docs synchronized.
- Scope fence: no Agent/RBAC/new queue/new proxy; no remote Salt MCP redesign.
- Baseline lock: current migrations 001-017, existing Relay Configuration
  owner, Bridge typed paths, mcpm compatibility relay unit, and current live
  DB/host inventory.
- Compatibility boundary: additive migration; legacy data read only during
  migration; no rewrite of applied migrations.
- Retirement boundary: legacy `shared-mcp` Profile MCP ownership, retired
  `*-text-processing` Profiles, and old
  routing-governance health dependency are retired; the confirmation-gated
  destructive purge is complete and its evidence is retained.
- Review gates: architecture/API/schema review, security/secret review,
  runtime/packaging review, then verification-before-completion.
- Evidence required: migration assertions, no stale active references, healthy
  per-member relay projection, operation idempotency, and full test ladder.
