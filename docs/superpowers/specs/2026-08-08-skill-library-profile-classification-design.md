# Skill Library And Profile Classification Design

Date: 2026-08-08

## Goal

Consolidate the currently installed local Claude and Codex Skills into the
ToolHub Library, retire a small reviewed set of redundant or legacy entries,
and create runtime-specific Profiles that reduce the number of Skills exposed
in a normal session.

The user will choose and Apply a Profile through ToolHub. This work must not
Apply a Profile or otherwise change a target's desired snapshot.

## Current State

- The Library contains 47 Skills imported from the five newly installed
  groups: `superpowers-zh`, `webdesign-agency-skills`, `qu-ai-wei`,
  `taste-skill`, and `ui-ux-pro-max-skill-cn`.
- `local/claude` currently reports 79 inventory entries: 30 importable regular
  Skill directories and 49 protected entries.
- `local/codex` currently reports 78 inventory entries: 28 importable regular
  Skill directories and 50 protected entries.
- The regular Claude/Codex inventories contain 32 unique Skill slugs. Their 26
  common slugs have identical content hashes; Claude has four additional
  Codex-delegation Skills and Codex has two host-integration Skills.
- The only current Profile is `shared-mcp`; it must remain unchanged.
- Each runtime has 47 active symlinks into `/root/.shared/skills`. ToolHub
  classifies those symlinks as protected inventory entries.

## Invariants

- A Profile is a complete desired selection, not an additive layer. Every
  Profile therefore repeats the mandatory baseline.
- Profile revisions pin exact immutable Skill versions. Later Library updates
  do not change an existing Profile until an explicit Refresh or edit.
- Library import never Applies a Profile and never changes runtime files.
- No Profile Apply is part of this work.
- The 47 newly installed Skills must all appear in at least one created
  Profile, even when some of them overlap.
- `qu-ai-wei` and the agreed web-research and agent-tooling baseline must be in
  every created Profile.
- All five `grill-*` Skills are mandatory in every Claude and Codex Profile.
  `acemcp-incremental-sync` remains Codex-only.
- The existing `shared-mcp` Profile, MCP Library, desired snapshots, and target
  health must not be changed.

## Reviewed Library Intake

The 47 newly installed Skills are already in the Library and remain there.

Import 25 of the 32 unique regular-directory Skills:

```text
ab-testing-analyzer
acemcp-incremental-sync
attribution-analysis-modeling
baoyu-format-markdown
baoyu-translate
baoyu-url-to-markdown
cloakbrowser-agent-browser-guard
codex-build
codex-review
content-analysis
domain-modeling
firecrawl
firecrawl-agent
firecrawl-build-interact
firecrawl-build-onboarding
firecrawl-build-scrape
firecrawl-build-search
funnel-analysis
grill-me
grill-me-codex
grill-with-docs
grill-with-docs-codex
grilling
hallmark
ltv-predictor
```

The expected active Library size after intake is 72 Skills. An import that
matches an existing immutable artifact may reuse it instead of creating a
duplicate version.

Do not import these seven reviewed cleanup candidates:

```text
vibe-upgrade
firecrawl-search
firecrawl-scrape
firecrawl-map
firecrawl-crawl
firecrawl-download
firecrawl-interact
```

`vibe-upgrade` explicitly declares itself a legacy compatibility entry. The
six Firecrawl command Skills are not deprecated, but their routing surface is
duplicated by the retained `firecrawl` umbrella Skill. `firecrawl-agent` and
the four `firecrawl-build-*` Skills remain because they cover distinct
structured-extraction and product-integration workflows.

`hallmark` remains in the Library and in the frontend Profile as requested.
The three Baoyu Skills remain: their upstream is active, and their current
local versions should not be treated as obsolete merely because they were
installed before the new groups.

## Mandatory Baseline

Every Profile contains these 22 common Skills:

```text
using-superpowers
brainstorming
writing-plans
systematic-debugging
test-driven-development
verification-before-completion
requesting-code-review
receiving-code-review
using-git-worktrees
executing-plans
finishing-a-development-branch
grill-me
grill-me-codex
grill-with-docs
grill-with-docs-codex
grilling
qu-ai-wei
firecrawl
cloakbrowser-agent-browser-guard
mcp-builder
writing-skills
workflow-runner
```

Every Codex Profile additionally contains:

```text
acemcp-incremental-sync
```

The effective baseline is therefore 22 Skills for Claude and 23 for Codex.
`ast-grep` remains available through its existing protected/global path and is
not duplicated in a Profile.

## Profiles

Create eight Profiles. Names deliberately include the runtime because ToolHub
does not dynamically filter a Profile's Skill members by runtime.

### Coding Workflow

Profiles: `claude-coding` and `codex-coding`.

Add these 12 common members to the runtime baseline:

```text
chinese-code-review
chinese-commit-conventions
chinese-documentation
chinese-git-workflow
dispatching-parallel-agents
subagent-driven-development
domain-modeling
full-output-enforcement
firecrawl-build-onboarding
firecrawl-build-search
firecrawl-build-scrape
firecrawl-build-interact
```

