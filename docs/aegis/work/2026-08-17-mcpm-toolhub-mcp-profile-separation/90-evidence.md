# Evidence bundle

Date: 2026-08-17 (Asia/Shanghai)

## Backup and migration

- Database dump: `/var/tmp/toolhub-text-processing-cleanup.dump`
- SHA256: `4ab41c362f9507801aace0aa91ae5b818edcac466e28c64f2c49fc792bcb809c`
- Live schema migrations: `013-017` (including `017_remove_text_processing_profiles.sql`)
- Live `schema_migrations.max(version)`: `17`
- Migration 015 first hit the intended immutable-trigger guard; the migration
  was amended to use a scoped trigger maintenance exception and then applied
  successfully. No destructive shell cleanup was used.

## Live postconditions

- Applied Relay Configuration members: `6`
  (`acemcp`, `agent-browser`, `context7`, `deepwiki`, `grok-search`, `trellis`)
- Shared relay target: `healthy`
- Relay member projection: `6/6 ready`
- `toolhub-mcpm-relay.service`: `active`
- `toolhub-bridge.service`: `active`
- `/healthz`: `{"bridge":"ok","status":"ok"}`
- mcpm contract: runtime `mcpm`, version `2.15.0-toolhub.1`
- `profile_revision_mcp_governance`: `0`
- `profile_revision_mcp_servers`: `0`
- `profile_mcp_servers`: `0`
- `profile_revision_tool_rules`: `0`
- `pending_secret_bindings`: `0`
- `shared-mcp` Profile: absent
- `claude-text-processing` / `codex-text-processing` Profiles: absent
- text-processing Profile revisions: `0`
- retired MCP names (`desktop-commander`, `memory`, `sequential-thinking`): absent
- retired Skill slugs (the approved ten): absent
- old Profile MCP Apply operation targets: `0`
- old Profile MCP preflight confirmations: `0`
- active relay snapshots containing Profile routing entries: `0`

## Verification

- `GOCACHE=/tmp/toolhub-gocache go test ./...` — passed
- `GOCACHE=/tmp/toolhub-gocache go test -race ./...` — passed
- `GOCACHE=/tmp/toolhub-gocache go vet ./...` — passed
- `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s packaging/salt/tests -p '*_test.py'` — 10 passed
- `cd web && npm audit --audit-level=high` — 0 vulnerabilities
- `cd web && npm run typecheck && npm run build` — passed
- `make docker-config` — passed
- `git diff --check` — passed
- focused regression `TestActiveRelayConfigurationBundleIgnoresProfileRevisions` — passed
- `TOOLHUB_SMOKE_USERNAME=liujh273 TOOLHUB_SMOKE_PASSWORD=... sh scripts/smoke-api.sh` — passed
- `cd web && npm run test:e2e` — 24 passed across desktop and mobile projects

## Authenticated acceptance

The current singleton account credentials were supplied through the ignored,
mode-600 `.env.local` file. API smoke verified login, overview, local targets,
CSRF rejection, and logout. Playwright verified the desktop/mobile navigation,
Skill-only Profile editing, shared mcpm relay configuration, write-only MCP
secrets, target workflows, and operation handling. The password value is not
recorded in this evidence bundle.
