# Rollout Runbook

1. Start ToolHub with a new PostgreSQL volume, a generated 32-byte master key, and a unique bootstrap password.
2. Bind Docker to loopback and configure Tailscale Serve plus Tailnet ACLs. Verify `/healthz` only through intended paths.
3. In Nodes, claim the default `project-host` entry and run its generated Agent command on the machine containing this project. For the packaged Linux service, enroll with `--config /etc/toolhub-agent/agent.json`, an explicit runtime `--home`, and `--data-dir /var/lib/toolhub-agent`; keep that home aligned with the service `User=` and writable runtime-path exceptions. Connection immediately runs inventory; confirm Codex, Claude, Hermes, symlink, protected-Skill, and MCP findings. MCP is automatically captured and baselined without rewriting the existing configuration.
4. From Skills → Discovered, adopt one low-risk local Skill. Confirm the snapshot enters pending review and the Agent writes the managed marker only after import succeeds. Alternatively import a Git/ZIP Skill into Library.
5. Approve the Skill. The target matrix lists `project-host` first and preselects only runtimes found in its inventory; confirm that single-node canary and run sync manually.
6. Confirm the Agent created the content-addressed cache, management marker, and deployment backup. Exercise rollback and confirm actual state returns to the prior version.
7. Enable scheduled update checks. Approve one candidate and verify that discovery alone does not mutate desired state.
8. Enable the default `03:30 Asia/Shanghai` reconciliation only after the canary and restore path are verified. It enqueues both Skill and MCP sync; inventory refreshes every six hours independently.
9. Back up PostgreSQL and retain Agent backup directories through the operational rollback window.

Rollback is always a new desired-state transition to the recorded previous approved version. Do not edit ToolHub-managed runtime directories while reconciliation is enabled.

## Shared-source staged rollout

Shared-source support is node-scoped. Omitting `sharedSources` allows read-only auto-probe when the default shared directory or manifest exists; it does not authorize writes. Before enabling reconciliation, add an explicit source to the Agent configuration with the real enrolled home and paths. The minimum managed set is Claude + Codex; add Hermes, Grok, or OpenClaw only when those runtimes are intentionally part of the rollout. Start with:

```json
{
  "sharedSources": [
    {
      "name": "root-shared",
      "mode": "observed",
      "autoSync": false,
      "skillsRoot": "/root/.shared/skills",
      "mcpManifest": "/root/.shared/mcp/servers.json",
      "allowedSkillRoots": [
        "/root/.shared/skills",
        "/root/.agents/skills",
        "/root/.shared/vibe-skills"
      ],
      "consumers": {
        "codex": {
          "skillsPath": "/root/.codex/skills",
          "mcpPath": "/root/.codex/.tmp/plugins/plugins/shared-mcp/.mcp.json",
          "mcpFormat": "codex-plugin-json"
        },
        "claude": {
          "skillsPath": "/root/.claude/skills",
          "mcpPath": "/root/.claude/settings.json",
          "mcpFormat": "claude-settings-json"
        }
      }
    }
  ]
}
```

Merge that block into the existing JSON; do not replace enrollment credentials or runtime paths. Then use this sequence:

1. Record hashes, modes, owners, and symlink targets for the manifest and all consumers. Make secure, timestamped backups without following Skill links, and confirm no cron, timer, or legacy generator is writing concurrently.
2. Restart the Agent in `observed` mode and confirm the shared source plus the configured Claude/Codex consumers appear in inventory. Optional consumers appear only when explicitly configured; no filesystem write is allowed in this mode.
3. Run the same reconciler locally in dry-run mode:

   ```bash
   toolhub-agent sync-shared --config /etc/toolhub-agent/agent.json --source root-shared --scope all --dry-run
   ```

   Resolve every reported conflict. Confirm unknown/local MCP entries are preserved, especially Hermes `task-trellis` and `acemcp`.
4. With explicit human approval, secure the shared manifest and each existing MCP target to mode `0600`. The Agent reports insecure permissions during dry-run and refuses a managed write rather than changing an existing file's mode implicitly.
5. Change the source to `"mode": "managed"` while keeping `"autoSync": false`, restart the Agent, repeat the dry-run, and apply MCP first:

   ```bash
   toolhub-agent sync-shared --config /etc/toolhub-agent/agent.json --source root-shared --scope mcp
   ```

   Verify enabled/disabled servers, native file syntax, Grok's Claude inheritance, and the preserved local entries before applying Skill links:

   ```bash
   toolhub-agent sync-shared --config /etc/toolhub-agent/agent.json --source root-shared --scope skills
   ```

6. Verify the Claude/Codex link sets and any explicitly configured optional consumers, then review the shared-source status in ToolHub. A whole-file compare-and-swap or managed-entry mismatch is a conflict; inspect the local edit and rerun rather than forcing an overwrite.
7. After one manual canary and at least one periodic inventory cycle, set `"autoSync": true` and restart the Agent. The existing Agent process watches only the manifest and the top level of the shared Skills directory, debounces events, serializes each source with a lock, and reports a fresh redacted inventory after successful reconciliation.

Existing MCP target replacements create `0600` last-known-good backups under `<dataDir>/backups/shared/<source>/<consumer>/`; the five newest files are retained per consumer. Shared ownership state lives under `<dataDir>/shared/` and must be included in Agent data backups.

### Shared-source rollback

1. Set `"mode": "observed"` and `"autoSync": false`, then restart the Agent before touching any consumer file.
2. Cancel only pending `shared_sync` jobs or `sync_shared` tasks. Leave unrelated Skill and MCP work alone.
3. Restore MCP targets only from verified pre-rollout or Agent backups, using the recorded hashes and permissions.
4. Restore or remove only links recorded as ToolHub-owned; never bulk-delete a consumer Skills directory or alter the canonical shared source.
5. Re-enable a legacy generator only after confirming the Agent watcher is inactive. Migration `004_shared_sources.sql` is additive and has no down migration.
