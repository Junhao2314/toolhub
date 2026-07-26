# Rollout Runbook

1. Start ToolHub with a new PostgreSQL volume, a generated 32-byte master key, and a unique bootstrap password.
2. Bind Docker to loopback and configure Tailscale Serve plus Tailnet ACLs. Verify `/healthz` only through intended paths.
3. In Nodes, claim the default `project-host` entry and run its generated Agent command on the machine containing this project. Connection immediately runs inventory; confirm Codex, Claude, Hermes, symlink, protected-Skill, and MCP findings. MCP is automatically captured and baselined without rewriting the existing configuration.
4. From Skills → Discovered, adopt one low-risk local Skill. Confirm the snapshot enters pending review and the Agent writes the managed marker only after import succeeds. Alternatively import a Git/ZIP Skill into Library.
5. Approve the Skill. The target matrix lists `project-host` first and preselects only runtimes found in its inventory; confirm that single-node canary and run sync manually.
6. Confirm the Agent created the content-addressed cache, management marker, and deployment backup. Exercise rollback and confirm actual state returns to the prior version.
7. Enable scheduled update checks. Approve one candidate and verify that discovery alone does not mutate desired state.
8. Enable the default `03:30 Asia/Shanghai` reconciliation only after the canary and restore path are verified. It enqueues both Skill and MCP sync; inventory refreshes every six hours independently.
9. Back up PostgreSQL and retain Agent backup directories through the operational rollback window.

Rollback is always a new desired-state transition to the recorded previous approved version. Do not edit ToolHub-managed runtime directories while reconciliation is enabled.
