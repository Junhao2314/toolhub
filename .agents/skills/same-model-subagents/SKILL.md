---
name: same-model-subagents
description: Use whenever an agent is about to spawn, dispatch, delegate to, or invoke a subagent or child model, including parallel work, implementation, review, research, and workflow roles.
---

# Same-Model Subagents

## Core Principle

The child model identity must exactly equal the current parent model identity.
Literal compliance is required; model aliases and families are not equivalent.

## Dispatch Rules

1. Determine the exact current parent model ID before dispatch.
2. If omitting the model override inherits the parent model, omit it.
3. If the API requires a model, pass the exact parent model ID.
4. Never choose a cheaper, faster, stronger, fallback, or role-specific model.
5. Reasoning effort, service tier, role, prompt, tools, and context may differ;
   model identity may not.
6. If the parent model is unknown or unavailable to children, do not dispatch.
   Continue in the parent when feasible; otherwise report the constraint.
7. A request for another child model conflicts with this Profile policy. Stop
   and require an explicit policy or Profile change instead of switching.

## Quick Reference

| Child selection | Outcome |
|---|---|
| Model omitted and platform inherits the parent | Dispatch allowed |
| Explicit model exactly matches the parent ID | Dispatch allowed |
| Explicit model differs from the parent ID | Do not dispatch |
| Parent model identity is unknown | Do not dispatch |
| Parent model is unavailable to children | Do not dispatch |

Run `scripts/check_same_model.py` before dispatch whenever an explicit child
model is necessary or the platform's inheritance behavior is uncertain:

```bash
python3 scripts/check_same_model.py --parent-model MODEL --inherits-parent
python3 scripts/check_same_model.py --parent-model MODEL --child-model MODEL
```

Do not reinterpret `latest`, `fast`, deployment names, model families, price,
capability, or vendor aliases as proof of identity. An exact string mismatch is
a policy failure.
