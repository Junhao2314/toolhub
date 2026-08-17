# Salt Integration

ToolHub uses the host Salt Master through a fixed CLI driver in
`toolhub-bridge`. It requires Salt Master/minions `3008.x`, discovers only
accepted keys, and does not edit `/etc/salt/master`.

## File Root And Assets

The default existing `base` state root is `/srv/salt/states`. Before a remote
Apply, edit, Restore, reconcile, backup removal, or scan that needs ToolHub
extensions, the Bridge publishes embedded assets by content hash:

```text
/srv/salt/states/.toolhub-assets/<sha256>/
/srv/salt/states/_modules/toolhub.py
/srv/salt/states/_states/toolhub.py
/srv/salt/states/toolhub/init.sls
```

Activation writes only these ToolHub namespaced paths through temporary files
and atomic rename. Existing non-ToolHub states and `/etc/salt/master` are not
modified. The driver then calls `saltutil.sync_modules` and
`saltutil.sync_states` for the selected minion.

## Discovery And Capability Gate

Node refresh runs fixed commands equivalent to:

```bash
salt-key --out=json --list=acc
salt --out=json --static --timeout=10 -- MINION test.version
```

Minion IDs must match the driver's bounded literal identifier format. Target
globs, compound matchers, arbitrary Salt functions, and caller-supplied argv are
not accepted. A minion is writable only when `test.version` starts with
`3008.`. Accepted but offline/incompatible nodes remain active and visible as
`unavailable`; transient connectivity does not archive them.

A successful accepted-key refresh is authoritative. A previously active minion
absent from that result becomes `archived`, and its Targets are hidden from
active APIs without deleting node/Target identity, desired snapshots, runtime
inventory, backups, or operation history. Rediscovering the same minion ID
clears the archive marker and restores those original records. If the
accepted-key command fails, ToolHub changes no discovery projection. An empty
successful accepted-key list archives all Salt nodes while leaving the local
node and its four Targets active.

The remote managed username comes from Settings unless a node override is set.
The driver resolves it through the allow-listed `user.info` function. A missing
user or unsafe/non-canonical home blocks all writes.

## Fixed Function Surface

The Go driver allows only:

- `toolhub.scan`
- `toolhub.preflight`
- `toolhub.apply`
- `toolhub.reconcile`
- `toolhub.restore`
- `toolhub.remove_backup`
- `toolhub.cleanup_bundle`
- `user.info` for synchronous identity resolution

Async dispatch cannot call `user.info`. There is no generic module, state,
shell, executable, path, or target-expression endpoint.

## Staging And Async Jobs

Dynamic manifests, Skill archives, edit data, and ephemeral secret values are
serialized into a root-only local bundle below
`/var/lib/toolhub-bridge/staging`. The bundle is bounded by the Bridge request
limit and copied with fixed argv using:

```bash
salt-cp --chunked --out=json -- MINION LOCAL_BUNDLE \
  /var/cache/salt/minion/toolhub-staging/BUNDLE.json
```

Remote mutations dispatch with `salt --async`. The returned JID is persisted
before polling begins. Results are read with fixed
`salt-run jobs.lookup_jid JID`; after timeout, `jobs.list_job JID` distinguishes
a still-known timeout from an expired/missing cache entry.

Both local and minion staging files are removed after a terminal result.
Plaintext secrets and archives are never stored in BoltDB, operation metadata,
audit events, or logs.

## Restart And Missing-JID Recovery

On Bridge restart, persisted JIDs are polled before the socket becomes ready.
If a JID has disappeared, ToolHub never guesses success. It scans the exact
target and compares persisted member fingerprints:

- destructive Apply/edit recovery requires the manageable inventory to match;
- reconcile recovery requires pinned members to match and permits later
  unmanaged additions;
- a mismatch becomes a retryable failed/blocked result.

ToolHub does not depend on `term_job` or `kill_job`. Cancellation applies only
before the control plane dispatches a target; an async destructive target step
runs to its atomic terminal state.

## Runtime Scope

- Claude Skills: managed user's `~/.claude/skills`.
- Codex Skills: managed user's `~/.codex/skills`.
- Claude MCP: top-level user entries in `~/.claude.json`; these native
  inventories are not Profile membership.
- Codex MCP: `mcp_servers` in `~/.codex/config.toml`; these native inventories
  are not Profile membership. The local shared MCP service is edited through
  ToolHub Relay Configuration and owned at runtime by mcpm.
- Hermes: inventory-only; it is never a Library import source and every write
  is rejected.

Hidden/protected Skill entries, `.system`, unsafe file types, and non-user MCP
scopes are excluded. Remote Apply mirrors only the manageable scope. Reconcile
repairs pinned members without deleting later unmanaged entries.

Restore performs the same revision and managed-home checks before creating its
recovery backup. After replacement, the module rescans and verifies every
pinned Skill/MCP member from the backup's desired manifest. A mismatch restores
the recovery backup and returns failure; ToolHub never pins an unverified
restore.

Remote backup data lives under `/var/lib/toolhub/backups` on the minion. Backup
catalog metadata is also stored by the Bridge/control plane. Retention is 30
days and at most 10 items per target.

## Validation

Static module tests do not require a live Salt installation:

```bash
cd /root/docker/toolhub
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s packaging/salt/tests -p '*_test.py'
GOCACHE=/tmp/toolhub-gocache go test ./internal/saltdriver ./internal/bridge
```

Before a real canary, verify connectivity independently:

```bash
sudo salt-key --out=json --list=acc
sudo salt --out=json --static --timeout=10 -- MINION test.version
```

As of 2026-07-30, four of five accepted minions report `3008.x` and are online;
`racknerd-73661c5` still times out and remains unavailable. A read-only
`salt:racknerd/claude` canary passed asset publication, extension sync,
`user.info`, chunked staging, fixed read execution, 64-character target
revision capture, and complete staging cleanup. Destructive Apply/reconcile
remains an explicit rollout step. Do not weaken the version gate or bypass
accepted-key discovery for the unavailable minion.
