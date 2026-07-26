# ToolHub

ToolHub is a Tailnet-only operations console for managing Codex, Claude, and Hermes Skills and MCP configuration across multiple nodes. It ships as one Go control-plane container with an embedded React UI, one PostgreSQL container, and a cross-platform Go Agent.

Licensed under the [MIT License](LICENSE).

## What Is Implemented

- Agent-first WSS enrollment, heartbeat, inventory, signed typed tasks, offline queues, retry, cancellation, and pinned SSH/SFTP fallback.
- Six-hour inventory for Codex, Claude, and Hermes homes, including symlink/protected-Skill reporting, manual Skill adoption, and automatic MCP takeover.
- Immutable Skill artifacts with canonical SHA-256, source commit, provenance, ZIP/Git path safety, review, target matrix, update approval, sync, and per-node rollback.
- SkillsMP search proxy with rate-limit messaging and fixed-commit Git import; OpenAI-compatible structured recommendations never install automatically.
- MCP server/profile/deployment management with automatic first-seen baselines, encrypted one-time secret capture, drift detection, native `mcpm` preference, structured fallback patches, and atomic backups.
- HttpOnly server-side sessions, Argon2id, CSRF rotation, Admin/Operator/Viewer RBAC, encrypted secrets, redaction, and audit events.

## Quick Start

Requirements: Docker with Compose v2 and a host already connected to Tailscale.

```bash
git clone https://github.com/Junhao2314/toolhub.git
cd toolhub
cp .env.example .env
openssl rand -base64 32
# Put the generated value in TOOLHUB_MASTER_KEY and set a unique admin username/password.
docker compose up -d --build --wait
curl --fail http://127.0.0.1:18480/healthz
```

Open `http://127.0.0.1:18480` for local setup. Production cookies default to `Secure`; the local HTTP smoke profile explicitly sets `TOOLHUB_SECURE_COOKIES=false`.

ToolHub creates a pending `project-host` node on first startup. Set `TOOLHUB_LOCAL_NODE_NAME` when the project machine should use a different display name. Nodes -> Enroll project host produces the exact one-time Agent command to run on that machine.

Expose the loopback listener with Tailscale Serve according to the installed Tailscale version. Keep Docker bound to `127.0.0.1:18480`; do not publish the port on a public interface. Set `TOOLHUB_PUBLIC_URL` to the resulting Tailnet HTTPS URL so enrollment commands and WSS origins are correct.

## Agent

Create a node enrollment token in the UI, then run on the target:

```bash
toolhub-agent enroll --server https://toolhub.your-tailnet.ts.net --token '<one-time-token>'
toolhub-agent run
```

Service templates are under `packaging/systemd`, `packaging/launchd`, and `packaging/windows`. Enrollment immediately scans existing runtime homes. Existing Skills remain read-only until an administrator adopts one from the Discovered view; the Agent writes the management marker only after the backend imports and verifies the snapshot.

Existing MCP servers are automatically taken over. The Agent sends normalized commands, arguments, URLs, environment key names, and per-node HMAC fingerprints through the authenticated channel. It uploads plaintext environment values only when the backend issues a short-lived one-time capture token. The values are encrypted immediately and never enter browser APIs, inventory JSON, jobs, audit metadata, or logs. The first scan is a no-rewrite baseline; later local edits or deletion are drift, and ToolHub restores central desired state during reconciliation.

Nodes also provides an SSH fallback form. It accepts `user@host`, one pinned `known_hosts` line, and a private key. The key is encrypted before storage; fallback permits only signed task upload and the fixed Agent task runner. Once the project-host inventory is online, its discovered runtimes are preselected in the Skill target matrix as the default single-node canary.

## Development

```bash
go test ./...
cd web && npm ci --ignore-scripts && npm run typecheck && npm run build
cd .. && make docker-config
```

The Vite development server uses `127.0.0.1:18481` and proxies API requests to the control plane on `18480`.

## Operations

- Update checks discover upstream commit/content changes and build a reviewable candidate. They never change desired state.
- Admin approval marks the candidate version approved and advances existing deployments to that desired version.
- Reconcile writes approved Skill desired state and centrally managed MCP desired state. Offline nodes retain pending tasks for the next Agent connection.
- Inventory runs on Agent connection and every six hours. Global defaults are update checks at `02:00` and Skill + MCP reconciliation at `03:30 Asia/Shanghai`; more-specific policy scopes take precedence.
- Deleted Skills and Nodes are archived. Skill artifact purge starts only after 30 days.

See [Security](docs/SECURITY.md), [API](docs/API.md), and [Rollout](docs/ROLLOUT.md).
