# ToolHub Architecture

ToolHub is a modular monolith: one Go control-plane binary embeds the compiled React UI and runs API, WebSocket hub, scheduler, and job workers. A separate Go agent binary runs on managed nodes. PostgreSQL is the durable source of truth.

## Invariants

- The container binds only `127.0.0.1:18480`; Tailscale Serve owns external HTTPS/WSS termination.
- Update discovery never mutates desired state. Only an explicit approval creates a desired version.
- Reconciliation writes only previously approved desired state and is idempotent per node/runtime/version.
- Imported packages are immutable and identified by source commit, canonical SHA-256, and provenance.
- Agent tasks use a fixed typed protocol, are HMAC-signed, and never expose arbitrary shell execution.
- Existing non-Hermes Skills are read-only until explicit adoption; adoption uploads a verified immutable snapshot before writing the managed marker. Shared Skills are imported without modifying the legacy source, then deployed as ordinary materialized artifacts.
- Hermes is always a read-only import source. Ordinary inventory creates only discovery candidates. Explicit Skill import uses `skill_snapshot_import` → signed `import_skill_snapshot` and never writes a marker; explicit MCP import pins an observed generation and grants capture only for that candidate. Later source changes set `sourceChanged` and never create drift or reconciliation work.
- MCP discovery is no-write: mcpm membership seeds fixed observed profiles, membership edits keep them observed, and only an exact fixed-profile/runtime deployment enables DB → Agent → mcpm → native-anchor reconciliation for Codex/Claude. Hermes cannot own a deployment, activation, Profile membership target, or rollback transition.
- Secrets are encrypted with a master key and redacted at each inventory/audit/AI/browser boundary that can carry them; there is no universal response middleware.

## Boundaries

- `internal/httpapi`: REST transport, auth middleware, validation and response envelopes.
- `internal/store`: PostgreSQL queries and migrations.
- `internal/security`: password, session, CSRF, encryption, redaction and signing.
- `internal/skills`: package scanning, provenance, hashing and import.
- `internal/agenthub`: WSS connections, signed tasks and inventory.
- `internal/worker`: update/sync schedules and job execution.
- `internal/runtime`: runtime inventory, materialized Skill deployment, read-only Hermes/shared-source snapshot intake, mcpm registry patching, and native relay anchors. Both the runtime and Agent layers reject Hermes writers.
- `cmd/toolhub-agent`: cross-platform node process and runtime adapters.

## Rollback

Deployments retain the previous approved version when one exists. Agent writes a backup manifest before atomic replacement. A rollback job sets that prior version as desired; for a first successful deployment with no previous version, rollback advances desired state to disabled while retaining the version for safe removal and later re-enable. Hermes import snapshots have no desired/actual deployment state and therefore no rollback path back into Hermes.
