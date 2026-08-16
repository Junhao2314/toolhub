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

ToolHub owns the relay configuration and native client anchors. Relay routing
governance was removed on 2026-08-16: the unit starts mcpm without
`--toolhub-routing`, so the default `/mcp` endpoint exposes every tool from the
current MCP contract. An explicit `?profile=<name>` query is accepted by the
runtime but is not enforced by a published-Profile bundle.

Tests, temporary files, caches, logs, and virtual environments are intentionally
excluded by the repository `.gitignore` and `.dockerignore`.
