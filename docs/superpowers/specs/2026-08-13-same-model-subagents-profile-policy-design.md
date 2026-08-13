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

## Migration Outcome (2026-08-13)

The Browser API migration completed successfully without Preflight or Apply.
The imported immutable Library artifact is:

- Skill ID: `39fb1f1e-fcbb-489b-9411-a3c3ee6bf1f5`
- Current version ID: `4a66a850-0e49-4c86-9e23-9ccd3e051eb6`
- Canonical SHA-256: `102228dfe0de95ef7d8d0fe4a150da1796e8748e69be047e61c981cd82686df0`
- Content hash: `d7bec7348a06629257f5ab19cb7fc5032d2d0aef63ae557638dba1022e198c89`

The active Profile revisions and final Skill counts are:

| Profile | Revision | Final Skills |
| --- | ---: | ---: |
| `claude-coding` | 3 -> 4 | 33 |
| `codex-coding` | 5 -> 6 | 32 |
| `claude-data-analysis` | 1 -> 2 | 29 |
| `codex-data-analysis` | 3 -> 4 | 30 |
| `claude-frontend-ui` | 1 -> 2 | 48 |
| `codex-frontend-ui` | 3 -> 4 | 49 |
| `claude-text-processing` | 1 -> 2 | 28 |
| `codex-text-processing` | 3 -> 4 | 29 |

Read-only PostgreSQL audit confirmed that exactly eight new Profile revision
rows were added and every prior revision remained unchanged and queryable.
All eight current revisions pin the exact new Skill version and retain
`requesting-code-review`. Both coding Profiles omit
`dispatching-parallel-agents` and `subagent-driven-development`, while both
Skills remain active in the Library. `shared-mcp` retained its revision,
canonical hash, and ordered MCP membership.

The baseline and post-migration PostgreSQL projections matched exactly for
Preflight confirmations, Apply operations, desired snapshots, backups,
runtime snapshot revisions, target desired snapshot pointers, desired
revisions, health, drift, and error fields. In particular, the audit window
beginning at `2026-08-13T05:29:24Z` contained zero new Preflight, Apply,
desired snapshot, or backup rows.

These verification commands passed with the policy credentials supplied only
through the process environment:

```bash
python3 -m unittest scripts/same_model_subagents_test.py -v
python3 /root/.codex/skills/.system/skill-creator/scripts/quick_validate.py \
  .agents/skills/same-model-subagents
GOCACHE=/tmp/toolhub-gocache go test ./internal/skills \
  ./scripts/same-model-profile-policy
GOCACHE=/tmp/toolhub-gocache go run ./scripts/same-model-profile-policy
GOCACHE=/tmp/toolhub-gocache go run ./scripts/same-model-profile-policy --apply
GOCACHE=/tmp/toolhub-gocache go run ./scripts/same-model-profile-policy --check \
  | jq -e '.verified == true'
GOCACHE=/tmp/toolhub-gocache go run ./scripts/same-model-profile-policy --apply \
  | jq -e '[.profiles[] | select((.add | length) > 0 or (.remove | length) > 0)] | length == 0'
```

The final `--apply` was an idempotency check: it reused the exact artifact and
created no further Profile revisions.
