---
name: toolhub-precommit-gates
description: Run the complete ToolHub generation-2 validation suite before a commit or push, including PostgreSQL-backed Go tests, race checks for high-risk changes, Salt tests, web audit/typecheck/build, OpenAPI parsing, Compose validation, API smoke, desktop/mobile Playwright, and diff/secret/generated-file/plan inspection. Use when preparing a ToolHub submission, reviewing a pre-commit diff, or asked to test all cases; fail closed when required database, backend, or browser credentials are unavailable.
---

# ToolHub Pre-commit Gates

Run every relevant quality gate before committing or pushing ToolHub changes.
The bundled script is deterministic and validation-only: it never creates a
commit, pushes, edits source files, or silently skips an integration gate.

## Quick start

From the repository root, start a fresh local Compose stack (and the host
Bridge) when browser/API smoke is required, then provide disposable test
credentials and a dedicated PostgreSQL database:

```bash
export TOOLHUB_TEST_DATABASE_URL='postgres://toolhub:toolhub-test-only@127.0.0.1:5432/toolhub_test?sslmode=disable'
export TOOLHUB_E2E_URL='http://127.0.0.1:18480'
export TOOLHUB_E2E_USERNAME='admin'
export TOOLHUB_E2E_PASSWORD='...'

.agents/skills/toolhub-precommit-gates/scripts/run-gates.sh
```

`TOOLHUB_SMOKE_URL`, `TOOLHUB_SMOKE_USERNAME`, and
`TOOLHUB_SMOKE_PASSWORD` may be set independently. If omitted, the script
uses the E2E URL and credentials for API smoke. Never use production secrets.

## Required workflow

1. Inspect the complete working-tree diff, including staged, unstaged, and
   non-ignored untracked files. Resolve unrelated changes before running gates.
2. Run `scripts/run-gates.sh` from the repository root. It runs all gates and
   reports every failure before exiting non-zero; do not commit after a
   failure.
3. For a failed gate, fix the root cause and rerun the full script. Do not add
   a bypass environment variable or turn a missing prerequisite into a skip.
4. Review `git diff HEAD`, `git diff --cached`, and `git status --short` after
   the run. Commit/push only after the script succeeds and the final diff is
   still the one tested.

## Gate policy

The script enforces these checks:

- PostgreSQL-backed `GOCACHE=/tmp/toolhub-gocache go test -count=1 ./...`; the required
  `TOOLHUB_TEST_DATABASE_URL` prevents integration tests from silently calling
  `t.Skip`.
- `go vet ./...`, temporary binary builds for all three Go commands, and
  `go test -race -count=1 ./...` whenever the diff touches security, store,
  bridge/journal, runtime, Salt, worker, or migration code.
- Salt unit tests with `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover
  -s packaging/salt/tests -p '*_test.py'`.
- `npm ci --ignore-scripts` when web dependencies are absent or the lockfile
  changed, followed by `npm audit --audit-level=high`, `npm run typecheck`,
  and `npm run build`.
- YAML parsing for both `api/openapi.yaml` and `api/bridge-openapi.yaml`, plus
  `make docker-config`.
- A live `/healthz` check, `scripts/smoke-api.sh`, and the complete serial
  Playwright suite (desktop and mobile, ten tests). E2E credentials and a
  reachable backend are required; missing values are failures.
- `git diff --check`, suspicious secret material in added lines, and generated or
  runtime artifacts.

The script uses `TOOLHUB_GATES_RACE=auto` by default. High-risk paths trigger
the race gate automatically; set it to `always` to run the race suite for a
low-risk diff too. The value `never` is rejected for high-risk changes.

## Integrated smoke prerequisites

The script does not mutate Docker, Bridge, Salt, or production state. Start
the isolated services explicitly, for example:

```bash
docker compose up -d --build --wait
curl --fail http://127.0.0.1:18480/healthz
```

Use a fresh PostgreSQL volume for schema changes. If the backend is not
running, the live smoke gate must fail so the missing acceptance coverage is
visible in the handoff.
