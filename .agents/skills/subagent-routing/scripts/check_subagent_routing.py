#!/usr/bin/env python3
"""Validate the deterministic subagent route policy."""

from __future__ import annotations

import argparse
import os
import shutil
import sys
from collections.abc import Sequence


PARENT_ROLES = {
    "planner",
    "planning",
    "reviewer",
    "review",
    "architecture",
    "final-review",
}
CLI_ROUTES = {
    "executor": ("hermes", ("-z", "--oneshot")),
    "coding": ("hermes", ("-z", "--oneshot")),
    "implementation": ("hermes", ("-z", "--oneshot")),
    "frontend": ("kimi", ("-p", "--prompt")),
    "ui": ("kimi", ("-p", "--prompt")),
    "research": ("agy", ("--print", "--prompt")),
    "资料查阅": ("agy", ("--print", "--prompt")),
}
MODEL_FLAGS = ("--model", "-m")


def _role_key(role: str) -> str:
    key = role.strip().lower()
    if key in PARENT_ROLES or key in CLI_ROUTES:
        return key
    raise ValueError(f"unsupported subagent role: {role.strip()!r}")


def _has_model_override(args: Sequence[str]) -> bool:
    for token in args:
        if token in MODEL_FLAGS or token.startswith("--model="):
            return True
        # Reject compact short forms such as -mgpt-5.6-sol as well.
        if token.startswith("-m") and token != "--":
            return True
    return False


def _option_values(args: Sequence[str], option: str) -> list[str]:
    values: list[str] = []
    for index, token in enumerate(args):
        if token == option and index + 1 < len(args):
            values.append(args[index + 1])
        elif token.startswith(option + "="):
            values.append(token.split("=", 1)[1])
    return values


def validate_dispatch(
    role: str,
    parent_model: str | None = None,
    child_model: str | None = None,
    command: str | None = None,
    args: Sequence[str] | None = None,
    inherits_parent: bool = False,
    check_executable: bool = False,
) -> None:
    """Raise ValueError unless a dispatch obeys the role route."""

    key = _role_key(role)
    parent = (parent_model or "").strip()
    child = child_model.strip() if child_model is not None else None
    command = command.strip() if command is not None else None
    argv = tuple(args or ())

    if child and inherits_parent:
        raise ValueError("choose parent inheritance or an explicit child model, not both")

    if key in PARENT_ROLES:
        if not parent:
            raise ValueError("parent model identity is required for planner/reviewer")
        if command:
            raise ValueError("planner/reviewer must use the parent-model route, not a CLI")
        if child and child != parent:
            raise ValueError("child model must match parent model exactly")
        if not child and not inherits_parent:
            raise ValueError("planner/reviewer requires exact child model or parent inheritance")
        if _has_model_override(argv):
            raise ValueError("planner/reviewer command cannot contain a CLI model override")
        return

    expected, prompt_flags = CLI_ROUTES[key]
    if child or inherits_parent:
        raise ValueError(f"{expected} route must use its configured default model")
    if not command or os.path.basename(command) != expected:
        raise ValueError(f"role {role.strip()!r} must use the {expected} CLI")
    if _has_model_override(argv):
        raise ValueError(f"{expected} route must not pass --model or -m")
    if not any(flag in argv for flag in prompt_flags):
        raise ValueError(f"{expected} route requires one of: {', '.join(prompt_flags)}")
    if expected == "hermes":
        if "subagent-routing" not in _option_values(argv, "--skills"):
            raise ValueError("hermes route must preload the subagent-routing Skill")
    if expected == "kimi":
        if not _option_values(argv, "--skills-dir"):
            raise ValueError("kimi route requires a curated --skills-dir")
    if check_executable and shutil.which(command) is None:
        raise ValueError(f"required CLI {expected!r} is not executable or not on PATH")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Validate the subagent routing policy.")
    parser.add_argument("--role", required=True)
    parser.add_argument("--parent-model")
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--inherits-parent", action="store_true")
    selection.add_argument("--child-model")
    parser.add_argument("--command")
    parser.add_argument("--arg", action="append", default=[])
    parser.add_argument("--check-executable", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        validate_dispatch(
            role=args.role,
            parent_model=args.parent_model,
            child_model=args.child_model,
            command=args.command,
            args=args.arg,
            inherits_parent=args.inherits_parent,
            check_executable=args.check_executable,
        )
    except ValueError as error:
        print(str(error), file=sys.stderr)
        return 1

    print(f"allowed: {_role_key(args.role)} route is valid")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
