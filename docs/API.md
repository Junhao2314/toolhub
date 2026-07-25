# API Notes

The contract is [api/openapi.yaml](../api/openapi.yaml). Browser endpoints use an HttpOnly `toolhub_session` cookie. Unsafe requests require `X-CSRF-Token`; the token is returned by login and the public session probe.

Main namespaces:

- `/api/v1/auth`, `/users`, `/audit`, `/settings`
- `/api/v1/nodes`, `/skills`, `/sources`, `/deployments`, `/updates`, `/sync`, `/jobs`
- `/api/v1/market`, `/recommendations`, `/mcp`
- `/agent/v1/enroll`, `/connect`, `/artifacts`, `/secrets`

Errors use `{ "error": { "code", "message", "requestId" } }`. List responses use `{ "items": [...] }`. Agent WSS messages are typed envelopes; task signatures cover ID, kind, and canonical payload.

## Browser credentials

- Login accepts `{ "identifier", "password" }`; `identifier` matches either a lowercase username or the required email address, case-insensitively. Authentication failures use one non-enumerating error.
- Usernames are normalized to lowercase and contain 3–32 letters, numbers, `.`, `_`, or `-`; `@` is reserved for email identifiers.
- `PATCH /account/username` and `PATCH /account/password` require `currentPassword`. A successful change revokes every session for that user, including the current browser session.
- Administrators create users with `passwordMode: "random" | "manual"` and reset credentials through `POST /users/{id}/password`. Random passwords are returned once as `temporaryPassword`; manual passwords are never echoed.
- Temporary passwords set `passwordChangeRecommended=true`. The flag clears only after the user changes their own password.
