---
name: toolhub-local-development
description: Set up, run, build, and inspect ToolHub generation 2 with the host Bridge, fresh PostgreSQL 16 Compose stack, embedded React/Vite UI, singleton bootstrap credentials, shared socket GID, Salt fixtures, smoke API, and Playwright. Use for onboarding, local builds, dependency changes, Compose smoke, or dev-server troubleshooting.
---

# ToolHub Local Development

Read `AGENTS.md`, `README.md`, `docs/DEPLOYMENT.md`, `.env.example`, `Makefile`,
`internal/config/config.go`, `compose.yaml`, `Dockerfile`, CI, smoke script,
Playwright config, and bootstrap/migration code.

Start the Bridge before ToolHub. Production installs use the packaged systemd
service. CI/local isolated smoke may run `toolhub-bridge` with explicit paths
under `/tmp`, a root-only key, and the current user's group.

Use a fresh PostgreSQL volume. Any non-empty database without schema generation
2 is intentionally rejected. Configure independent 32-byte
`TOOLHUB_MASTER_KEY` and `TOOLHUB_BRIDGE_HMAC_KEY`, bootstrap username/password,
managed OS username, Bridge socket GID, and optional fixed relay port.

Run modes:

- Compose: integrated PostgreSQL + embedded UI on `127.0.0.1:18480`.
- Native Go: set database/key/socket environment and `go run ./cmd/toolhub`.
- Vite: `cd web && npm run dev` on `127.0.0.1:18481`, proxying `/api`.

Local plain HTTP requires `TOOLHUB_SECURE_COOKIES=false`; production keeps the
default true. The container mounts the socket directory read-only and no user
home.

Know mutating commands: `make web` rewrites ignored embedded dist;
`make build` also writes `bin/`; `make lint` runs `gofmt -w`. Never hand-edit or
commit generated dist, `.env`, HMAC/master keys, journals, or test artifacts.

Baseline validation:

```bash
GOCACHE=/tmp/toolhub-gocache go test ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s packaging/salt/tests -p '*_test.py'
cd web && npm ci --ignore-scripts && npm run typecheck && npm run build
cd .. && make docker-config
```

Integrated validation requires a live Bridge and fresh Compose stack:

```bash
docker compose up -d --build --wait
curl --fail http://127.0.0.1:18480/healthz
TOOLHUB_SMOKE_USERNAME=... TOOLHUB_SMOKE_PASSWORD=... sh scripts/smoke-api.sh
```

Playwright requires `/usr/bin/google-chrome`, a live backend, and
`TOOLHUB_E2E_USERNAME`/`TOOLHUB_E2E_PASSWORD`.
