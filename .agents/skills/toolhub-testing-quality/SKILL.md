---
name: toolhub-testing-quality
description: Test and troubleshoot ToolHub generation-2 Go control plane, Bridge/HMAC journal, local runtime and shared relay, Salt driver/Python module, PostgreSQL schema/operations, React UI, Compose API smoke, and desktop/mobile Playwright behavior. Use for regressions, CI failures, acceptance, or choosing focused coverage.
---

# ToolHub Testing And Quality

Read `AGENTS.md`, `Makefile`, CI, `scripts/smoke-api.sh`, Playwright config/e2e,
Salt Python tests, and adjacent package tests.

Use this ladder:

1. Run the narrow changed Go/Python test.
2. Run `go test ./...` and `go vet ./...`.
3. Run `go test -race ./...` for session, worker, journal, recovery, or shared
   state changes (not currently CI-gated).
4. Run web audit/typecheck/build for UI/contracts/dependencies.
5. Run `make docker-config`.
6. Start a host Bridge plus fresh Compose stack, then API smoke and Playwright.
7. Run a real Salt 3008.x canary only when connectivity is available.

Current CI jobs are `go-test`, Linux `bridge-build`, `web`, and
`container-smoke`. Container smoke starts an isolated Bridge, builds a fresh
Compose database, runs username/CSRF API smoke, then desktop/mobile Playwright.

Prioritize these invariants in regression tests:

- singleton/session revocation, CSRF, timing-safe login, secure cookies;
- fresh schema generation rejection and secret/manifest constraints;
- HMAC tamper/expiry/replay/idempotency and sensitive journal rejection;
- revision-bound destructive diff, immutable pinning, partial fleet/retry;
- reconcile no-op/no-backup, pinned repair, unmanaged preservation/coalescing;
- JID persistence/restart/cache-miss scan, streaming JSON, staging cleanup;
- path/symlink/type/size/protected guards and atomic backup/rollback;
- shared relay port/pause/health/rollback and write-only MCP secret UI;
- desktop/mobile overflow, navigation, preflight, partial results, relay labels.

Do not claim integrated behavior from unit tests alone. State which gates ran
and distinguish external blockers from failures. The currently accepted Salt
minions time out, so real Salt acceptance is unavailable until connectivity is
repaired.

Commands:

```bash
GOCACHE=/tmp/toolhub-gocache go test ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s packaging/salt/tests -p '*_test.py'
cd web && npm audit --audit-level=high && npm run typecheck && npm run build
cd .. && make docker-config
```

For Playwright, start the backend first and provide
`TOOLHUB_E2E_USERNAME`/`TOOLHUB_E2E_PASSWORD`; Chrome is fixed at
`/usr/bin/google-chrome` and workers are serial.
