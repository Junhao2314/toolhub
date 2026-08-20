---
name: subagent-routing
description: Use when delegating planning, review, coding, frontend work, research, or any other subagent task that needs a deterministic model and tool route.
---

# Subagent Routing

## Core Principle

Classify the work before dispatching it. A child must use the route assigned to
its role; do not choose a model because it is cheaper, faster, newer, or seems
stronger. Keep the route explicit in the dispatch record and return the child
result to the parent for the next stage.

## Route Table

| Role | Route | Model selection |
|---|---|---|
| `planner`, `reviewer`, architecture decisions, final review | parent model | Use the parent session's exact model ID, character for character. If the API inherits the parent, inheritance is acceptable; otherwise pass that exact ID. |
| `executor`, coding, implementation, general execution | Hermes CLI (`hermes`) | Use Hermes' configured default. Preload this routing Skill; do not pass `--model` or `-m`. |
| `frontend`, UI, React/CSS/browser presentation | Kimi CLI (`kimi`) | Use Kimi's configured default and a curated frontend Skill directory; do not pass `--model` or `-m`. |
| `research`, documentation lookup,资料查阅 | Gemini CLI (`agy`) | Use agy/Gemini's configured default. Do not pass `--model` or `-m`. |

The configured CLI entry points are `hermes`, `kimi`, and `agy`. Use their
oneshot/print prompt modes:

```bash
hermes --skills subagent-routing -z "$PROMPT"
kimi --skills-dir "$FRONTEND_KIMI_SKILLS" -p "$PROMPT"
agy --print "$PROMPT"
```

Do not add a model flag to these commands. The CLI configuration is the source
of truth for its model ID, credentials, and service settings. `FRONTEND_KIMI_SKILLS`
must contain only the approved cross-project frontend bundle. Build it with
`scripts/build-frontend-kimi-bundle`; do not point Kimi at the whole Hermes,
Codex, or repository Skill tree. If the current repository has project-specific
frontend constraints, add them explicitly with `--project-skill-dir`; never
make one project's Skill part of the generic bundle.

## Dispatch Procedure

1. Label the task as exactly one route role (split mixed work into stages).
2. For `planner` or `reviewer`, resolve the current parent model's exact ID
   first. Never substitute `latest`, `fast`, a family name, or a vendor alias.
3. For a CLI role, verify the command is executable (`command -v hermes`,
   `command -v kimi`, or `command -v agy`) and invoke its prompt mode without a
   model override. Hermes must preload `subagent-routing`; Kimi must receive a
   curated `--skills-dir`, not the entire global Skill tree.
4. Give the child only the required workspace and context. It may not change
   the route, model, permissions, or workspace boundary.
5. If a required CLI is missing, not executable, misconfigured, or returns a
   failure, stop that route and report the failure. Never silently fall back to
   the parent model, another CLI, or an arbitrary model.
6. For a multi-role task, use an explicit sequence such as
   `planner -> executor -> reviewer`; frontend execution uses Kimi, while the
   final review still uses the parent model. Research is returned to the parent
   before implementation decisions are made.

The generic Kimi curated bundle defaults to `ui-ux-pro-max-cn`,
`responsive-check`, `performance-audit`, and `browser-ui-verification`;
`ui-ux-pro-max-cn` is mandatory. ToolHub exposes both the Kimi frontend bundle
and the Pi agent bundle in `Settings -> Subagent routing bundles`. The UI
stores Skill slugs; it does not Apply a runtime Profile. Materialize the
selected bundle from Settings with:

```bash
scripts/build-frontend-kimi-bundle --bundle kimi-frontend \
  --api-url http://127.0.0.1:18480 \
  --username "$TOOLHUB_BUNDLE_USERNAME" \
  --password "$TOOLHUB_BUNDLE_PASSWORD"
scripts/build-frontend-kimi-bundle --bundle pi \
  --api-url http://127.0.0.1:18480 \
  --username "$TOOLHUB_BUNDLE_USERNAME" \
  --password "$TOOLHUB_BUNDLE_PASSWORD"
```

For ToolHub, add the project overlay explicitly:

```bash
scripts/build-frontend-kimi-bundle \
  --project-skill-dir "$PWD/.agents/skills/toolhub-frontend"
```

Other visual or marketing Skills stay opt-in because frontend tasks vary by
project and application type.

Run `scripts/check_subagent_routing.py` when the dispatch mechanism is unclear
or an explicit child selection needs a deterministic policy check.

## Common Mistakes

| Mistake | Required correction |
|---|---|
| Sending a reviewer to Hermes/Kimi/agy | Reviewer is a parent-model route. |
| Passing the parent model to an executor or CLI | Omit the model override and use the CLI's configured default. |
| Adding `--model`, `-m`, `latest`, or `fast` to a CLI command | Remove it; CLI configuration owns model selection. |
| Loading the full frontend Skill tree into Hermes | Keep frontend Skills in Kimi's curated directory; Hermes only needs routing and execution context. |
| Falling back when `agy`/Hermes/Kimi is unavailable | Fail closed and report the unavailable route. |
| Letting a child pick a different route mid-task | Split the task and dispatch a new, explicitly classified stage. |
