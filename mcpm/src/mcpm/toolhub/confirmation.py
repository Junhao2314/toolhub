"""Bounded one-shot confirmation challenges without raw argument retention."""

import hashlib
import hmac
import re
import secrets
import time
from collections import defaultdict, deque
from dataclasses import asdict, dataclass, field
from threading import RLock
from typing import Any, Callable

import rfc8785

MAX_PENDING_GLOBAL = 256
MAX_PENDING_PER_PROFILE = 32
MAX_CHALLENGES_PER_PROFILE_MINUTE = 30
CHALLENGE_TTL_SECONDS = 5 * 60
GRANT_TTL_SECONDS = 60
MAX_SUMMARY_NODES = 512
MAX_SUMMARY_DEPTH = 64
MAX_POINTER_LENGTH = 512

_SENSITIVE_NAME = re.compile(r"(?:secret|password|passwd|token|credential|api.?key|private.?key)", re.IGNORECASE)


class ConfirmationError(ValueError):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


@dataclass(frozen=True)
class CallBinding:
    profile_id: str
    profile_revision_id: str
    profile_revision_hash: str
    profile_name: str
    client_kind: str
    server_id: str
    server_name: str
    tool_id: str
    tool_name: str
    runtime_name: str
    mcp_config_revision_id: str
    contract_revision_id: str
    contract_revision_hash: str
    global_policy_revision_id: str
    global_policy_hash: str
    decision: str
    reason_codes: tuple[str, ...]

    def canonical_dict(self) -> dict[str, Any]:
        value = asdict(self)
        value["reason_codes"] = list(self.reason_codes)
        return value


@dataclass(frozen=True)
class ArgumentSummary:
    pointer: str
    value_type: str
    array_length: int | None
    string_length: int | None
    sensitive: bool

    def safe_dict(self) -> dict[str, Any]:
        return {
            "pointer": self.pointer,
            "valueType": self.value_type,
            "arrayLength": self.array_length,
            "stringLength": self.string_length,
            "sensitive": self.sensitive,
        }


@dataclass(frozen=True)
class Challenge:
    challenge_id: str
    binding: CallBinding
    binding_hash: str
    argument_hash: str
    argument_summary: tuple[ArgumentSummary, ...]
    created_at: float
    expires_at: float
    created_deadline: float = field(repr=False, compare=False)
    expires_deadline: float = field(repr=False, compare=False)

    def safe_dict(self) -> dict[str, Any]:
        return {
            "challengeId": self.challenge_id,
            "bindingHash": self.binding_hash,
            "argumentHash": self.argument_hash,
            "createdAt": self.created_at,
            "expiresAt": self.expires_at,
            "profileId": self.binding.profile_id,
            "profileRevisionId": self.binding.profile_revision_id,
            "profileName": self.binding.profile_name,
            "clientKind": self.binding.client_kind,
            "serverId": self.binding.server_id,
            "serverName": self.binding.server_name,
            "toolId": self.binding.tool_id,
            "toolName": self.binding.tool_name,
            "runtimeName": self.binding.runtime_name,
            "mcpConfigRevisionId": self.binding.mcp_config_revision_id,
            "contractRevisionId": self.binding.contract_revision_id,
            "globalPolicyRevisionId": self.binding.global_policy_revision_id,
            "decision": self.binding.decision,
            "reasonCodes": list(self.binding.reason_codes),
            "argumentSummary": [item.safe_dict() for item in self.argument_summary],
        }


@dataclass(frozen=True)
class _Grant:
    challenge_id: str
    binding: CallBinding
    argument_hash: str
    expires_deadline: float


def canonical_json_hash(value: Any) -> str:
    try:
        canonical = rfc8785.dumps(value)
    except (rfc8785.CanonicalizationError, TypeError, ValueError) as error:
        raise ConfirmationError("arguments_invalid", "Tool arguments are not valid canonical JSON") from error
    return hashlib.sha256(canonical).hexdigest()


def binding_hash(binding: CallBinding) -> str:
    return canonical_json_hash(binding.canonical_dict())


