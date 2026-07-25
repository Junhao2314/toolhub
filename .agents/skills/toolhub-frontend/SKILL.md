---
name: toolhub-frontend
description: Develop and debug ToolHub's React/Vite operations UI, manual navigation, API client, shared components, responsive styles, and Playwright smoke behavior. Use when adding pages, actions, forms, or UI fixes.
---

# ToolHub Frontend

## When to use

New pages/actions, layout/auth/navigation fixes, API client changes, styling, or Playwright smoke adjustments.

## Read first

Read `AGENTS.md`, `web/src/App.tsx`, `web/src/api/client.ts`, `web/src/hooks/useData.ts`, `web/src/components/ui.tsx`, `web/src/styles.css`, the target page, `web/package.json`, `web/vite.config.ts`, and `web/e2e/smoke.spec.ts` when navigation/layout/auth changes.

## Stack and constraints

- React 18.3 + Vite 8 + TypeScript 5.7 (strict, ES2022). Icons: `lucide-react` only.
- **No** React Router, Redux/TanStack Query, CSS-in-JS, or UI kit packages.
- Vite dev: `127.0.0.1:18481`; proxy `/api` → `:18480`, `/agent` → WS `:18480`.
- Embedded production assets: rebuild via `make web` / Dockerfile into `cmd/toolhub/dist` — **never hand-edit**.

## Routing (`App.tsx`)

Manual `history.pushState` + `popstate`. `/` → `/overview`. Path selection uses `path.startsWith(...)` cascade.

| Path | Page | Gate |
|------|------|------|
| `/overview` | `Overview` | any session |
| `/skills` | `Skills` | any |
| `/marketplace` | `Marketplace` | any |
| `/nodes` | `Nodes` | any |
| `/jobs` | `Jobs` | any |
| `/mcp` | `MCP` | any |
| `/account` | `Account` | any (self-service username/password) |
| `/access` | `Access` | UI: `admin` only |
| `/settings` | `Settings` | UI: `admin` only |

Admin nav filter is cosmetic; server RBAC remains authoritative. Non-admin deep links to admin pages fall through to Overview. Password-change banner routes to `/account` when `passwordChangeRecommended`.

## Shared primitives

**`components/ui.tsx`:** `Button` (primary|secondary|danger|ghost), `IconButton`, `Status`, `Loading`, `Empty`, `ErrorNotice`, `Modal`, `Field`, `PageHeader`, `Segments`.

**`useData<T>`:** `{ data, error, loading, reload, setData }` with loader + dependency array. Prefer this for page loads; Marketplace search intentionally uses local loading state.

**`api` singleton (`ToolHubClient`):** `request`, `login(identifier, password)`, `bootstrap`, `logout`, `forgetSession`, `list`/`get`/`post`/`patch`/`delete`, `uploadSkill`, account helpers (`updateOwnUsername`, password change). Base `/api/v1`, `credentials: 'same-origin'`, CSRF from `sessionStorage` key `toolhub.csrf` on non-GET/HEAD. Errors → `APIError(status, code, message)`.

DTO types are page-local interfaces (not a shared models package). Wire JSON is camelCase.

## Login and account

- Login field is **username or email** (`identifier`), password `minLength={12}`.
- Account page updates username/password; successful changes sign out all sessions (server invalidation).
- Roles shown in Access: `viewer` | `operator` | `admin`.

## CSS conventions

Single `styles.css`: dark glassmorphism tokens, BEM-ish classes (`.app-shell`, `.page-header`, `.toolbar`, `.table-scroll`, `.modal-*`, status variants). Breakpoints ~1050 / 800 / 600; mobile off-canvas sidebar + `.nav-scrim`. `Status` class = value lowercased with `_` → `-`. Preserve focus states, table overflow, and modal semantics.

## Representative API paths used by UI

`/auth/*`, `/overview`, `/skills`, `/skills/upload`, skill review/deployments, `/deployments` + rollback, `/nodes` + scan/connections, `/jobs` + cancel, `/mcp/servers|profiles|deployments`, `/market/search`, `/recommendations`, `/users`, `/audit`, `/settings`, `/settings/ai-providers`, `/account/*`.

Skill review UI currently **approves** only (`decision: 'approved'`). Marketplace never auto-deploys. Skill TargetModal preselects project-host (`isLocal`) runtimes as canary. MCP env secrets are `NAME=value` lines; SSH form uses `knownHosts` + private key (no key read-back).

## Add a page or action

1. Confirm API contract and role needed (OpenAPI + server groups).
2. Add page-local types; use `useData` + `ui.tsx` primitives.
3. Show loading, error+retry, empty, success/reload, disabled/busy states.
4. Use `api` methods only (CSRF handled); map `APIError.message` to the user.
5. Register nav entry **and** `startsWith` branch in `App.tsx` together.
6. Run typecheck/build; run Playwright when layout, auth, or navigation changes.

## Playwright facts

- No `webServer`; hits **built** app on `:18480` by default (not Vite `:18481` unless `TOOLHUB_E2E_URL` set).
- Requires `TOOLHUB_E2E_EMAIL` / `TOOLHUB_E2E_PASSWORD` and Chrome at `/usr/bin/google-chrome`.
- Sequential workers; desktop + mobile; preserve console-error + layout non-overlap assertions.

## Reinvent traps

- Second HTTP client / bare `fetch` / axios
- React Router or global list store
- New Modal/toast/form library or Tailwind/CSS-in-JS parallel system
- Per-page loading widgets instead of `Loading`/`ErrorNotice`/`Empty`
- Hand-editing `cmd/toolhub/dist`

## Reuse

Prefer `api` (`ToolHubClient`), `useData`, and `components/ui.tsx` exports (`Button`, `Modal`, `Field`, `PageHeader`, `Loading`, `ErrorNotice`, `Empty`, `Status`, `Segments`). Extend client methods rather than adding a second fetch layer. Keep page-local DTO interfaces.

## Prohibitions

Do not rely on UI-only role gates for security. Do not auto-install marketplace results from the UI. Do not skip CSRF by using raw `fetch`.

## Verification

```bash
cd web && npm run typecheck
cd web && npm run build
# with backend up:
TOOLHUB_E2E_EMAIL=... TOOLHUB_E2E_PASSWORD=... npm run test:e2e
```
