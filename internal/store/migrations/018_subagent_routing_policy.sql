-- The routing policy replaces the old same-model dispatch rule. Keep the old
-- Skill artifact in Library history, but allow the Profile migration to remove
-- its required membership before adding subagent-routing.
UPDATE skills
SET tags = array_remove(tags, 'required'), updated_at = now()
WHERE archived_at IS NULL
  AND slug = 'same-model-subagents'
  AND 'required' = ANY(tags);