def summarize_arguments(arguments: dict[str, Any]) -> tuple[ArgumentSummary, ...]:
    summaries: list[ArgumentSummary] = []
    pending: deque[tuple[str, Any, int, bool]] = deque([("", arguments, 0, False)])

    while pending and len(summaries) < MAX_SUMMARY_NODES:
        pointer, value, depth, inherited_sensitive = pending.popleft()
        if depth > MAX_SUMMARY_DEPTH:
            continue

        value_type = _value_type(value)
        summaries.append(
            ArgumentSummary(
                pointer=pointer,
                value_type=value_type,
                array_length=len(value) if isinstance(value, list) else None,
                string_length=len(value) if isinstance(value, str) else None,
                sensitive=inherited_sensitive,
            )
        )

        if isinstance(value, dict):
            for ordinal, (key, child) in enumerate(value.items()):
                child_pointer = f"{pointer}/o{ordinal}"
                if len(child_pointer) <= MAX_POINTER_LENGTH:
                    pending.append(
                        (
                            child_pointer,
                            child,
                            depth + 1,
                            inherited_sensitive or bool(_SENSITIVE_NAME.search(str(key))),
                        )
                    )
        elif isinstance(value, list):
            for index, child in enumerate(value):
                child_pointer = f"{pointer}/a{index}"
                if len(child_pointer) <= MAX_POINTER_LENGTH:
                    pending.append((child_pointer, child, depth + 1, inherited_sensitive))

    return tuple(summaries)


def _value_type(value: Any) -> str:
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "boolean"
    if isinstance(value, int | float):
        return "number"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    return "invalid"


