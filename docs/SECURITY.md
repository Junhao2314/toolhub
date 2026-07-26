# Security Model

## Trust Boundary

ToolHub assumes the host and Tailnet are trusted administrative infrastructure. Docker publishes only `127.0.0.1:18480`; Tailscale Serve should terminate HTTPS/WSS. PostgreSQL is attached only to the internal Compose network.

## Credentials

- Passwords use Argon2id with 64 MiB memory, three iterations, random salt, and constant-time verification.
- Browser login accepts a case-insensitive username or email identifier and returns one uniform invalid-credentials error.
- Random temporary passwords use the operating system CSPRNG, are returned once, and are never written to audit metadata or logs. Username/password changes revoke all sessions for the target user.
- Session and Agent bearer tokens are stored only as SHA-256 hashes.
- AI keys, centrally managed MCP environment/header values, SSH keys, and Agent task keys use XChaCha20-Poly1305 with record ID associated data.
- MCP inventory contains normalized non-secret descriptors plus environment/header key names only. Secret comparison uses HMAC-SHA256 under the per-node task key; unknown MCP values are requested through an expiring, one-time, node/identity-bound capture token and encrypted immediately.
- Agent secret resolution permits only `mcp-env` and `mcp-header` secrets referenced by an enabled, desired, ToolHub-authoritative MCP deployment on that node. An Agent cannot fetch AI keys, SSH keys, disabled/unreferenced MCP values, or another node's values.
- Shared-file MCP values remain on the node. The Agent reports only normalized key names and keyed fingerprints; inline environment or header values from the shared manifest are never sent to the control plane.
- Audit metadata, persisted inventory, and AI inputs recursively redact credential-shaped fields. Secret-bearing browser responses are prevented at their specific store/handler boundaries; there is no universal response-redaction middleware.

## Remote Execution

Agent tasks have a closed type set and are HMAC-signed over canonical JSON. The Agent records results for 30 days and returns the previous result for a repeated task ID.

SSH fallback uses a pinned OpenSSH `known_hosts` line, BatchMode, IdentitiesOnly, SFTP upload, and one fixed command: `toolhub-agent run-task --file <validated-temp-path>`. ToolHub does not expose a remote shell API.

The Nodes page never reads SSH private keys back. Saving a replacement disables the previous active SSH connection and stores the new key as a separate encrypted secret for auditability.

## Skill Intake

Archives reject absolute paths, traversal, backslashes, duplicate paths, symlinks, oversized files, and multiple package roots. Review reports expose scripts, executables, URLs, allowed tools, possible credentials, and license presence. Imported content is immutable and remains Library-only until an administrator approves it and assigns targets.

Discovered runtime Skills remain read-only until an administrator queues adoption. The Agent rejects `.system`, escaped, or symlinked paths; the backend rescans the uploaded canonical ZIP and verifies its discovery hash. The Agent writes the managed marker only after that import succeeds.

## Deployment Safety

Managed content is cached under `~/.toolhub/artifacts/<sha256>`. Activation stages a full directory, backs up the previous managed directory, and uses rename-based replacement. Existing unmanaged directories and `.system` targets are conflicts, not overwrite candidates.

For ToolHub-authoritative MCP, newly discovered servers are automatically baselined without rewriting the node. After baseline, ToolHub owns desired state and local edits/deletion are drift restored by manual or scheduled reconciliation.

Shared-file sources use a separate node-local authority boundary:

- Filesystem paths and allowed Skill roots come only from the local Agent configuration; browser requests and signed tasks identify a configured source and never carry arbitrary paths or shell commands.
- Auto-probe is observed-only. Writes require an explicit `sharedSources` entry in `managed` mode; `autoSync` is rejected outside managed mode.
- The manifest and existing MCP targets must already be mode `0600` before a managed write. Generated files, ownership state, temporary files, and retained backups are written with restrictive permissions.
- Skill reconciliation records ownership and changes only links it previously created. Real directories, unknown links, escaped source links, and locally modified stale links produce conflicts instead of being overwritten or removed.
- MCP reconciliation preserves unknown top-level fields and unknown server entries. Each write compares the whole file with the scanned bytes, validates managed-entry fingerprints, writes and fsyncs a same-directory temporary file, and atomically replaces the target. Concurrent or out-of-band changes produce conflicts.
- Before replacing an existing MCP target, the Agent stores a timestamped last-known-good copy under its data directory and retains the five newest backups per source/consumer.
- Grok is validated as inheriting Claude's concrete MCP output. Renderer merges leave Hermes local-only entries such as `task-trellis` and `acemcp` outside the managed set.

Before production use, rotate every example credential, enable secure cookies, configure Tailnet ACLs, back up PostgreSQL, and review high-risk Skill findings manually.
