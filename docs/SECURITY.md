# Security Model

## Identity And Sessions

PostgreSQL enforces one `account` row. Usernames are normalized lowercase
identifiers; passwords are Argon2id (`m=65536 KiB`, `t=3`, `p=2`, 16-byte salt,
32-byte key) and must be 12-1024 characters.

Login uses a dummy hash for unknown usernames and a per-IP limit of 10 attempts
per 10 minutes. Session and CSRF tokens are random; only SHA-256 hashes are
stored. The cookie is HttpOnly, SameSite=Strict, and Secure by default. Changing
username or password revokes all sessions. There is no RBAC or second account.

## Secret Storage

`TOOLHUB_MASTER_KEY` is a 32-byte key used by XChaCha20-Poly1305. Each encrypted
secret uses its record UUID as AAD. MCP browser responses contain key names only.
Plaintext is accepted only on write, decrypted only for an authorized active
manifest, sent ephemerally to the Bridge/Salt bundle, cleared from worker maps,
and never returned to the browser.

Desired snapshot manifests may contain secret references only. Bridge BoltDB
rejects values named `secretValues`, archives, editable content, plaintext, or
raw output. Audit metadata is redacted before persistence. There is no universal
response redaction middleware, so every new response must be reviewed at its
call site.

The only browser download containing plaintext values is the separately named
`.toolhub-profile-secrets` export. It requires current-password
reauthentication, exports only Secrets referenced by one pinned Profile
revision, sets `Cache-Control: no-store`, and never persists the Bundle bytes,
values, or password in PostgreSQL, BoltDB, operations, audit, or logs. Standard
`.toolhub-profile` Bundles contain slot names and no values.

## Bridge Boundary

The Bridge listens only on `/run/toolhub-bridge/bridge.sock` at mode `0660` with
a fixed shared GID. It runs as root; ToolHub runs unprivileged and mounts only
the socket directory, never a managed home.

Every request uses a dedicated 32-byte HMAC key and signs:

```text
UPPERCASE_METHOD\nREQUEST_URI\nUNIX_TIMESTAMP\nNONCE\nSHA256(BODY)
```

The accepted clock skew is 30 seconds. Nonces are persisted before dispatch and
replay is rejected. Mutations are idempotent by caller key and request hash. The
Bridge persists the hash and operation ID before dispatch, then records only a
safe terminal result. Restart replay returns that result without repeating the
adapter mutation.
The Bridge API exposes typed operations only: no arbitrary shell, executable,
filesystem path, Salt function, or systemd unit can cross the boundary.

Browser governance handlers treat even authenticated Bridge responses as
untrusted. Confirmation summaries and live observations are accepted only when
their UUIDs/hashes, timestamps, enum values, names, reason codes, JSON pointers,
and collection sizes match the private contract. Invalid safe DTOs return `502`;
Bridge transport failures return `503`. The Browser allowlist contains only
identity/revision hashes, bounded names and reason codes, scalar type/length
summaries, decisions, outcome classes, and duration buckets. Arguments, results,
prompts, raw errors, secret values, and session identifiers are never forwarded.
Argument-summary pointers use only anonymous structural ordinals (`/oN` for an
object member and `/aN` for an array member), never original object keys.

Confirmation challenges and one-shot 60-second grants live only in the mcpm
Relay process. They are not stored in PostgreSQL, desired snapshots, operations,
or Bridge BoltDB. Approval requires the exact Profile display name and binding
hash, rechecks the current Profile revision, and sends only the challenge ID and
binding hash across the Bridge. It requires session authentication and CSRF but
not password reauthentication. The synchronous audit record is payload-free and
limited to challenge/binding/argument hashes, Profile/revision/server/tool IDs,
reason codes, and the decision outcome. A successful Relay decision is withheld
from the Browser if that synchronous audit record cannot be persisted. A
post-dispatch transport failure or a
decision response whose challenge/binding does not match is audited and returned
as an unknown outcome, never retried automatically. The Relay consumes a grant
before dispatch and never restores it, so a confirmed high-risk call is sent at
most once. Only an adapter-proven pre-dispatch failure is `not_executed`; any
unproven transport failure after the boundary is `execution_unknown` and
requires state inspection before a manual retry.

The typed session canary is bound to the canonical hash of an `enforced`
candidate routing bundle. It checks Claude/Codex catalog binding,
missing/unknown Profile behavior, concurrency, and the existing shared process
counts without invoking a business tool. Its HMAC response is ephemeral and is
not stored in BoltDB or PostgreSQL.

Local MCP intake requires a separate revision-bound confirmation. Its preview
returns sanitized transport fields and secret key names only. After confirmation,
the Bridge reads the matching native user-scope entry once; the worker encrypts
the captured values immediately. The capture response bypasses BoltDB and is
forbidden from browser responses, operation metadata/results, audit events, and
logs.

