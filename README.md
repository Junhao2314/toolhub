# ToolHub

ToolHub is a single-user Linux control plane for managing Codex and Claude
Skills and MCP configuration on one host and across Salt minions. The browser
container never mounts user homes. A root-owned `toolhub-bridge` performs typed,
guarded host operations over an HMAC-authenticated Unix socket.

Licensed under the [MIT License](LICENSE).

## Architecture

- `toolhub`: Go API, operation workers, five-minute reconcile scheduler,
  PostgreSQL state, and embedded React UI.
- `toolhub-bridge`: Linux host service with a mode-`0600` BoltDB journal,
  guarded local adapter, fixed Salt CLI driver, backup catalog, and fixed
  systemd relay controls.
- PostgreSQL 16: singleton account, encrypted secrets, immutable Library
  artifacts, unified Profiles, operations, desired snapshots, backups, settings,
  providers, and actorless audit.
- Salt 3008.x: accepted-key discovery and remote Claude/Codex/Hermes inventory.
  Hermes is read-only. Claude/Codex are writable targets.
- `mcpm`: one shared upstream pool served through one HTTP relay at
  `http://127.0.0.1:6276/mcp?profile=<published-profile-name>`. Claude, Codex,
  Hermes, Kimi, and Grok use the same Profile query; the Profile alone controls
  the visible MCP tools and call policy. A profile member defaults to
  `all_accepted` (all tools from its pinned accepted MCP contract); `selected`
  and `hidden` remain explicit opt-in modes. The local Hermes MCP map is
  collapsed to that anchor on Apply; remote Hermes remains read-only.
- `mcpm/`: the embedded, ToolHub-owned mcpm Python project. The installer
  materializes it at `/usr/libexec/toolhub-mcpm` (launcher) with its runtime
  under `/var/lib/toolhub-bridge/mcpm`, so the relay keeps working when the
  checkout is moved or re-cloned.

ToolHub is the MCP control plane: it versions Library inputs, Contracts,
Profiles, policy, desired snapshots, and routing bundles, then delivers them
through the Bridge. It never proxies MCP tool traffic. `mcpm` is the local MCP
data plane: it owns the shared upstream process set and exposes every configured
tool from the `toolhub` Profile through one relay endpoint.

Relay routing governance was removed on 2026-08-16: the contract-publication
flow never worked and an empty bundle hid every tool. The relay unit therefore
runs in compatibility (pass-through) mode — `profile run ... toolhub` without
`--toolhub-routing` — and exposes all configured MCP tools. Do not re-add
`--toolhub-routing`/`--toolhub-admin-socket` without a working contract flow.
The governance control endpoints, revision history, and routing-file writes
remain in the control plane for compatibility, but the running relay no longer
enforces filtered catalogs or call policy. The legacy `shared-mcp`
Profile is retained for history and rollback.

There is no Agent, WebSocket enrollment, SSH fallback, RBAC, multi-user API,
review/approval workflow, deployment table, or legacy job queue.

## Fresh Database Requirement

Generation 2 intentionally supports fresh databases only. Startup checks
`app_meta.schema_generation=2` before the HTTP server starts. A legacy or
unknown schema fails with instructions to create a new PostgreSQL volume.
Application startup never converts legacy rows. The separate offline
[`toolhub-config-migrate`](docs/CONFIG_MIGRATION.md) command can read the
reviewed configuration subset from an exact legacy-v11 database into a
distinct fresh database. Keep the old volume for whole-stack rollback.

## Host Prerequisites

- Linux with systemd and a local managed user.
- Docker Engine with Compose v2.
- PostgreSQL 16 (provided by Compose).
- Salt Master/minions 3008.x for remote targets. ToolHub reuses the existing
  `base` root at `/srv/salt/states` and does not edit `/etc/salt/master`.
- The embedded `mcpm/` project with a root-owned `.venv/bin/mcpm` launcher
  (repository build/validation only) and
  uv-managed interpreter under `/root/.local/share/uv/python`. ToolHub does not
  install or upgrade the embedded runtime automatically; the installer
  validates it and materializes the installation-owned `/usr/libexec/toolhub-mcpm`
  launcher plus the `/var/lib/toolhub-bridge/mcpm` runtime copy.

