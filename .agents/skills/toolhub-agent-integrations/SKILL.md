---
name: toolhub-agent-integrations
description: "Extend or troubleshoot ToolHub generation-2 host and external integrations: root-owned HMAC Unix Bridge, guarded local runtime operations, Salt 3008.x accepted-key discovery and async JID delivery, shared mcpm relay/systemd packaging, Skill intake, marketplaces, reconcile, and backup recovery. Use when work crosses process, node, filesystem, or service boundaries."
---

# ToolHub Bridge And Integrations

The directory name is retained for compatibility; ToolHub has no Agent. Read
`AGENTS.md`, `docs/BRIDGE.md`, `docs/SALT.md`, `docs/SECURITY.md`,
`cmd/toolhub-bridge/main.go`, `internal/bridge`, `internal/bridgeprotocol`,
`internal/runtime`, `internal/saltdriver`, `internal/worker`, and the relevant
systemd/Salt packaging.

Preserve the Bridge boundary:

- HTTP only over `/run/toolhub-bridge/bridge.sock`, mode `0660`, fixed group;
- independent 32-byte HMAC over method, URI, timestamp, nonce, body hash;
- 30-second window, durable nonce replay rejection and idempotency;
- typed operations only; no arbitrary path, executable, unit, Salt function,
  target expression, shell, or TCP listener;
- mode-`0600` BoltDB with safe operation/JID/fingerprint/backup metadata only.

Preserve Salt behavior:

- discover accepted keys only and require `test.version` `3008.x`;
- publish `_modules/toolhub.py`, `_states/toolhub.py`, and `toolhub/init.sls`
  by content hash under the existing `/srv/salt/states` base root;
- run fixed argv via `exec.CommandContext` and parse aggregate or streaming JSON;
- sync modules/states, stage root-only bundles with `salt-cp --chunked`, dispatch
  per-minion async JIDs, poll `jobs.lookup_jid`, and inspect `jobs.list_job`;
- persist JID/recovery fingerprints before polling and clean both staging paths;
- rescan on missing JID; permit unmanaged extras only for reconcile recovery;
- never depend on kill/term job for cancellation.

Preserve local/relay behavior:

- resolve a real non-symlink managed home through OS lookup;
- use guarded known paths and protected entry rules;
- backup, stage, validate, and atomically replace per target/runtime;
- manage only `toolhub-mcpm-relay.service` and mcpm Profile `toolhub`;
- require preinstalled compatible mcpm and a fixed available port;
- rollback registry/profile/anchors/environment and restart the old relay when
  new health fails;
- honor persistent intentional pause during reconcile.

Hermes stays read-only. Marketplace/update discovery may import immutable
artifacts but never Apply automatically.

Add negative tests for traversal/symlink/type/size, allowlist bypass, version
gate, streaming JSON, JID loss, staging cleanup, relay port conflict, rollback,
pause intent, and journal redaction as relevant.

Verify:

```bash
GOCACHE=/tmp/toolhub-gocache go test ./internal/bridge ./internal/bridgeprotocol ./internal/runtime ./internal/saltdriver ./internal/worker ./internal/skills ./internal/market
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s packaging/salt/tests -p '*_test.py'
GOCACHE=/tmp/toolhub-gocache go test ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
```
