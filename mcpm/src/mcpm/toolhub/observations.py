"""Payload-free bounded observations for ToolHub relay governance."""

import time
from collections import deque
from dataclasses import dataclass
from datetime import UTC, datetime
from threading import RLock
from typing import Callable
from uuid import uuid4

MAX_OBSERVATIONS = 100_000
OBSERVATION_TTL_SECONDS = 24 * 60 * 60
MAX_DRAIN_ITEMS = 1_000

_DECISIONS = {"allow", "confirm", "deny"}
_OUTCOMES = {
    "confirmation_required",
    "confirmed",
    "rejected",
    "expired",
    "denied",
    "executed",
    "failed",
    "unknown",
    "not_executed",
}
_ERROR_CLASSES = {
    "none",
    "policy",
    "confirmation",
    "rate_limited",
    "timeout",
    "transport",
    "upstream",
    "internal",
}


@dataclass(frozen=True)
class Observation:
    boot_id: str
    sequence: int
    observed_at: float
    minute_bucket: str
    profile_id: str
    profile_revision_id: str
    server_id: str
    tool_id: str
    decision: str
    reason_codes: tuple[str, ...]
    outcome: str
    duration_bucket: str
    error_class: str

    def safe_dict(self) -> dict[str, object]:
        return {
            "bootId": self.boot_id,
            "sequence": self.sequence,
            "observedAt": self.observed_at,
            "minuteBucket": self.minute_bucket,
            "profileId": self.profile_id,
            "profileRevisionId": self.profile_revision_id,
            "serverId": self.server_id,
            "toolId": self.tool_id,
            "decision": self.decision,
            "reasonCodes": list(self.reason_codes),
            "outcome": self.outcome,
            "durationBucket": self.duration_bucket,
            "errorClass": self.error_class,
        }


class ObservationRing:
    def __init__(self, limit: int = MAX_OBSERVATIONS, clock: Callable[[], float] = time.time):
        if not 1 <= limit <= MAX_OBSERVATIONS:
            raise ValueError(f"observation limit must be between 1 and {MAX_OBSERVATIONS}")
        self.limit = limit
        self.boot_id = str(uuid4())
        self._clock = clock
        self._lock = RLock()
        self._sequence = 0
        self._items: deque[Observation] = deque(maxlen=limit)

    def record(
        self,
        *,
        profile_id: str,
        profile_revision_id: str,
        server_id: str,
        tool_id: str,
        decision: str,
        reason_codes: tuple[str, ...],
        outcome: str,
        duration_seconds: float,
        error_class: str,
    ) -> Observation:
        if decision not in _DECISIONS:
            raise ValueError("invalid observation decision")
        if outcome not in _OUTCOMES:
            raise ValueError("invalid observation outcome")
        if error_class not in _ERROR_CLASSES:
            raise ValueError("invalid observation error class")
        if duration_seconds < 0:
            raise ValueError("observation duration cannot be negative")
        if len(reason_codes) > 32:
            raise ValueError("too many observation reason codes")

        now = self._clock()
        with self._lock:
            self._purge(now)
            self._sequence += 1
            observed = Observation(
                boot_id=self.boot_id,
                sequence=self._sequence,
                observed_at=now,
                minute_bucket=_minute_bucket(now),
                profile_id=profile_id,
                profile_revision_id=profile_revision_id,
                server_id=server_id,
                tool_id=tool_id,
                decision=decision,
                reason_codes=reason_codes,
                outcome=outcome,
                duration_bucket=_duration_bucket(duration_seconds),
                error_class=error_class,
            )
            self._items.append(observed)
            return observed

    def drain(
        self,
        *,
        after_boot_id: str | None = None,
        after_sequence: int = 0,
        limit: int = MAX_DRAIN_ITEMS,
    ) -> dict[str, object]:
        if not 1 <= limit <= MAX_DRAIN_ITEMS:
            raise ValueError(f"drain limit must be between 1 and {MAX_DRAIN_ITEMS}")
        if after_sequence < 0:
            raise ValueError("after_sequence cannot be negative")

        now = self._clock()
        with self._lock:
            self._purge(now)
            cursor = after_sequence if after_boot_id == self.boot_id else 0
            selected = [item for item in self._items if item.sequence > cursor][:limit]
            return {
                "bootId": self.boot_id,
                "items": [item.safe_dict() for item in selected],
                "nextSequence": selected[-1].sequence if selected else cursor,
            }

    def count(self) -> int:
        now = self._clock()
        with self._lock:
            self._purge(now)
            return len(self._items)

    def _purge(self, now: float) -> None:
        cutoff = now - OBSERVATION_TTL_SECONDS
        while self._items and self._items[0].observed_at <= cutoff:
            self._items.popleft()


def _minute_bucket(timestamp: float) -> str:
    value = datetime.fromtimestamp(timestamp, tz=UTC).replace(second=0, microsecond=0)
    return value.isoformat().replace("+00:00", "Z")


def _duration_bucket(duration_seconds: float) -> str:
    if duration_seconds < 0.01:
        return "lt_10ms"
    if duration_seconds < 0.1:
        return "lt_100ms"
    if duration_seconds < 1:
        return "lt_1s"
    if duration_seconds < 10:
        return "lt_10s"
    return "gte_10s"
