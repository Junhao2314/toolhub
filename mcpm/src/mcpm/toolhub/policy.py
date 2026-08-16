"""ToolHub call-time policy lattice."""

from dataclasses import dataclass
from typing import Literal

Decision = Literal["allow", "confirm", "deny"]
_RANK = {"allow": 0, "confirm": 1, "deny": 2}


@dataclass(frozen=True)
class PolicyDecision:
    decision: Decision
    reasonCodes: tuple[str, ...]


def effective(global_decision: Decision, profile_decision: Decision | None) -> Decision:
    if global_decision not in _RANK:
        raise ValueError(f"invalid global decision: {global_decision}")
    if profile_decision is not None and profile_decision not in _RANK:
        raise ValueError(f"invalid profile decision: {profile_decision}")

    if profile_decision is None or _RANK[global_decision] >= _RANK[profile_decision]:
        return global_decision
    return profile_decision
