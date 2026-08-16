# Clean Deployment

ToolHub generation 2 is a fresh-database deployment. It does not migrate users,
roles, Agent state, jobs, deployments, approvals, or audit history from an older
ToolHub database. The optional offline
[configuration importer](CONFIG_MIGRATION.md) transfers only the reviewed
Library, MCP, desired-selection, and schedule subset into a distinct fresh
database. Keep the old volume as a whole-stack rollback artifact.

## Prerequisites

- Linux with systemd.
- Docker Engine and Compose v2.
- One existing local managed user with a real, non-symlink home.
- `/root/docker/toolhub` as the canonical, root-owned repository root, including
  its embedded `mcpm/` project.
- A root-owned `/root/docker/toolhub/bin/toolhub-bridge` and
  `/root/docker/toolhub/mcpm/.venv/bin/mcpm`; its shebang must use the repository
  `.venv/bin/python3`, resolving into `/root/.local/share/uv/python`.
- Salt Master/minions `3008.x` when remote targets are required.
- Existing Salt `base` file root at `/srv/salt/states`.

ToolHub does not install or upgrade `mcpm`, edit `/etc/salt/master`, or open the
Bridge over TCP.

## 1. Back Up The Old Generation

Before replacing anything, record the current image/version and back up:

- the PostgreSQL volume;
- local Claude/Codex/Hermes homes and MCP configuration;
- mcpm registry/profile data;
- `/srv/salt/states`;
- existing ToolHub/Agent packages and unit files.

Do not attach the generation-2 application to a copied legacy database. The
offline importer may read a restored legacy clone, but generation-2 application
startup must use its distinct fresh database. Retain the backup for all-at-once
rollback.

## 2. Build And Install Host Services

```bash
cd /root/docker/toolhub
make build
sudo packaging/systemd/install-toolhub-services.sh \
  MANAGED_USER /root/docker/toolhub MANAGED_GROUP toolhub
sudo systemctl status toolhub-bridge.service
```

The installer validates repository ownership, the embedded mcpm project,
executable ownership/modes, the mcpm shebang and uv interpreter, and the
ToolHub capability contract before writing units. It prints the shared Bridge GID. Read the
root-only HMAC key locally and place its exact value in `.env`; do not commit or
log it. The units bind the repositories read-only inside their `ProtectHome`
namespaces, while managed-home writes remain separately guarded.

## 3. Configure ToolHub

Create `.env` from `.env.example` and replace every placeholder:

```bash
cp .env.example .env
openssl rand -base64 32
```

Required values:

| Variable | Meaning |
| --- | --- |
| `TOOLHUB_MASTER_KEY` | 32 raw bytes or base64-encoded 32 bytes for encrypted secrets |
| `TOOLHUB_BOOTSTRAP_USERNAME` | initial singleton username; default `admin` |
| `TOOLHUB_BOOTSTRAP_PASSWORD` | initial password; ignored after the account exists |
| `TOOLHUB_MANAGED_USERNAME` | default local/remote managed OS username |
| `TOOLHUB_BRIDGE_GID` | numeric socket group GID printed by the installer |
| `TOOLHUB_BRIDGE_HMAC_KEY` | exact independent Bridge key |

Important optional values:

| Variable | Default | Notes |
| --- | --- | --- |
| `TOOLHUB_PORT` | `18480` | published on host loopback only |
| `TOOLHUB_LOCAL_NODE_NAME` | `project-host` | local node display name |
| `TOOLHUB_RELAY_PORT` | `6276` | fixed relay port, no auto-find |
| `TOOLHUB_TIMEZONE` | `Asia/Shanghai` | initial update schedule timezone |
| `TOOLHUB_SESSION_TTL` | `12h` | valid range `15m` to `168h` |
| `TOOLHUB_SECURE_COOKIES` | `true` | set false only for local plain-HTTP smoke |
| `SKILLSMP_API_KEY` | empty | optional marketplace quota key |
| `XIAPING_API_KEY` | empty | optional explicit Xiaping import key |

`TOOLHUB_DATABASE_URL` is built by Compose from
`TOOLHUB_POSTGRES_PASSWORD`. Compose mounts the Bridge runtime directory only;
it does not mount managed homes or `/var/lib/toolhub-bridge`.

## 4. Start With A Fresh PostgreSQL Volume

Use a new Compose project/volume or remove the old stack only after its backup
has been verified. Then start Bridge first and the application second:

```bash
sudo systemctl start toolhub-bridge.service
docker compose config --quiet
docker compose up -d --build --wait
curl --fail http://127.0.0.1:18480/healthz
```

Expected health shape:

```json
{"bridge":"ok","status":"ok"}
```

Any non-empty database without `app_meta.schema_generation=2` fails before the
HTTP listener starts and instructs the operator to replace the PostgreSQL
volume.

## 5. Verify Authentication And UI

For local HTTP testing only, set `TOOLHUB_SECURE_COOKIES=false`, rebuild, and
run:

```bash
TOOLHUB_SMOKE_USERNAME=admin \
TOOLHUB_SMOKE_PASSWORD='<bootstrap-password>' \
sh scripts/smoke-api.sh
```

The smoke verifies username login, Overview, target presence, CSRF rejection,
and logout. Production should keep secure cookies enabled and expose the
loopback service through an authenticated HTTPS/Tailnet boundary.

Changing the singleton username or password revokes all sessions. Bootstrap
credentials have no effect after the account exists.

## 6. Enable Managed Targets

1. Refresh nodes and verify only accepted Salt keys are shown.
2. Restore Salt connectivity until remote nodes report `3008.x`.
3. Scan `local/claude`, `local/codex`, `local/hermes`, and `local/shared-relay`.
4. For local MCP, verify `/root/docker/toolhub/mcpm/.venv/bin/mcpm` and its capability
   contract, configure the fixed port, and Apply
   a Profile to `local/shared-relay`.
5. Canary one non-critical Salt minion before a fleet Apply.

Apply is destructive only inside the documented manageable scope and always
requires a fresh preflight confirmation. Reconcile begins automatically every
five minutes once a target has an active desired snapshot. Shared-relay member
discovery runs every 30 minutes; blocked relays retry after 5 minutes, 15
minutes, and 1 hour before suspension.

## Updates

Upgrade the Bridge binary and ToolHub image as one release:

```bash
make build
sudo packaging/systemd/install-toolhub-services.sh \
  MANAGED_USER /root/docker/toolhub MANAGED_GROUP toolhub
sudo systemctl restart toolhub-bridge.service
docker compose up -d --build --wait
```

Do not deploy generation-2 code against a generation-1 database or mix old
Agent delivery paths with Bridge/Salt delivery. Keep the repository roots
root-owned; after a Git update, rebuild, test, reinstall the units, and restart
services as one change.
