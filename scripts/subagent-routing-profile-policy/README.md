# `subagent-routing` Profile policy

This policy keeps the route ownership explicit:

- `*-coding` gets `subagent-routing` plus the existing parallel-execution
  Skills. Hermes is the coding/plan execution hub and uses its configured
  default model.
- `*-frontend-ui` keeps `requesting-code-review` and the generic Kimi bundle.
  Its default members are `ui-ux-pro-max-cn`, `responsive-check`,
  `performance-audit`, and `browser-ui-verification`; the live Kimi and Pi
  dispatch bundles are adjustable in ToolHub Settings.
- Other Profiles lose the retired `same-model-subagents` membership.

The policy tool only changes Library/Profile revisions through the Browser API;
it never applies a Profile to a runtime target.

```bash
TOOLHUB_POLICY_USERNAME=... TOOLHUB_POLICY_PASSWORD=... \
  go run ./scripts/subagent-routing-profile-policy

TOOLHUB_POLICY_USERNAME=... TOOLHUB_POLICY_PASSWORD=... \
  go run ./scripts/subagent-routing-profile-policy --check

TOOLHUB_POLICY_USERNAME=... TOOLHUB_POLICY_PASSWORD=... \
  go run ./scripts/subagent-routing-profile-policy --apply
```

Before a Kimi frontend dispatch, build the local allowlist directory from the
current UI-managed Settings:

```bash
scripts/build-frontend-kimi-bundle --bundle kimi-frontend \
  --api-url http://127.0.0.1:18480 \
  --username "$TOOLHUB_BUNDLE_USERNAME" \
  --password "$TOOLHUB_BUNDLE_PASSWORD"
FRONTEND_KIMI_SKILLS="$HOME/.cache/toolhub/frontend-kimi"
kimi --skills-dir "$FRONTEND_KIMI_SKILLS" -p "$PROMPT"
```

The Pi bundle uses the same materializer with `--bundle pi`; its source root
defaults to `$HOME/.pi/agent/skills` and is independently editable in Settings.
For offline automation, pass a saved `/settings` response with `--config`.

For a project with repository-specific frontend constraints, add an explicit
overlay. For ToolHub:

```bash
scripts/build-frontend-kimi-bundle \
  --project-skill-dir "$PWD/.agents/skills/toolhub-frontend"
```

`toolhub-frontend` is not part of the generic frontend Profile; it remains a
ToolHub-only project Skill.

Do not point `--skills-dir` at `/root/.hermes/skills`, `/root/.codex/skills`,
or the repository's complete Skill tree.
