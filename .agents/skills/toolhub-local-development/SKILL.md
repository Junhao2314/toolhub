---
name: toolhub-local-development
description: Set up, run, build, and inspect ToolHub locally with Go, Vite, PostgreSQL, Docker Compose, and the embedded UI. Use when onboarding a developer, reproducing a bug, changing dependencies, validating a local build, compose smoke, Playwright setup, or Makefile side effects.
---

# ToolHub Local Development

## When to use

Onboarding, reproducing bugs, dependency changes, local build/run, Compose smoke, Playwright prerequisites, or understanding Makefile/Docker side effects.

## Read first

Read `AGENTS.md`, `README.md`, `.env.example`, `Makefile`, `internal/config/config.go`, `compose.yaml`, `Dockerfile`, `.dockerignore`, `.github/workflows/ci.yml`, `scripts/smoke-api.sh`, `web/package.json`, `web/vite.config.ts`, and bootstrap paths in `internal/store/db.go` / `cmd/toolhub/main.go`.

## Choose a run mode

| Mode | When | How |
|------|------|-----|
| Docker Compose | Integrated control plane + Postgres + smoke/e2e | `docker compose up -d --build --wait` |
| Native control plane | Iterate on Go against a DB | set env + `make dev` / `go run ./cmd/toolhub` |
| Vite UI only | UI iteration with API on 18480 | `cd web && npm run dev` → `127.0.0.1:18481` |
| Production path | Embedded UI binary/image | `make build` or Dockerfile multi-stage |

## Setup workflow

1. Copy `.env.example` → `.env`. Set:
   - `TOOLHUB_MASTER_KEY` — exactly 32 raw bytes **or** base64-encoded 32 bytes
   - `TOOLHUB_BOOTSTRAP_ADMIN_EMAIL` / `TOOLHUB_BOOTSTRAP_ADMIN_PASSWORD`
   - optional `TOOLHUB_BOOTSTRAP_ADMIN_USERNAME` (default `admin`, normalized)
2. Start integrated stack and prove readiness:

```bash
docker compose up -d --build --wait
curl --fail http://127.0.0.1:18480/healthz
```

3. For local HTTP browser/API smoke, set `TOOLHUB_SECURE_COOKIES=false` (Compose default is `true`).
4. Install/validate tooling:

```bash
go test ./...
cd web && npm ci --ignore-scripts
cd web && npm run typecheck && npm run build
make docker-config
```

5. Optional smoke:

```bash
TOOLHUB_SMOKE_EMAIL=... TOOLHUB_SMOKE_PASSWORD=... sh scripts/smoke-api.sh
# Playwright (backend already up; Chrome at /usr/bin/google-chrome):
TOOLHUB_E2E_EMAIL=... TOOLHUB_E2E_PASSWORD=... cd web && npm run test:e2e
```

## Commands and mutating targets

| Target | Behavior | Mutates tree? |
|--------|----------|---------------|
| `make build` | `web` then `bin/toolhub` + `bin/toolhub-agent` | yes (via web) |
| `make web` | `npm ci` + build; **rm** `cmd/toolhub/dist/assets`; **cp** `web/dist` → `cmd/toolhub/dist/` | **yes** |
| `make test` | `go test ./...` + web typecheck | no |
| `make lint` | **`gofmt -w`** on `cmd`/`internal`, `go vet`, typecheck | **yes** |
| `make docker-config` | `compose config --quiet` with placeholder required env | no |
| `make dev` | `go run ./cmd/toolhub` | no |

Prefer `npm ci --ignore-scripts` for CI parity; `make web` currently uses bare `npm ci`. Dockerfile web stage also uses bare `npm ci`.

## Ports and bind addresses

- Host publish: `127.0.0.1:${TOOLHUB_PORT:-18480}:18480` only — never public.
- Default non-Compose listen: `TOOLHUB_LISTEN_ADDR=127.0.0.1:18480`.
- Compose container listen: `0.0.0.0:18480` (still published only on loopback).
- Vite dev: `127.0.0.1:18481`; proxies `/api` → `http://127.0.0.1:18480`, `/agent` → `ws://127.0.0.1:18480`.

## Environment reference

