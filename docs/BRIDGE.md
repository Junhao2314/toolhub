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

Preflight responses bind a SHA-256 target revision and canonical manifest hash
to four non-null diff collections. Diff entries are bounded, typed as `skill`
or `mcp`, except that protected regular files use `entry` in the `excluded`
collection. Inventory names are bounded to the Linux 255-byte basename limit.
Add/replace entries reference an exact desired member ID; only excluded
protected entries carry the `protected` reason.

For Salt targets the Bridge resolves `managedUsername` through the fixed
`user.info` call, validates a canonical non-root home, and injects that home
only into the transient staged bundle. Caller-selected home paths are not an
API capability. Restore verifies the restored pinned Skill/MCP members before
accepting the write; a mismatch atomically rolls back to the recovery backup.

The only allowed relay unit is `toolhub-mcpm-relay.service`; allowed actions are
status, start, stop, and restart plus structured relay health. Start and
successful Apply enable the unit; Stop disables it. Restart stops the unit,
waits for the configured port to become bindable, and starts it without MCPM
port fallback. Full health checks the fixed admin socket capability, exact
routing status, and normalized upstream contract observation. It never creates
a synthetic client session or invokes a business tool. Running destructive
target work cannot be force-cancelled.

Apply and Restore back up the registry, three native anchors, environment, and
routing bundle before any write. A routing-only change uses atomic replacement
plus admin hot reload and does not restart upstream processes. Runtime changes
use the fixed restart path. A reload, restart, or full-health failure restores
the complete old file set and old process state, then returns a failed target;
desired and Published pointers must not advance.

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

## Relay Governance Boundary

The private governance routes are typed and remain on the same HMAC Unix
socket: capability discovery, routing-bundle reload, contract observation,
confirmation approve/reject, payload-free observation drain, and native-client
inspection. They do not provide a generic action or body proxy.

Native-client inspection accepts only the managed username and `claude` or
`codex` client kind. The Bridge scans only managed-home `~/.local/bin`,
`~/.nvm/versions/node/*/bin`, and `~/.volta/bin` entries plus `/usr/bin` and
`/usr/local/bin`, rejects escaped
symlinks and non-regular executables, and runs only `--version` as the managed
UID/GID with a clean environment, bounded output, and a five-second deadline.
Claude Code must be at least `2.1.232` and Codex CLI at least `0.147.0`.
Responses contain only client kind, canonical semantic version, supported
state, and a bounded reason code; binary paths and argv never cross the Bridge
boundary. Inspection enumerates every approved candidate and deduplicates
aliases by device and inode. More than one distinct executable fails closed as
`native_client_resolution_ambiguous`. Shell aliases/functions and custom PATH
entries are part of the external launch environment and cannot be verified by
ToolHub.

The relay admin protocol is a separate bounded one-line JSON protocol at the
fixed `/run/toolhub-mcpm/relay.sock`. The systemd unit passes the fixed routing
file `~/.config/mcpm/toolhub-routing.json`; neither path is caller-selectable.
The installer requires a compatible preinstalled `/usr/bin/mcpm` contract and
does not download, install, or update mcpm.

Routing bundles contain only immutable revision IDs and hashes, accepted
contract/tool identities, visibility/risk rules, and the applied policy
revision. Manifest v2 is valid only for `local/shared-relay` and binds the
canonical routing hash to the relay configuration hash. Manifest v1 remains
readable for compatibility restores.

Governance responses never contain call arguments, results, prompts, raw
errors, persistent session IDs, Secret values, archives, or editable MCP
configuration. Confirmation summaries contain only exact revision bindings,
hashes, reason codes, and structural argument summaries. Observation drain uses
`afterBootId`/`afterSequence`, returns at most 1000 payload-free events, and
carries bounded outcomes, error classes, duration buckets, and minute buckets.
Confirmation approval returns a finite grant expiry no more than 60 seconds in
the future; rejection omits the grant expiry entirely.
Mutation routes retain timestamp/nonce replay protection and idempotency
semantics.
