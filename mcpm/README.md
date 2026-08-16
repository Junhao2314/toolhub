# Embedded mcpm runtime

This directory contains the ToolHub-owned mcpm Python runtime. It is not a
separate repository or standalone installation target.

Setup from the ToolHub repository root:

```bash
make mcpm-sync
make mcpm-contract
make mcpm-lint
```

The shared relay is started by `toolhub-mcpm-relay.service` from the
installation-owned launcher:

```text
/usr/libexec/toolhub-mcpm
```

ToolHub owns the relay configuration, Profile routing, confirmation policy,
and native client anchors. The default `/mcp` endpoint exposes all tools from
the current MCP contract subject to global deny/pause policy. An explicit
`?profile=<name>` selects a published Profile; unknown Profiles fail closed.

Tests, temporary files, caches, logs, and virtual environments are intentionally
excluded by the repository `.gitignore` and `.dockerignore`.
