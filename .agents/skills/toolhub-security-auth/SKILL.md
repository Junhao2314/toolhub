---
name: toolhub-security-auth
description: Modify or audit ToolHub authentication, sessions, CSRF, RBAC, encryption, redaction, SSH credentials, Agent secret access, input validation, or audit boundaries. Use for security-sensitive changes and auth failures.
---

# ToolHub Security and Auth

## When to use

Auth/login/session/CSRF changes, RBAC, secrets encryption, redaction, SSH credentials, Agent secret access, enrollment tokens, audit, or security failures.

## Read first

Read `AGENTS.md`, `docs/SECURITY.md`, `internal/security` (including credentials/username helpers), `internal/policy`, `internal/httpapi/auth.go`, `internal/httpapi/middleware.go`, `internal/httpapi/agent.go`, `internal/store/auth.go`, `internal/store/secrets.go`, `internal/store/connections.go`, `internal/config/config.go`, and relevant tests.

## Required boundaries

### Passwords and identity

- Passwords: Argon2id (m=65536 KiB, t=3, p=2, salt 16B, key 32B); min 12 / max 1024 chars; constant-time verify.
- Login: **username or email** identifier; dummy hash for missing users (timing); IP limiter **10 / 10 minutes**; non-enumerating failures.
- Users have `username` (3–32, `[a-z0-9._-]`, unique, no `@`) and email (unique lowercased) from migration `002_username_credentials.sql`.
- `PasswordHash` / `CSRFHash` are `json:"-"` on domain types.

### Sessions and CSRF

- Cookie `toolhub_session`: HttpOnly, SameSite=Strict, Secure=`TOOLHUB_SECURE_COOKIES` (default **true**).
- Session token: 32B random; **only SHA-256** stored. TTL 15m–168h (default 12h).
- CSRF: 24B random; hash in session; unsafe methods need `X-CSRF-Token`; constant-time compare. Rotated on `GET /auth/session` and `GET /auth/csrf`.
- CSRF applies under authenticated `/api/v1` only. Login and public session probe are outside; Agent routes use bearer, not CSRF.
- There is **no Origin/Referer CSRF double-submit middleware** today — do not claim “origin validation” exists as a separate check.

### RBAC

Roles: `admin`, `operator`, `viewer`. Enforced by `requireRoles` route groups in `api.go`. UI admin filters are not security.

### Encryption

- `security.Cipher`: XChaCha20-Poly1305; master key 32 raw or base64-32; wire format version byte `1` + nonce + ciphertext; **AAD = secret record ID**.
- Token hash: SHA-256 for sessions, enrollment tokens, agent bearer tokens.
- Task signing: HMAC-SHA256 hex over `protocol.TaskSigningBytes`.

### Redaction (not universal)

`RedactMap` / `RedactJSON`: key regex for secret/password/token/api_key/authorization/private_key/credential; string prefixes `bearer ` / `sk-`. Used in **audit metadata**, **runtime inventory**, **AI input sanitization**. **Not** global API-response middleware. New response fields with secrets can leak unless omitted or redacted at the call site. `docs/SECURITY.md` wording about “API responses” is broader than code enforcement.

### Secrets access

| API | Authz |
|-----|-------|
| `SecretValue(id)` | Decrypt by ID only — **caller must authorize** |
| `AgentSecretValue(nodeID, id)` | `kind='mcp-env'` + enabled desired MCP deployment on that node with matching `env_refs` |
| `GET /agent/v1/secrets/{id}` | Agent bearer + node header → intentional plaintext for MCP env |

Never return AI keys, SSH private keys, or secret values on browser list/get APIs.

### SSH

- Address `user@host` only; one known_hosts line containing ` ssh-`; key must contain `PRIVATE KEY`.
- Key stored as encrypted secret kind `ssh-private-key`; previous enabled SSH connection disabled on replace.
- API returns connection **id only** (no key read-back). Fallback is fixed `toolhub-agent run-task`, not a shell.

### Enrollment

- Token 32B, hash-only store, 30m TTL, single-use (`used_at`).
- Create response embeds plaintext token in `token` + `agentCommand` — intentional one-time secret surface.
- Enroll returns one-time `agentToken` + base64 `taskKey` (task key also stored encrypted as `agent-task-key`). Do not log or re-echo casually.

### Policy package

`Resolve` in `internal/policy` ranks skill > source > node_group > global and skips disabled. **No production call sites** found outside tests; scheduler loads schedules from the store. Do not assume runtime policy enforcement exists when changing schedules.

### Input validation

`decodeJSON`: MaxBytesReader + DisallowUnknownFields + single object. Skill archives/Git imports enforce path, symlink, size, credential, and private-host safety.

### Audit

`Store.Audit` redacts metadata. Many callers use `_ = a.store.Audit(...)` (errors ignored). Preserve or improve observability deliberately.

## Reuse

Prefer `internal/security` (`HashPassword`, `VerifyPassword`, `Cipher`, `RandomToken`, `TokenHash`, `SignPayload`, `VerifyPayload`, `RedactMap`, `RedactJSON`, `NormalizeUsername`), store `CreateSecret` / `AgentSecretValue`, and middleware `authenticate` / `verifyCSRF` / `requireRoles`. Do not invent parallel crypto or global redaction middleware.

## Change workflow

1. Identify the trust boundary and threat model before editing.
2. Reuse existing security/token/cipher/redaction/username helpers — do not invent parallel crypto.
3. Preserve audit events for auth and administrative mutations.
4. Add a negative test for bypass, leakage, invalid input, or privilege escalation.
5. Verify secure-cookie and local smoke profiles separately; inspect logs and API payloads for accidental secrets.

## Easy-to-weaken traps

- Skipping `requireRoles` or putting mutations on the read group.
- Calling `SecretValue` from new HTTP handlers “to show configured.”
- Expanding `AgentSecretValue` beyond mcp-env + desired deployment join.
- Returning SSH private keys or AI keys in JSON.
- Disabling CSRF or Secure cookies “for convenience” without dual local/production profiles.
- Logging `agentCommand`, enrollment tokens, Authorization headers, or decrypt errors with sensitive context.
- Softening SSH validation (multi-line known_hosts, ProxyCommand-style addresses).
- Assuming redaction covers every API response because SECURITY.md says so.

## Prohibitions

Do not log passwords, tokens, private keys, API keys, ciphertext keys, or raw provider errors. Do not weaken role checks, CSRF, task signatures, SSH pinning, or managed-target conflict checks for convenience.

## Verification

```bash
go test ./internal/security/... ./internal/policy/... ./internal/store/...
go test ./...
go vet ./...
# session/limiter/queue concurrency:
go test -race ./...
# dual cookie profiles:
# production-like TOOLHUB_SECURE_COOKIES=true vs local false + smoke-api
```
