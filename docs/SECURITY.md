# Security Model

## Trust Boundary

ToolHub assumes the host and Tailnet are trusted administrative infrastructure. Docker publishes only `127.0.0.1:18480`; Tailscale Serve should terminate HTTPS/WSS. PostgreSQL is attached only to the internal Compose network.

## Credentials

- Passwords use Argon2id with 64 MiB memory, three iterations, random salt, and constant-time verification.
- Browser login accepts a case-insensitive username or email identifier and returns one uniform invalid-credentials error.
- Random temporary passwords use the operating system CSPRNG, are returned once, and are never written to audit metadata or logs. Username/password changes revoke all sessions for the target user.
- Session and Agent bearer tokens are stored only as SHA-256 hashes.
- AI keys, MCP environment values, SSH keys, and Agent task keys use XChaCha20-Poly1305 with record ID associated data.
- MCP inventory contains normalized non-secret descriptors and environment key names only. Secret comparison uses HMAC-SHA256 under the per-node task key; unknown MCP values are requested through an expiring, one-time, node/identity-bound capture token and encrypted immediately.
- Agent secret resolution is authorized against the node's desired MCP deployments. An Agent cannot fetch AI keys, SSH keys, or another node's MCP values.
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

MCP is the intentional exception to manual onboarding: newly discovered MCP servers are automatically baselined without rewriting the node. After baseline, ToolHub owns desired state and local edits/deletion are drift restored by manual or scheduled reconciliation.

Before production use, rotate every example credential, enable secure cookies, configure Tailnet ACLs, back up PostgreSQL, and review high-risk Skill findings manually.
