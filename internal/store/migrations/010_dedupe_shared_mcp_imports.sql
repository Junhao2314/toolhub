-- Legacy shared manifests are review inputs, not a second copy of a server that
-- is already observed from the same node. Preserve unique candidates, while
-- hiding structurally equivalent shared imports from the default library.
UPDATE mcp_servers AS candidate
SET enabled = false,
    archived_at = coalesce(candidate.archived_at, now()),
    updated_at = now()
WHERE candidate.source = 'shared-import'
  AND candidate.archived_at IS NULL
  AND EXISTS (
      SELECT 1
      FROM mcp_servers AS live
      WHERE live.id <> candidate.id
        AND live.authority = 'toolhub'
        AND live.source <> 'shared-import'
        AND live.archived_at IS NULL
        AND live.runtime_name = candidate.runtime_name
        AND live.transport = candidate.transport
        AND live.command = candidate.command
        AND live.args = candidate.args
        AND live.url = candidate.url
        AND (
            coalesce(live.origin->>'nodeId', '') = coalesce(candidate.origin->>'nodeId', '')
            OR EXISTS (
                SELECT 1
                FROM mcp_runtime_bindings AS binding
                WHERE binding.server_id = live.id
                  AND binding.node_id::text = candidate.origin->>'nodeId'
                  AND binding.server_name = candidate.runtime_name
            )
        )
  );
