# Security Model

## Trust Boundary

ToolHub assumes the host and Tailnet are trusted administrative infrastructure. Docker publishes only `127.0.0.1:18480`; Tailscale Serve should terminate HTTPS/WSS. PostgreSQL is attached only to the internal Compose network.

## Credentials

- Passwords use Argon2id with 64 MiB memory, three iterations, random salt, and constant-time verification.
- Session and Agent bearer tokens are stored only as SHA-256 hashes.
- AI keys, MCP environment values, SSH keys, and Agent task keys use XChaCha20-Poly1305 with record ID associated data.
- Agent secret resolution is authorized against the node's desired MCP deployments. An Agent cannot fetch AI keys, SSH keys, or another node's MCP values.
- API responses and audit metadata recursively redact credential-shaped fields.

## Remote Execution

Agent tasks have a closed type set and are HMAC-signed over canonical JSON. The Agent records results for 30 days and returns the previous result for a repeated task ID.

SSH fallback uses a pinned OpenSSH `known_hosts` line, BatchMode, IdentitiesOnly, SFTP upload, and one fixed command: `toolhub-agent run-task --file <validated-temp-path>`. ToolHub does not expose a remote shell API.

## Skill Intake

Archives reject absolute paths, traversal, backslashes, duplicate paths, symlinks, oversized files, and multiple package roots. Review reports expose scripts, executables, URLs, allowed tools, possible credentials, and license presence. Imported content is immutable and remains Library-only until an administrator approves it and assigns targets.

## Deployment Safety

Managed content is cached under `~/.toolhub/artifacts/<sha256>`. Activation stages a full directory, backs up the previous managed directory, and uses rename-based replacement. Existing unmanaged directories and `.system` targets are conflicts, not overwrite candidates.

Before production use, rotate every example credential, enable secure cookies, configure Tailnet ACLs, back up PostgreSQL, and review high-risk Skill findings manually.
