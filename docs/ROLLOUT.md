# Rollout Runbook

1. Start ToolHub with a new PostgreSQL volume, a generated 32-byte master key, and a unique bootstrap password.
2. Bind Docker to loopback and configure Tailscale Serve plus Tailnet ACLs. Verify `/healthz` only through intended paths.
3. In Nodes, claim the default `project-host` entry and run its generated Agent command on the machine containing this project. Run a read-only inventory scan and confirm Codex, Claude, Hermes, symlink, protected-skill, and MCP findings before assigning desired state.
4. Import one low-risk test Skill into Library. Review its scripts, URLs, allowed tools, license, source commit, and canonical SHA-256.
5. Approve the Skill. The target matrix lists `project-host` first and preselects only runtimes found in its inventory; confirm that single-node canary and run sync manually.
6. Confirm the Agent created the content-addressed cache, management marker, and deployment backup. Exercise rollback and confirm actual state returns to the prior version.
7. Enable scheduled update checks. Approve one candidate and verify that discovery alone does not mutate desired state.
8. Enable the default `03:30 Asia/Shanghai` sync only after the canary and restore path are verified.
9. Back up PostgreSQL and retain Agent backup directories through the operational rollback window.

Rollback is always a new desired-state transition to the recorded previous approved version. Do not edit ToolHub-managed runtime directories while reconciliation is enabled.
