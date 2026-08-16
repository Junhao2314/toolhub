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
- `mcpm`: one local `toolhub` profile served through one shared HTTP relay at
  `http://127.0.0.1:6276/mcp` by default. Codex, Claude, and local Hermes each
  contain one native user-scope `toolhub-relay` anchor. The local Hermes MCP
  map is collapsed to that anchor on Apply; remote Hermes remains read-only.

Relay governance starts in explicit `compatibility` mode. Contract review and
Profile candidate creation never publish or Apply automatically. Switching to
`enforced` is revision-bound and fails closed unless the applied v2 Relay state,
Restore backup, accepted Contracts, Profile metadata, compatible mcpm features,
and both Claude/Codex adapters are ready. The legacy `shared-mcp` Profile is
retained for history and rollback, then hidden from the ordinary Profile list
only after that transition succeeds.

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
- `mcpm` installed at `/usr/bin/mcpm` for local MCP. ToolHub does not install or
  upgrade it automatically.

Build and install the host services:

```bash
make build
sudo install -m 0755 bin/toolhub-bridge /usr/local/sbin/toolhub-bridge
sudo packaging/systemd/install-toolhub-services.sh "$USER" "$(id -gn)" toolhub
```

The installer creates `/etc/toolhub-bridge/hmac.key`, the shared socket group,
and the Bridge/relay units. Record the printed Bridge GID and HMAC key in the
ToolHub environment. The HMAC key is independent from `TOOLHUB_MASTER_KEY`.

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

Target edit and Restore also create new snapshots. Every five minutes,
reconcile repairs only pinned managed members and preserves content added after
Apply. A no-op reconcile creates no backup; every write creates one first.
Retention is 30 days and at most 10 backups per target.

Target health is `healthy`, `drifted`, `repairing`, `blocked`, or `unavailable`.
Operation status is `queued`, `running`, `succeeded`, `partial`, `failed`, or
`cancelled`. Cancel prevents queued dispatch only; a running atomic target step
is never interrupted.

## Development

```bash
GOCACHE=/tmp/toolhub-gocache go test ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
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
