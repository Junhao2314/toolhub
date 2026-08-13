#!/usr/bin/env python3
import argparse
import sys


def validate_dispatch(
    parent_model: str,
    child_model: str | None,
    inherits_parent: bool,
) -> None:
    """Raise ValueError unless the child uses the exact parent model."""
    parent_model = parent_model.strip()
    if not parent_model:
        raise ValueError("parent model identity is required")

    child_model = child_model.strip() if child_model is not None else None
    if inherits_parent:
        if child_model:
            raise ValueError(
                "choose inheritance or an explicit child model, not both"
            )
        return

    if not child_model:
        raise ValueError(
            "inheritance or an explicit child model selection is required"
        )
    if child_model != parent_model:
        raise ValueError("child model must match parent model exactly")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Validate that a child dispatch uses the parent model."
    )
    parser.add_argument("--parent-model", required=True)
    selection = parser.add_mutually_exclusive_group(required=True)
    selection.add_argument("--inherits-parent", action="store_true")
    selection.add_argument("--child-model")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        validate_dispatch(
            args.parent_model,
            args.child_model,
            args.inherits_parent,
        )
    except ValueError as error:
        print(str(error), file=sys.stderr)
        return 1

    print("allowed: child model matches parent model")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
