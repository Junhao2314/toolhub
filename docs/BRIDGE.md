# ToolHub Bridge

`toolhub-bridge` is the only component allowed to mutate managed Linux homes,
control the shared MCP relay, or invoke Salt. It runs as a root-owned systemd
service and exposes typed HTTP operations only over
`/run/toolhub-bridge/bridge.sock`. It never opens a TCP listener.

The checked wire contract is [`api/bridge-openapi.yaml`](../api/bridge-openapi.yaml).
Browser requests cannot select a Bridge path, filesystem path, executable,
systemd unit, Salt target expression, or Salt function.

## Installation

Build the Linux binary and install the packaged units:

```bash
cd /root/docker/toolhub
make build
sudo install -o root -g root -m 0755 bin/toolhub-bridge /usr/local/sbin/toolhub-bridge
sudo packaging/systemd/install-toolhub-services.sh MANAGED_USER MANAGED_GROUP BRIDGE_GROUP
```

`MANAGED_GROUP` defaults to `MANAGED_USER`; `BRIDGE_GROUP` defaults to
`toolhub`. The installer:

- validates the managed OS user and canonical home;
- creates the shared Bridge group when necessary;
- creates `/etc/toolhub-bridge/hmac.key` as a root-only file;
- creates `/var/lib/toolhub-bridge/mcpm-relay.env` with port `6276`;
- renders both units with only the selected canonical managed home exposed in
  their private home namespaces;
- enables and starts `toolhub-bridge.service`;
- prints the GID required by Compose.

Set `TOOLHUB_BRIDGE_GID` to the printed GID and
`TOOLHUB_BRIDGE_HMAC_KEY` to the exact key-file value. The Bridge key is
independent from `TOOLHUB_MASTER_KEY`.

## Host Paths

| Path | Owner/mode | Purpose |
| --- | --- | --- |
| `/run/toolhub-bridge/bridge.sock` | `root:BRIDGE_GROUP`, `0660` | HMAC HTTP socket |
| `/etc/toolhub-bridge/hmac.key` | `root:root`, `0600` | Bridge request key |
| `/var/lib/toolhub-bridge/journal.db` | `root:root`, `0600` | BoltDB operation journal |
| `/var/lib/toolhub-bridge/staging` | root-only | transient Salt bundles |
| `/var/lib/toolhub-bridge/backups` | root-only | local target backups |
| `/var/lib/toolhub-bridge/mcpm-relay.env` | `root:root`, `0600` | fixed relay port |
| `/srv/salt/states` | existing Salt base root | versioned ToolHub assets |

The ToolHub container mounts only the runtime socket directory, read-only, and
joins the fixed Bridge GID. Managed homes and the Bridge journal are never
mounted into the container.

## Authentication And Idempotency

Each request carries `X-ToolHub-Timestamp`, `X-ToolHub-Nonce`, and
`X-ToolHub-Signature`. The signature input is:

```text
UPPERCASE_METHOD\nREQUEST_URI\nUNIX_TIMESTAMP\nNONCE\nSHA256(EXACT_BODY)
```

The accepted timestamp skew is 30 seconds. Nonces are persisted before
dispatch and cannot be replayed. Every mutation also requires an 8-200
character `Idempotency-Key`. Reusing a key with the same request returns the
stored safe response; reusing it with a different request returns
`idempotency_conflict`.

Four authenticated worker-only intake routes are deliberately ephemeral:
`/local/skills/export`, `/local/skills/export-batch`, `/local/mcp/preview`, and
`/local/mcp/capture`. They
still require HMAC timestamp/nonce authentication, but bypass the idempotency
response journal. Preview exposes only key names. Capture plaintext and Skill
archives live only in the request/worker call chain and must be encrypted or
imported immediately.

The server derives Apply/Restore semantics from the typed route. A caller
cannot change the operation kind by editing `operationKind` in the body.

## Durable Journal And Recovery

BoltDB stores only routing and recovery metadata: idempotency hashes, safe
operation states, target IDs, Salt minion IDs/JIDs, staging paths,
pinned-member fingerprints, and backup catalog entries. A mutation records its
request hash and operation ID before dispatch. Its terminal target result is
then stored separately, so a control-plane replay after restart can return the
recovered result without executing the adapter a second time. Pending
idempotency records remain replayable across Bridge restart.

Only the safe terminal projection is journaled. Manifest and editable `details`
fields are stripped, `managedHome` is never persisted, and the Salt JID is
retained for operation projection and recovery. Relay results retain only the
fixed endpoint/unit state, bounded contract result, member names, capability
counts, stable error codes, and truncated safe reasons. Journal validation
rejects secret values, archives, editable configuration, plaintext fields, and
raw Salt output.

Before opening the Unix socket after a restart, the Bridge resolves every
journaled running Salt JID. If the job cache still contains a result, normal
polling resumes. If the JID is missing, the Bridge scans the target and accepts
success only when pinned fingerprints match. Reconcile recovery permits later
unmanaged additions; destructive Apply recovery does not. An unprovable result
is failed/blocked and remains safely retryable.

This startup recovery is intentionally readiness-blocking. It prevents a new
target mutation from racing an unresolved destructive step. With the default
driver, a still-running Salt job may delay Bridge readiness for up to the
10-minute poll timeout.

## Operations

The Bridge supports health, accepted-key node refresh, target scan, preflight,
Apply, edit, Restore, reconcile, backup list/GC, fixed relay controls, and safe
operation lookup. It has no shell or generic execution endpoint.

Apply/edit mirror the manageable scope after a revision check. Reconcile
repairs pinned members and preserves unmanaged additions. Restore first backs
up current content and then restores one cataloged backup. Every write uses
stage, validate, backup, and atomic replacement. A no-op reconcile does not
create a backup.

For Salt targets the Bridge resolves `managedUsername` through the fixed
`user.info` call, validates a canonical non-root home, and injects that home
only into the transient staged bundle. Caller-selected home paths are not an
API capability. Restore verifies the restored pinned Skill/MCP members before
accepting the write; a mismatch atomically rolls back to the recovery backup.

The only allowed relay unit is `toolhub-mcpm-relay.service`; allowed actions are
status, start, stop, and restart plus structured MCP protocol health. Start and
successful Apply enable the unit; Stop disables it. Restart stops the unit,
waits for the configured port to become bindable, and starts it without MCPM
port fallback. Full health initializes one Streamable HTTP session and discovers
advertised tools, resources, templates, and prompts without invoking business
tools. Each discovery request is limited to 30 seconds; a failed method is
retried once within the 90-second total budget so MCPM can finish lazy member
startup, and a second failure remains fail-closed. Running destructive target
work cannot be force-cancelled.

Once backup, registry/profile writes, and integrity validation succeed, Apply
and Restore keep the new relay configuration even if the fixed port, systemd
process, or member namespace probe fails. The Bridge returns a successful target
result with `health=blocked`; write or integrity failures still roll back and
return an operation error.

## Service Checks

```bash
sudo systemctl status toolhub-bridge.service
sudo journalctl -u toolhub-bridge.service --since '-15 minutes'
sudo stat -c '%U:%G %a %n' /run/toolhub-bridge/bridge.sock /var/lib/toolhub-bridge/journal.db
docker compose exec toolhub wget -qO- http://127.0.0.1:18480/healthz
```

`/healthz` reports PostgreSQL readiness and a `bridge` projection. If the
Bridge is unavailable, inspect the service, socket group/GID, key equality,
Salt CLI availability, and recovery logs. Never relax the socket to `0666` or
mount a managed home to work around access failures.
