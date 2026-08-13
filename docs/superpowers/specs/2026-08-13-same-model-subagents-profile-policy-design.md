# Same-Model Subagents Profile Policy Design

Date: 2026-08-13

## Goal

Reduce explicit subagent orchestration in the two coding Profiles while adding
a mandatory Skill that requires every subagent invocation to use the current
parent model.

The affected orchestration Skills remain available in the Library. This work
changes Profile membership only; it does not Apply a Profile or mutate any
target, desired snapshot, backup, or runtime inventory.

## Scope And Invariants

- Update the active revisions of `claude-coding` and `codex-coding` to remove:
  - `dispatching-parallel-agents`
  - `subagent-driven-development`
- Keep `requesting-code-review` in the mandatory baseline.
- Keep both removed Skills and every immutable version in the Library.
- Create and import one new Skill named `same-model-subagents`.
- Add `same-model-subagents` to all eight ordinary Skill Profiles:
  - `claude-coding`
  - `codex-coding`
  - `claude-data-analysis`
  - `codex-data-analysis`
  - `claude-frontend-ui`
  - `codex-frontend-ui`
  - `claude-text-processing`
  - `codex-text-processing`
- Do not add the Skill to `shared-mcp`; that Profile contains MCP membership,
  not runtime Skills.
- Preserve exact immutable version pinning by creating a new revision for each
  changed Profile. Do not rewrite historical Profile revisions.
- Do not Preflight or Apply any changed Profile.

## Skill Contract

`same-model-subagents` is a compact discipline Skill that triggers whenever an
agent is about to spawn, dispatch, delegate to, or otherwise invoke a subagent
or child model.

Its rules are:

1. Capture the exact model identity of the current parent session before the
   child call.
2. When the platform inherits the parent model by omitting a model override,
   omit the override.
3. When the platform requires an explicit model argument, pass the exact
   parent model identity.
4. Never select a cheaper, faster, stronger, fallback, role-specific, or
   otherwise different model for the child.
5. Reasoning effort, service tier, role, prompt, tools, and context may differ;
   model identity may not.
6. If the parent model identity cannot be determined or the same model is not
   available to the child, do not dispatch the child. Continue in the parent
   session when feasible; otherwise report the constraint.
7. User or project instructions requesting a different child model do not
   silently override this mandatory Profile policy. Surface the conflict and
   require the Profile or governing policy to be changed explicitly.

The Skill must not require a subagent in order to validate itself. Validation
uses static structure checks and deterministic scenarios that inspect the
chosen dispatch parameters.

## Library Intake

Create the Skill in a bounded local Skill source, scan it with ToolHub's normal
Skill validation, and import the resulting immutable artifact through the
existing Library intake path. The Library entry must have slug
`same-model-subagents`, valid frontmatter, a canonical artifact hash, and an
active current version.

If an existing Library Skill already owns that slug, stop and compare its
content and provenance. Do not overwrite or silently advance an unrelated
entry.

## Profile Revision Changes

Resolve current Profile membership from ToolHub at execution time rather than
assuming the historical counts in the earlier classification design still
match live state.

For both coding Profiles, the new membership is:

```text
current members
- dispatching-parallel-agents
- subagent-driven-development
+ same-model-subagents
```

For the other six ordinary Profiles, the new membership is:

```text
current members
+ same-model-subagents
```

All unrelated Skill IDs, MCP IDs, descriptions, pending Secret bindings, and
ordering remain unchanged. Each edit uses the current revision for optimistic
concurrency and produces one new immutable revision.

## Failure Handling

- Missing Profile or Library member: stop before changing dependent Profiles.
- Revision conflict: reload the current Profile and recompute the exact delta;
  do not retry against stale membership.
- Partial Profile update: report exactly which Profiles advanced. Do not Apply
  any revision, and finish the remaining idempotent edits only after reloading
  state.
- Skill intake failure: leave every Profile unchanged.
- Existing unrelated worktree changes: preserve them and commit only files
  created for this design and implementation.

## Validation

Before reporting completion:

- validate the new Skill folder with the skill-creator validator;
- verify the Skill contract rejects explicit child-model substitution and
  accepts parent-model inheritance;
- confirm `same-model-subagents` exists in the Library with an immutable current
  version;
- confirm all eight ordinary Profiles pin that exact Skill version;
- confirm only the two coding Profiles omit both removed orchestration Skills;
- confirm `requesting-code-review` remains in all eight ordinary Profiles;
- confirm `dispatching-parallel-agents` and `subagent-driven-development` remain
  in the Library;
- confirm `shared-mcp` is unchanged;
- confirm no Preflight confirmation, Apply operation, desired snapshot, target
  health, backup, or runtime inventory changed;
- inspect audit records and Profile revision increments for the exact changes.

## Rollback

No runtime rollback is needed because this work does not Apply. To reverse the
configuration, create another Profile revision that restores the two removed
members to the coding Profiles and removes `same-model-subagents` from the
eight ordinary Profiles. Historical revisions and immutable Library artifacts
remain available throughout.