BoltDB is mode `0600`. It stores nonce/idempotency records, safe operation and
target steps, Salt JIDs, recovery fingerprints, fixed staging paths, and backup
catalog entries. Terminal result persistence strips manifests and editable
details; remote `managedHome` exists only in transient delivery DTOs. BoltDB
does not store secret values, archives, editable config, or raw Salt output.

## Filesystem Safety

Local and Salt adapters resolve the managed user through OS account lookup and
reject missing users, `/` homes, symlink homes, traversal, escaped realpaths,
symlinks, devices, oversized/binary unsafe input, and protected names. Writes use
same-filesystem staging, validation, fsync, backup, and atomic rename/replace.
Every managed parent is checked for symlinks before scan, stage, backup,
restore, or atomic replacement. Salt independently resolves the managed user
with `user.info` and repeats canonical-home and symlink-parent checks on the
minion.

The Bridge service hides other home directories with a private tmpfs and binds
only the configured managed home into its mount namespace. Local symlinks inside
protected inventory are never followed or managed and are preserved as symlink
objects across backup, Apply, and Restore. Symlinks in managed Skill artifacts
and managed parent paths remain rejected.

Skills reject absolute/backslash/traversal paths, symlinks, unsafe types,
oversized archives/files, and multiple package roots. Artifacts are rescanned and
must match their pinned canonical SHA-256 before each write.

## Salt

Only accepted keys are discovered. Every target must report Salt 3008.x.
Commands use fixed `exec.CommandContext` argv and allow-listed functions. Dynamic
payloads are root-only staged and sent with `salt-cp --chunked`; they are removed
after terminal handling. Missing/expired async JIDs are never assumed successful:
the target is rescanned and must match the persisted pinned-member fingerprints.

ToolHub writes only its namespace below `/srv/salt/states`; it never edits
`/etc/salt/master` or remote Hermes-managed content. On the local node, the
`local/shared-relay` operation atomically owns only the Hermes
`mcp_servers.toolhub-relay` consumer anchor; the `local/hermes` target remains
read-only for Skills and inventory.

## MCP Scope

Local MCP requires a compatible preinstalled `/usr/bin/mcpm` whose machine-
readable ToolHub contract advertises admin protocol 1, routing schema 1, and all
required governance features. The installer and runtime fail closed; neither
auto-finds, installs, upgrades, nor falls back to native per-server local
delivery. ToolHub manages one profile named `toolhub`, one fixed relay unit, one
atomic routing file at `~/.config/mcpm/toolhub-routing.json`, one admin socket at
`/run/toolhub-mcpm/relay.sock`, and one `toolhub-relay` user-scope anchor in
`~/.claude.json`, `~/.codex/config.toml`, and `~/.hermes/config.yaml`. Local
Hermes' existing `mcp_servers` map is collapsed on Apply; reconcile preserves
later unmanaged entries.

The relay unit hides all homes with a private tmpfs and binds back only the
selected canonical managed home as writable. Its private runtime directory,
admin socket, and files are created under a `UMask=0077`; the socket protocol is
one-line, size/deadline bounded, and exposes fixed typed operations only.
`ProtectSystem=strict`, an empty capability bounding set, private devices,
kernel/control-group protections, and the address-family allowlist remain
active. `MemoryDenyWriteExecute` is not used because supported Node/V8 MCPs
require JIT memory. A managed user with Docker socket access, including a root
managed user, is equivalent to host control; this is an accepted operational
trust boundary, not strong process isolation.

Each managed mcpm registry entry carries its Library content hash plus a runtime
integrity hash. Relay scans project the Library hash only while the actual entry,
including its ephemeral secret values, still matches that integrity hash;
otherwise they return a drift hash. Inventory never returns plaintext values.

Payload-free live observations are held only in the mcpm process, capped at
100,000 entries and 24 hours. ToolHub stores daily aggregates rather than raw
events and deletes aggregate buckets older than 30 days. Neither retention tier
contains arguments, results, prompts, raw errors, Secret values, or persistent
session identifiers.

Remote Claude writes only top-level user `mcpServers` in `~/.claude.json`.
Remote Codex writes only `mcp_servers` in `~/.codex/config.toml`. Claude
project/local/managed/plugin scopes and protected entries are excluded.

## Operational Requirements

Keep the HTTP port loopback-only, terminate remote access with authenticated
HTTPS and Tailnet ACLs, keep secure cookies enabled, rotate example credentials,
restrict the Bridge group, back up the PostgreSQL volume/runtime homes/Salt tree,
and review Skill scan reports before Apply.
