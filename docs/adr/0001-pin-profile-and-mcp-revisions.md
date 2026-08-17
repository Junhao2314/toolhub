# Superseded: Pin Profile And MCP Revisions

Status: superseded on 2026-08-17 by
`docs/superpowers/specs/2026-08-17-toolhub-mcpm-mcp-profile-boundary.md`.

The original decision treated MCP revisions as Profile membership. That owner
boundary is retired. Profiles now pin immutable Skill versions only; the
ToolHub MCP/Relay Configuration revision pins MCP revisions, while mcpm owns
the shared registry, relay, and upstream process lifecycle.

The immutable-pin and explicit-refresh principles remain valid for both owners,
but MCP pins must never be added to a Profile revision or Profile bundle.
