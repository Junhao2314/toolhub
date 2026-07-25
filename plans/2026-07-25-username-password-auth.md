# ToolHub Username Login and Credential Management

## Goal and constraints

- Add case-insensitive username login while retaining required email compatibility.
- Migrate the existing administrator to username `liujh273` and issue a one-time random temporary password.
- Let every authenticated user change their own username/password after confirming the current password.
- Let administrators create/reset temporary passwords without persisting or auditing plaintext credentials.
- Invalidate all target-user sessions after any username/password mutation, including the initiating session.
- Preserve existing Login and dark-theme working-tree changes and do not hand-edit `cmd/toolhub/dist`.

## Invariants

- Usernames are normalized to lowercase, are 3–32 characters, exclude `@`, and match `[a-z0-9._-]+`.
- Username uniqueness is case-insensitive; email remains required and case-insensitively unique.
- Passwords remain Argon2id hashes only. Random temporary passwords use `crypto/rand` and are returned once.
- Temporary-password reminders remain active until the user successfully changes their own password.
- Login failures remain non-enumerating and rate-limited; authenticated mutations preserve CSRF and RBAC boundaries.
- Audit metadata never includes passwords or password hashes.

## Implementation checklist

- [x] Add `002_username_credentials.sql` with username backfill, constraints/index, and `password_change_recommended`.
- [x] Add bootstrap username configuration and initialize/update the bootstrap administrator safely.
- [x] Extend domain/store projections and add transactional identifier lookup and credential mutation methods.
- [x] Change login to `{identifier,password}` and add self username/password plus admin reset endpoints.
- [x] Update create-user behavior for random/manual temporary passwords and one-time response fields.
- [x] Update OpenAPI and API notes.
- [x] Add Account route/page, persistent password reminder, Login identifier field, and Users credential controls.
- [x] Add focused negative/backend tests and update browser smoke coverage.
- [x] Run formatting, focused tests, full Go/race/vet tests, web typecheck/build, and inspect the final diff.
- [x] Run Compose migration/cutover when the local runtime configuration is available; return the generated password once.

## Validation strategy

- `go test ./internal/security/... ./internal/store/... ./internal/httpapi/... ./internal/config/...`
- `go test ./...`
- `go test -race ./internal/security/... ./internal/store/... ./internal/httpapi/...`
- `go vet ./...`
- `cd web && npm run typecheck && npm run build`
- `make docker-config`
- `docker compose up -d --build --wait` followed by API/browser smoke when local credentials are available.
- Confirm username and email login, invalid/duplicate usernames, weak manual passwords, RBAC/CSRF rejection, session invalidation, and one-time temporary-password handling.

## Rollback

- Restore the previous application image/binary. The additive `002` columns and indexes remain compatible and are not destructively removed.
- Do not edit or roll back `001_initial.sql`; no destructive down migration is provided.
- If cutover has occurred, use the prior application's email login and an explicitly reset password, or update the administrator hash through the new version before rolling back.
- Preserve database backups before integrated deployment and verify login/session behavior immediately after rollback.