Build and install the host services:

```bash
make mcpm-sync
make build
sudo packaging/systemd/install-toolhub-services.sh \
  "$USER" /root/docker/toolhub "$(id -gn)" toolhub
```

The installer validates the canonical, root-owned ToolHub repository, the
embedded mcpm launcher/shebang, and its resolved uv interpreter before it
creates `/etc/toolhub-bridge/hmac.key`, the shared socket group, and the
Bridge/relay units. Record the printed Bridge GID and HMAC key in the ToolHub
environment. The HMAC key is independent from `TOOLHUB_MASTER_KEY`.

The services execute directly from `/root/docker/toolhub/bin/toolhub-bridge`
and `/usr/libexec/toolhub-mcpm`; no second mcpm checkout, global
`/usr/bin/mcpm`, or copied `/usr/local/sbin` runtime is required. After updating
this repository, rebuild, rerun the installer, and restart the affected unit.

## Clean Start

```bash
cp .env.example .env
openssl rand -base64 32  # TOOLHUB_MASTER_KEY
# Set the Bridge GID/key, managed username, and a unique bootstrap password.
docker compose up -d --build --wait
curl --fail http://127.0.0.1:18480/healthz
TOOLHUB_SMOKE_USERNAME=admin \
TOOLHUB_SMOKE_PASSWORD='<bootstrap-password>' \
sh scripts/smoke-api.sh
```

The bootstrap username/password create the singleton account only when no
account exists. Later starts ignore both values. Changing either credential
revokes every session.

Compose publishes only `127.0.0.1:18480`. Keep that loopback binding and use
Tailscale Serve/ACLs or another authenticated HTTPS boundary when remote browser
access is required. Production cookies are `Secure` by default; local plain-HTTP
smoke must set `TOOLHUB_SECURE_COOKIES=false`.

## Desired State

Profiles reference Skill IDs and MCP server IDs. Every Apply begins with a
five-minute, one-use preflight token bound to the Profile revision, target
revision, and canonical manifest. Apply mirrors the manageable scope and pins
the exact current artifact versions, MCP revisions, secret IDs, and hashes in an
immutable desired snapshot.

Skills carry normalized tags (lowercase slugs, up to 50 per Skill). The
`required` tag means every non-`shared` Profile revision must include that
Skill; saving a revision without a required Skill is rejected. Existing
revisions stay immutable — the check applies when a new revision is saved.

Target edit and Restore also create new snapshots. Every five minutes,
reconcile repairs only pinned managed members and preserves content added after
Apply. A no-op reconcile creates no backup; every write creates one first.
Retention is 30 days and at most 10 backups per target.

Target health is `healthy`, `drifted`, `repairing`, `blocked`, or `unavailable`.
Operation status is `queued`, `running`, `succeeded`, `partial`, `failed`, or
`cancelled`. Cancel prevents queued dispatch only; a running atomic target step
is never interrupted.

Calls classified for confirmation use a five-minute challenge and an exact,
60-second one-shot grant. The grant is consumed before dispatch and is never
restored. Only a proven pre-dispatch failure is reported as `not_executed`; a
post-dispatch transport ambiguity is `execution_unknown` and must be inspected
before any manual retry. Relay observations retain no payload and live in mcpm
for at most 24 hours; ToolHub persists payload-free daily aggregates for 30
days.

## Development

```bash
make mcpm-sync
GOCACHE=/tmp/toolhub-gocache go test ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
make mcpm-lint
make mcpm-contract
cd web && npm ci --ignore-scripts && npm run typecheck && npm run build
cd .. && make docker-config
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s packaging/salt/tests -p '*_test.py'
```

The Vite server uses `127.0.0.1:18481` and proxies `/api` to `18480`. Set
`TOOLHUB_VITE_API_TARGET` to test against an isolated backend on another port.
Generated embedded assets under `cmd/toolhub/dist` are ignored; do not hand-edit
them.

See [Browser API](docs/API.md), [Bridge](docs/BRIDGE.md),
[Security](docs/SECURITY.md), [Deployment](docs/DEPLOYMENT.md),
[Configuration migration](docs/CONFIG_MIGRATION.md), [Salt](docs/SALT.md),
and [Rollout](docs/ROLLOUT.md).
