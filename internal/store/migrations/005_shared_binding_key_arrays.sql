-- Shared MCP bindings were written with JSON `null` instead of `[]` whenever the Agent
-- reported no environment or header keys, because a nil Go slice marshals to `null`.
-- The column default never applied to those explicit writes, and the shared-source
-- projection then failed with "cannot extract elements from a scalar", turning every
-- GET /api/v1/shared-sources into a 500.

UPDATE mcp_runtime_bindings
SET env_keys='[]'::jsonb
WHERE env_keys IS NULL OR jsonb_typeof(env_keys) <> 'array';

UPDATE mcp_runtime_bindings
SET header_keys='[]'::jsonb
WHERE header_keys IS NULL OR jsonb_typeof(header_keys) <> 'array';

ALTER TABLE mcp_runtime_bindings
    ADD CONSTRAINT mcp_runtime_bindings_env_keys_array_check CHECK (jsonb_typeof(env_keys)='array'),
    ADD CONSTRAINT mcp_runtime_bindings_header_keys_array_check CHECK (jsonb_typeof(header_keys)='array');