class ConfirmationStore:
    def __init__(
        self,
        clock: Callable[[], float] = time.monotonic,
        wall_clock: Callable[[], float] = time.time,
    ):
        self._clock = clock
        self._wall_clock = wall_clock
        self._lock = RLock()
        self._challenges: dict[str, Challenge] = {}
        self._grants: dict[str, _Grant] = {}
        self._profile_rates: dict[str, deque[float]] = defaultdict(deque)
        self._expiration_observer: Callable[[CallBinding], None] | None = None

    def set_expiration_observer(self, observer: Callable[[CallBinding], None] | None) -> None:
        with self._lock:
            self._expiration_observer = observer

    def create_challenge(self, binding: CallBinding, arguments: dict[str, Any]) -> Challenge:
        now = self._clock()
        wall_now = self._wall_clock()
        argument_hash = canonical_json_hash(arguments)
        argument_summary = summarize_arguments(arguments)
        with self._lock:
            self._purge(now)
            active = len(self._challenges) + len(self._grants)
            if active >= MAX_PENDING_GLOBAL:
                raise ConfirmationError("global_pending_limit", "Too many pending ToolHub confirmations")

            profile_active = sum(
                item.binding.profile_id == binding.profile_id
                for item in (*self._challenges.values(), *self._grants.values())
            )
            if profile_active >= MAX_PENDING_PER_PROFILE:
                raise ConfirmationError("profile_pending_limit", "Too many pending confirmations for this profile")

            rate = self._profile_rates[binding.profile_id]
            while rate and rate[0] <= now - 60:
                rate.popleft()
            if len(rate) >= MAX_CHALLENGES_PER_PROFILE_MINUTE:
                raise ConfirmationError("profile_rate_limited", "ToolHub confirmation creation is rate limited")
            rate.append(now)

            challenge = Challenge(
                challenge_id=secrets.token_hex(32),
                binding=binding,
                binding_hash=binding_hash(binding),
                argument_hash=argument_hash,
                argument_summary=argument_summary,
                created_at=wall_now,
                expires_at=wall_now + CHALLENGE_TTL_SECONDS,
                created_deadline=now,
                expires_deadline=now + CHALLENGE_TTL_SECONDS,
            )
            self._challenges[challenge.challenge_id] = challenge
            return challenge

    def list_pending(self) -> list[dict[str, Any]]:
        now = self._clock()
        with self._lock:
            self._purge(now)
            return [
                item.safe_dict() for item in sorted(self._challenges.values(), key=lambda value: value.created_deadline)
            ]

    def approve(self, challenge_id: str, expected_binding_hash: str) -> dict[str, Any]:
        response, _binding = self.approve_with_binding(challenge_id, expected_binding_hash)
        return response

    def approve_with_binding(
        self,
        challenge_id: str,
        expected_binding_hash: str,
    ) -> tuple[dict[str, Any], CallBinding]:
        now = self._clock()
        with self._lock:
            challenge = self._challenges.get(challenge_id)
            if challenge is not None and challenge.expires_deadline <= now:
                self._expire_challenge(challenge_id, challenge)
                raise ConfirmationError("challenge_expired", "ToolHub confirmation challenge expired")
            self._purge(now)
            challenge = self._challenges.get(challenge_id)
            if challenge is None:
                raise ConfirmationError("challenge_unknown", "ToolHub confirmation challenge was not found")
            if not hmac.compare_digest(challenge.binding_hash, expected_binding_hash):
                raise ConfirmationError("binding_mismatch", "ToolHub confirmation binding changed")

            del self._challenges[challenge_id]
            self._grants[challenge_id] = _Grant(
                challenge_id=challenge_id,
                binding=challenge.binding,
                argument_hash=challenge.argument_hash,
                expires_deadline=now + GRANT_TTL_SECONDS,
            )
            grant_expires_at = self._wall_clock() + GRANT_TTL_SECONDS
            return (
                {
                    "challengeId": challenge_id,
                    "bindingHash": challenge.binding_hash,
                    "grantExpiresAt": grant_expires_at,
                },
                challenge.binding,
            )

    def reject(self, challenge_id: str, expected_binding_hash: str) -> dict[str, Any]:
        response, _binding = self.reject_with_binding(challenge_id, expected_binding_hash)
        return response

    def reject_with_binding(
        self,
        challenge_id: str,
        expected_binding_hash: str,
    ) -> tuple[dict[str, Any], CallBinding]:
        now = self._clock()
        with self._lock:
            challenge = self._challenges.get(challenge_id)
            if challenge is not None and challenge.expires_deadline <= now:
                self._expire_challenge(challenge_id, challenge)
                raise ConfirmationError("challenge_expired", "ToolHub confirmation challenge expired")
            self._purge(now)
            challenge = self._challenges.get(challenge_id)
            if challenge is None:
                raise ConfirmationError("challenge_unknown", "ToolHub confirmation challenge was not found")
            if not hmac.compare_digest(challenge.binding_hash, expected_binding_hash):
                raise ConfirmationError("binding_mismatch", "ToolHub confirmation binding changed")
            del self._challenges[challenge_id]
            return (
                {"challengeId": challenge_id, "bindingHash": challenge.binding_hash},
                challenge.binding,
            )

    def consume_grant(self, binding: CallBinding, arguments: dict[str, Any]) -> bool:
        now = self._clock()
        argument_hash = canonical_json_hash(arguments)
        with self._lock:
            self._purge(now)
            for challenge_id, grant in self._grants.items():
                if grant.binding == binding and hmac.compare_digest(grant.argument_hash, argument_hash):
                    del self._grants[challenge_id]
                    return True
            return False

    def clear(self) -> None:
        with self._lock:
            self._challenges.clear()
            self._grants.clear()
            self._profile_rates.clear()

    def counts(self) -> dict[str, int]:
        now = self._clock()
        with self._lock:
            self._purge(now)
            return {"pending": len(self._challenges), "grants": len(self._grants)}

    def _purge(self, now: float) -> None:
        for challenge_id, challenge in list(self._challenges.items()):
            if challenge.expires_deadline <= now:
                self._expire_challenge(challenge_id, challenge)
        self._grants = {key: value for key, value in self._grants.items() if value.expires_deadline > now}
        for profile_id, rate in list(self._profile_rates.items()):
            while rate and rate[0] <= now - 60:
                rate.popleft()
            if not rate:
                del self._profile_rates[profile_id]

    def _expire_challenge(self, challenge_id: str, challenge: Challenge) -> None:
        del self._challenges[challenge_id]
        if self._expiration_observer is not None:
            self._expiration_observer(challenge.binding)
