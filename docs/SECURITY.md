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
- Observed-only and arbitrary/mismatched MCP profiles are excluded from Agent secret authorization; a fixed profile must first enter the explicit deployment state for its matching runtime.
- Mirrored `shared-file` MCP rows remain node-local observations. When a legacy manifest entry is imported as a central candidate, its values cross only the authenticated one-time capture route, are fingerprint-verified, and are encrypted immediately; ordinary inventory still contains key names only.
- Audit metadata, persisted inventory, and AI inputs recursively redact credential-shaped fields. Secret-bearing browser responses are prevented at their specific store/handler boundaries; there is no universal response-redaction middleware.

## Remote Execution

Agent tasks have a closed type set and are HMAC-signed over canonical JSON. The Agent records results for 30 days and returns the previous result for a repeated task ID.

SSH fallback uses a pinned OpenSSH `known_hosts` line, BatchMode, IdentitiesOnly, SFTP upload, and one fixed command: `toolhub-agent run-task --file <validated-temp-path>`. ToolHub does not expose a remote shell API.

The Nodes page never reads SSH private keys back. Saving a replacement disables the previous active SSH connection and stores the new key as a separate encrypted secret for auditability.

## Skill Intake

Archives reject absolute paths, traversal, backslashes, duplicate paths, symlinks, oversized files, and multiple package roots. Review reports expose scripts, executables, URLs, allowed tools, possible credentials, and license presence. Imported content is immutable and remains Library-only until an administrator approves it and assigns targets.

Discovered runtime Skills remain read-only until an administrator queues adoption. The Agent rejects `.system`, escaped, or symlinked paths; the backend rescans the uploaded canonical ZIP and verifies its discovery hash. The Agent writes the managed marker only after that import succeeds. Shared-source adoption is import-only and intentionally leaves the legacy source unmodified.

## Deployment Safety

Managed content is cached under `~/.toolhub/artifacts/<sha256>`. Activation stages a full directory, backs up the previous managed directory, and uses rename-based replacement. Existing unmanaged directories and `.system` targets are conflicts, not overwrite candidates.

For ToolHub-authoritative MCP, native non-relay discoveries are automatically baselined without rewriting the node. mcpm imports instead create fixed Codex/Claude profiles in `observed` state; an explicit deployment transition is required before the Agent writes anything.

The mcpm delivery boundary is narrow and reversible:

- `apply_mcp` is accepted only for Codex/Claude with the exact fixed profile name. Hermes, Grok, and OpenClaw remain outside the writer.
- Profile membership edits preserve an `observed` deployment instead of making it schedulable. Only the explicit fixed-profile deployment transition can move it to `pending`.
- Secret values are fetched only through `AgentSecretValue` authorization for an enabled desired deployment. They are materialized only into the node-local mcpm registry, which is written at mode `0600`.
- The structured patch preserves unknown servers and fields, refuses to replace an unowned conflicting definition, updates only the selected profile tag, and uses a same-directory fsynced temporary file plus atomic rename.
- Existing mcpm and native anchor files receive timestamped mode-`0600` backups. If the native anchor edit fails, the mcpm write is restored before the task fails.
- Claude/Codex anchor editors preserve unrelated JSON/TOML content and remove only recognized legacy relay sections. ToolHub archives the legacy Codex plugin only after validating its `plugin.json` ToolHub author marker.
- The packaged Linux service keeps `ProtectSystem=strict`, but the enrolled home is writable because same-directory atomic replacement of `~/.claude.json` cannot work through `ProtectHome=read-only`. Runtime paths remain constrained by the closed signed task protocol and Agent-side path validation.

Shared sources are a separate read-only import boundary. Paths and allowed Skill roots come only from local Agent configuration; browser requests and signed tasks never carry arbitrary filesystem paths. Legacy managed/auto-sync settings normalize to observed mode, and the shared writer, watcher, task kind, and CLI command do not exist.

Before production use, rotate every example credential, enable secure cookies, configure Tailnet ACLs, back up PostgreSQL, and review high-risk Skill findings manually.
