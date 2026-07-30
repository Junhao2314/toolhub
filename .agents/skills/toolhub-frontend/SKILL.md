---
name: toolhub-frontend
description: Develop and debug ToolHub generation-2 React/Vite UI, manual routing, username-only auth, unified Profile preflight/Apply, target inventory/edit/Restore, operation retry/cancel, shared relay controls, write-only MCP secrets, responsive layout, and Playwright smoke behavior.
---

# ToolHub Frontend

Read `AGENTS.md`, `web/src/App.tsx`, `web/src/api/client.ts`,
`web/src/hooks/useData.ts`, `web/src/components/ui.tsx`, `web/src/styles.css`,
the target page, and `web/e2e/smoke.spec.ts` for workflow/layout changes.

Use the existing React 18 + strict TypeScript + Vite stack. Keep manual
`history.pushState` routing, the `api` singleton, `useData`, shared UI
primitives, lucide icons, and the single stylesheet. Do not add React Router,
a second HTTP/state/form/modal system, CSS-in-JS, or bare `fetch`.

Navigation is exactly Overview, Skills, Marketplace, MCP, Profiles, Targets,
Operations, Settings, and Account. Authentication is username/password only.
There are no roles, users/access page, Agent/nodes onboarding, jobs,
deployments, approval queue, or Profile activation.

Preserve these workflows:

- Profile membership combines Skill and MCP IDs. Preflight shows per-target
  add/replace/delete/excluded and Apply sends one-use confirmation tokens.
- Targets distinguish `local/shared-relay` MCP from runtime-specific Skills,
  show managed/unmanaged/protected inventory, active snapshot revision, health,
  backups, edit, Restore, and relay actions.
- Operations show fleet partial results, per-target errors, cancel, and
  failed-only retry.
- MCP secret rows use a key plus `type=password` value. Empty retains an
  existing key, removal deletes it, duplicates are rejected, and responses
  never contain values.
- Responsive desktop/mobile layouts must not overflow or overlap.

Every action needs loading/busy, API error, success/reload, and empty states.
Use icon buttons for familiar controls and label unfamiliar icons with a
tooltip/accessibility name. Keep cards flat and operationally dense.

Playwright has no `webServer`; start the backend first. It uses
`TOOLHUB_E2E_USERNAME`/`TOOLHUB_E2E_PASSWORD`, system Chrome, workers=1, and
desktop/mobile projects. Preserve console-error and viewport-overflow checks.

Verify:

```bash
cd web
npm run typecheck
npm run build
# with a live generation-2 backend:
TOOLHUB_E2E_USERNAME=... TOOLHUB_E2E_PASSWORD=... npm run test:e2e
```

Never hand-edit or commit generated `cmd/toolhub/dist` files.
