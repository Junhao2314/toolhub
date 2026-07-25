# API Notes

The contract is [api/openapi.yaml](../api/openapi.yaml). Browser endpoints use an HttpOnly `toolhub_session` cookie. Unsafe requests require `X-CSRF-Token`; the token is returned by login and the public session probe.

Main namespaces:

- `/api/v1/auth`, `/users`, `/audit`, `/settings`
- `/api/v1/nodes`, `/skills`, `/sources`, `/deployments`, `/updates`, `/sync`, `/jobs`
- `/api/v1/market`, `/recommendations`, `/mcp`
- `/agent/v1/enroll`, `/connect`, `/artifacts`, `/secrets`

Errors use `{ "error": { "code", "message", "requestId" } }`. List responses use `{ "items": [...] }`. Agent WSS messages are typed envelopes; task signatures cover ID, kind, and canonical payload.