| Variable | Required? | Notes |
|----------|-----------|-------|
| `TOOLHUB_DATABASE_URL` | yes (`config.Load`) | Compose builds URL from `TOOLHUB_POSTGRES_PASSWORD` |
| `TOOLHUB_MASTER_KEY` | yes | 32 raw or base64-32 |
| `TOOLHUB_BOOTSTRAP_ADMIN_EMAIL` | first admin | Compose requires via `:?` |
| `TOOLHUB_BOOTSTRAP_ADMIN_PASSWORD` | first admin | Compose requires |
| `TOOLHUB_BOOTSTRAP_ADMIN_USERNAME` | optional | default `admin` |
| `TOOLHUB_BOOTSTRAP_ADMIN_NAME` | optional | default `ToolHub Admin` |
| `TOOLHUB_LISTEN_ADDR` | optional | default `127.0.0.1:18480` |
| `TOOLHUB_LOCAL_NODE_NAME` | optional | default `project-host` |
| `TOOLHUB_PUBLIC_URL` | optional | enrollment/WSS host; trailing `/` stripped |
| `TOOLHUB_TIMEZONE` | optional | default `Asia/Shanghai` |
| `TOOLHUB_SECURE_COOKIES` | optional | default **`true`**; smoke/CI use **`false`** |
| `TOOLHUB_SESSION_TTL` | optional | default `12h`; must be 15m–168h |
| `TOOLHUB_DATA_DIR` | optional | default `/data` |
| `SKILLSMP_API_KEY` | optional | empty ok |
| `TOOLHUB_PORT` | host map only | default `18480` |
| `TOOLHUB_SMOKE_URL` / `_EMAIL` / `_PASSWORD` | smoke script | email/password required by script |
| `TOOLHUB_E2E_URL` / `_EMAIL` / `_PASSWORD` | Playwright | backend must already run |

`config.Load` hard-fails only on missing DB URL / master key / invalid TTL/cookies/username. Empty bootstrap password fails at `BootstrapAdmin` when creating the first user (if users already exist, bootstrap is a no-op).

## Compose / Docker facts

- Services: `postgres` (16-alpine, healthcheck `pg_isready`) and `toolhub` (build context `.`).
- **Only Postgres is healthchecked**; `--wait` does not prove app readiness — always `curl /healthz`.
- Hardening: `read_only`, tmpfs `/tmp`, `cap_drop: ALL`, networks `backend` (internal) + `egress`.
- Volumes: `postgres_data`, `toolhub_data` → `/data`.
- Dockerfile: multi-stage `node:22-alpine` → `golang:1.22-alpine` (CGO=0, embeds `cmd/toolhub/dist`) → `alpine:3.21` non-root `toolhub` user; ships both binaries; ENTRYPOINT `toolhub`.
- `.dockerignore` drops `.git`, `.env`, `bin`, `web/node_modules`, `web/dist`, `cmd/toolhub/dist`, playwright artifacts.

## Agent packaging (local install templates)

| Platform | Path | Config surface |
|----------|------|----------------|
| Linux | `packaging/systemd/toolhub-agent.service` | `/usr/local/bin/toolhub-agent run --config /etc/toolhub-agent/agent.json` |
| macOS | `packaging/launchd/com.toolhub.agent.plist` | Application Support agent.json |
| Windows | `packaging/windows/install-service.ps1` | `sc.exe` + `run` |

## Common failures

- Secure cookies on plain HTTP → login “broken”; use `TOOLHUB_SECURE_COOKIES=false` for local HTTP.
- Missing master key / DB URL → `config.Load` fails before listen.
- Host `make dev` with Compose hostname `postgres` in `DATABASE_URL` fails (name only resolves inside Compose).
- UI not updated in binary until `make web` / Docker rebuild copies dist.
- Playwright without live backend or without `/usr/bin/google-chrome`.
- Treating `docs/workflows/**` historical race/extra gates as current CI (they are not).

## Reuse

Reuse Makefile targets, Compose health wait + `/healthz`, `scripts/smoke-api.sh`, and CI env names. Do not invent alternate ports, second smoke suites, or public port publishes.

## Prohibitions

- Do not commit `.env`, real keys, or generated `cmd/toolhub/dist`.
- Do not treat `make lint` / `make web` as read-only verification.
- Do not publish Compose ports beyond `127.0.0.1`.
- Do not hand-edit embedded dist assets.

## Verification

```bash
curl --fail http://127.0.0.1:18480/healthz
TOOLHUB_SMOKE_EMAIL=... TOOLHUB_SMOKE_PASSWORD=... sh scripts/smoke-api.sh
go test ./...
cd web && npm run typecheck
git diff   # after make lint/web, expect formatting/dist churn
```
