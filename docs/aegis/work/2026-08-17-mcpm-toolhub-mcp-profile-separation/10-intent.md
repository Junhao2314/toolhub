# Task intent and baseline

## TaskIntentDraft

Goal: keep ToolHub as the browser/declarative owner of shared MCP
configuration, keep mcpm as the sole relay/process owner, make Profiles
Skill-only, repair per-member relay health, and remove the explicitly approved
obsolete MCP/Skill state.

Scope: Go store/domain/worker/API/Bridge/runtime, mcpm health contract and
packaging, React MCP/Profile workflows, generation-2 migration, and related
requirements/operator documentation.

Non-goals: new Agent/RBAC/queue/proxy, remote Salt MCP redesign, weakening
secret protections, or retaining a hidden legacy Profile governance fallback.

Stop condition: done only after focused/full verification and live dry-run plus
approved cleanup assertions; blocked/needs-verification if runtime or migration
evidence is incomplete.

## BaselineReadSetHint

- `AGENTS.md`, `plans/2026-08-17-mcpm-toolhub-mcp-profile-separation.md`
- `.builder/architecture.md`, `CONTEXT.md`, `README.md`
- current migrations `001`-`012`, `internal/store/db.go`
- `internal/domain/models.go`, `internal/bridgeprotocol/types.go`,
  `internal/bridge/adapter.go`, `internal/worker/worker.go`
- `internal/store/profiles.go`, `library.go`, `relay_governance.go`,
  `snapshots.go`, `internal/runtime/relay*.go`, `mcpm` admin/run code
- Browser/Bridge OpenAPI, handlers, web MCP/Profile pages and e2e fixtures

## BaselineUsageDraft

- Required baseline refs: acknowledged and cited in parent plan.
- Missing refs: none identified for the first slice.
- Decision: continue.

## ImpactStatementDraft

Affected owners: PostgreSQL persistence and immutable history, Profile and
Relay Configuration contracts, Bridge HMAC/journal boundary, mcpm admin
protocol, systemd relay packaging, HTTP/OpenAPI, React UI, and cleanup of
live host/database state.

Primary invariants: one active target operation, atomic relay backup/replace,
write-only encrypted secrets, immutable desired snapshots, accepted local
runtime path guards, and no plaintext secrets in journals/logs/responses.

Compatibility boundary: additive migration only; legacy records are read only
during migration and never become a new active owner.

Retirement boundary: `shared-mcp` Profile MCP ownership and governance-only
health probe retire; destructive purge is exact-scope confirmed and backed up.
