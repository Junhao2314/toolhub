# ToolHub Architecture

ToolHub is a modular monolith: one Go control-plane binary embeds the compiled React UI and runs API, WebSocket hub, scheduler, and job workers. A separate Go agent binary runs on managed nodes. PostgreSQL is the durable source of truth.

## Invariants

- The container binds only `127.0.0.1:18480`; Tailscale Serve owns external HTTPS/WSS termination.
- Update discovery never mutates desired state. Only an explicit approval creates a desired version.
- Reconciliation writes only previously approved desired state and is idempotent per node/runtime/version.
- Imported packages are immutable and identified by source commit, canonical SHA-256, and provenance.
- Agent tasks use a fixed typed protocol, are HMAC-signed, and never expose arbitrary shell execution.
- Existing Skills are read-only until explicit adoption; adoption uploads a verified immutable snapshot before writing the managed marker. MCP discovery is automatically baselined without a first-run rewrite, then reconciled from central desired state.
- Secrets are encrypted with a master key and redacted at every API/log boundary.

## Boundaries

- `internal/httpapi`: REST transport, auth middleware, validation and response envelopes.
- `internal/store`: PostgreSQL queries and migrations.
- `internal/security`: password, session, CSRF, encryption, redaction and signing.
- `internal/skills`: package scanning, provenance, hashing and import.
- `internal/agenthub`: WSS connections, signed tasks and inventory.
- `internal/worker`: update/sync schedules and job execution.
- `internal/runtime`: shared runtime inventory/deployment contracts.
- `cmd/toolhub-agent`: cross-platform node process and runtime adapters.

## Rollback

Every deployment records the previous artifact hash. Agent writes a backup manifest before atomic replacement. A rollback job sets the prior approved version as desired and reconciles only selected nodes.