Add `codex-build` and `codex-review` only to `claude-coding`.

Expected totals: 36 Skills for Claude and 35 for Codex.

### Data Analysis

Profiles: `claude-data-analysis` and `codex-data-analysis`.

Add these six members to the runtime baseline:

```text
ab-testing-analyzer
attribution-analysis-modeling
content-analysis
firecrawl-agent
funnel-analysis
ltv-predictor
```

Expected totals: 28 Skills for Claude and 29 for Codex.

### Frontend UI

Profiles: `claude-frontend-ui` and `codex-frontend-ui`.

Add these 25 members to the runtime baseline:

```text
banner-design
brand
brandkit
design
design-system
design-taste-frontend
design-taste-frontend-v1
gpt-taste
hallmark
high-end-visual-design
image-to-code
imagegen-frontend-mobile
imagegen-frontend-web
improve
industrial-brutalist-ui
lighthouse-100
minimalist-ui
performance-audit
prospect-audit
redesign-existing-projects
responsive-check
stitch-design-taste
ui-styling
ui-ux-pro-max
ui-ux-pro-max-cn
```

This intentionally retains every newly installed taste/UI/web-design Skill,
including the v1 taste entry, plus `hallmark`.

Expected totals: 47 Skills for Claude and 48 for Codex.

### Text Processing

Profiles: `claude-text-processing` and `codex-text-processing`.

Add these five members to the runtime baseline:

```text
baoyu-format-markdown
baoyu-translate
baoyu-url-to-markdown
chinese-documentation
slides
```

Expected totals: 27 Skills for Claude and 28 for Codex.

## Operation Sequence

1. Scan `local/claude` and `local/codex` and retain their exact inventory
   revisions and content hashes.
2. Import the 19 retained common regular-directory Skills from one local
   target. Import the four Skills available only from the Claude source root
   from `local/claude`, and the two available only from the Codex source root
   from `local/codex`.
3. Respect the one-active-operation-per-target invariant by waiting for each
   target's import operation to reach a terminal state before queueing its next
   import. The Claude and Codex streams may progress independently.
4. Reload the Library and resolve every Profile member to its current immutable
   version.
5. Create the eight Profiles through the Browser API. Do not include MCP
   members.
6. Verify exact membership, revision 1, pinned version IDs, canonical hashes,
   and the unchanged `shared-mcp` Profile.
7. Do not Preflight or Apply. Preflight tokens would become stale during the
   required symlink migration.

If an import fails, leave that Skill out of dependent Profile creation until
the cause is resolved. Do not silently create an incomplete Profile.

## One-Time Symlink Migration Before First Apply

Creating Profiles alone will not reduce the currently visible Skill count.
ToolHub preserves symlink inventory entries as protected unless a desired Skill
replaces the same path, and several shared-directory aliases differ from their
normalized Library slugs.

Immediately before the user's first Apply:

1. Resolve and record each direct child symlink in `~/.claude/skills` and
   `~/.codex/skills` whose target is under `/root/.shared/skills`.
2. Move only those verified symlinks to a timestamped backup outside both
   active Skill roots. Preserve their runtime, original name, and link target.
3. Do not move `.system`, `.toolhub-disabled`, `dist`, or the protected
   `ast-grep` link into `.agents/skills`.
4. Scan the selected target again.
5. Run Profile Preflight against the new target revision.
6. The user reviews the destructive diff and performs Apply in the UI.

The migration must happen before Preflight. Moving links after Preflight
changes the target revision and correctly invalidates the confirmation token.

## Validation

Before reporting configuration completion:

- confirm the Library contains 72 active Skills and none of the seven cleanup
  candidates;
- confirm all 25 retained regular-directory slugs exist at their expected
  immutable versions;
- confirm all eight Profiles exist with the exact counts above and contain no
  MCP members;
- confirm every newly installed Library Skill belongs to at least one Profile;
- confirm `qu-ai-wei`, `firecrawl`, all five grill Skills, and the
  agreed agent-tooling members belong to every Profile;
- confirm the Codex-only membership rule;
- confirm `shared-mcp`, desired snapshots, target health, and runtime inventory
  were not changed by Library/Profile creation;
- inspect operation results and audit records for partial or failed imports;
- do not claim runtime Skill-count reduction until the symlink migration and a
  user-confirmed Apply have actually occurred.

## Rollback

- Library imports are non-destructive immutable intake. Unused imported Skills
  can remain in the Library or be archived later.
- Profiles can be archived without changing any runtime target.
- Before Apply, symlink migration rollback consists of moving each recorded
  link back to its original runtime path only when that path is absent.
- After Apply, use the separate symlink backup together with ToolHub's target
  backup if the user chooses to restore the former shared-link layout.
- Never delete `/root/.shared/skills`; it is the retained source behind the
  reversible symlink migration.
