---
name: toolhub-security-auth
description: Modify or audit ToolHub generation-2 singleton authentication, Argon2id passwords, sessions, CSRF, secure cookies, encrypted/write-only secrets, actorless audit, input validation, HMAC Unix Bridge authentication, nonce/idempotency journal safety, filesystem guards, or Salt/relay privilege boundaries.
---

# ToolHub Security And Auth

Read `AGENTS.md`, `docs/SECURITY.md`, `internal/security`,
`internal/httpapi/auth.go`, `internal/httpapi/middleware.go`,
`internal/store/auth.go`, `internal/store/secrets.go`, `internal/config`,
`internal/bridgeprotocol/auth.go`, `internal/bridge/server.go`,
`internal/bridge/journal.go`, and the relevant negative tests.

Preserve identity/session controls:

- schema-enforced singleton username; no email or RBAC;
- Argon2id `m=65536 KiB,t=3,p=2`, 16-byte salt, 32-byte key;
- passwords 12-1024 characters and timing-safe dummy-hash login failure;
- per-IP login limit 10 attempts/10 minutes;
- random session/CSRF tokens with SHA-256 hashes only in PostgreSQL;
- HttpOnly, SameSite=Strict, Secure-by-default cookie;
- CSRF header on every unsafe authenticated Browser request;
- username/password changes revoke every session.

Preserve secret controls:

- XChaCha20-Poly1305 with secret UUID as AAD;
- Browser responses expose key names only; values are write-only;
- desired manifests contain UUID references only;
- decrypt only active authorized manifest references and clear plaintext maps;
- never log or persist plaintext in audit, operation JSON, BoltDB, Bridge logs,
  Salt output, or browser responses.

Preserve Bridge controls:

- independent 32-byte HMAC signs method, URI, timestamp, nonce, exact body hash;
- 30-second skew and durable nonce replay rejection;
- idempotency key bound to request hash;
- socket `0660` with fixed group, no TCP, no managed-home container mount;
- typed operations/fixed allowlists only;
- journal rejects sensitive field names and raw payload categories.

Preserve filesystem/Salt/relay controls: canonical managed users/homes, path
containment, no symlink/device/traversal, bounded archives/config, protected
scopes, fixed Salt 3008.x accepted-key argv/functions, root-only chunked
staging/cleanup, fixed relay unit/port, and rollback on failed health.

Redaction is call-site specific, not universal middleware. Trace every new
field through Browser response, operation metadata, audit, logs, Bridge
idempotency response, recovery record, and Salt bundle.

Add a negative test for the bypass/leak being prevented. Never weaken secure
cookies, CSRF, HMAC, replay, path guards, version gates, protected scope, or
write-only semantics for convenience.

Verify:

```bash
GOCACHE=/tmp/toolhub-gocache go test ./internal/security ./internal/httpapi ./internal/store ./internal/bridgeprotocol ./internal/bridge ./internal/runtime ./internal/saltdriver
GOCACHE=/tmp/toolhub-gocache go test -race ./...
GOCACHE=/tmp/toolhub-gocache go vet ./...
```
