"""Fixed, bounded Unix-socket administration protocol for ToolHub mode."""

import asyncio
import inspect
import json
import os
import socket
import stat
from pathlib import Path
from typing import Any, Callable

from mcpm.toolhub.canary import SessionCanaryError
from mcpm.toolhub.confirmation import CallBinding, ConfirmationError, ConfirmationStore
from mcpm.toolhub.contract import capability_contract
from mcpm.toolhub.observations import ObservationRing
from mcpm.toolhub.routing import RoutingRuntime
from mcpm.toolhub.schema import RoutingBundle, parse_routing_bundle_json

MAX_ADMIN_MESSAGE_BYTES = 1024 * 1024
ADMIN_DEADLINE_SECONDS = 5

_SIMPLE_OPERATIONS = {
    "contract",
    "reload_routing",
    "observe_contracts",
    "list_confirmations",
    "status",
}
_CONFIRMATION_OPERATIONS = {"approve_confirmation", "reject_confirmation"}
_ALLOWED_OPERATIONS = _SIMPLE_OPERATIONS | _CONFIRMATION_OPERATIONS | {"drain_observations", "session_canary"}


class AdminError(ValueError):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


class AdminServer:
    def __init__(
        self,
        socket_path: Path,
        routing_path: Path,
        routing: RoutingRuntime,
        confirmations: ConfirmationStore,
        observations: ObservationRing,
        *,
        contract_observer: Callable[[], Any] | None = None,
        session_canary: Callable[[RoutingBundle], Any] | None = None,
        deadline_seconds: float = ADMIN_DEADLINE_SECONDS,
    ):
        if not 0 < deadline_seconds <= ADMIN_DEADLINE_SECONDS:
            raise ValueError(f"admin deadline must be between 0 and {ADMIN_DEADLINE_SECONDS} seconds")
        self.socket_path = socket_path
        self.routing_path = routing_path
        self.routing = routing
        self.confirmations = confirmations
        self.observations = observations
        self.contract_observer = contract_observer
        self.session_canary = session_canary
        self.deadline_seconds = deadline_seconds
        self._server: asyncio.AbstractServer | None = None
        self._socket_identity: tuple[int, int] | None = None

    async def __aenter__(self):
        await self.start()
        return self

    async def __aexit__(self, _exc_type, _exc_value, _traceback):
        await self.close()

    async def start(self) -> None:
        if self._server is not None:
            raise RuntimeError("ToolHub admin server is already running")
        self._prepare_socket_path()
        self._server = await asyncio.start_unix_server(
            self._handle_connection,
            path=self.socket_path,
            limit=MAX_ADMIN_MESSAGE_BYTES + 1,
        )
        self.confirmations.set_expiration_observer(self._record_expired_confirmation)
        os.chmod(self.socket_path, 0o660)
        info = os.lstat(self.socket_path)
        self._socket_identity = (info.st_dev, info.st_ino)

    async def close(self) -> None:
        self.confirmations.set_expiration_observer(None)
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()
            self._server = None
        try:
            info = os.lstat(self.socket_path)
        except FileNotFoundError:
            return
        if self._socket_identity == (info.st_dev, info.st_ino) and stat.S_ISSOCK(info.st_mode):
            self.socket_path.unlink()
        self._socket_identity = None

    def _prepare_socket_path(self) -> None:
        try:
            info = os.lstat(self.socket_path)
        except FileNotFoundError:
            return
        if not stat.S_ISSOCK(info.st_mode):
            raise ValueError("ToolHub admin path exists as a non-socket")

        probe = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        probe.settimeout(0.1)
        try:
            probe.connect(str(self.socket_path))
        except (ConnectionRefusedError, FileNotFoundError):
            self.socket_path.unlink()
        else:
            raise ValueError("ToolHub admin socket is already in use")
        finally:
            probe.close()

    async def _handle_connection(
        self,
        reader: asyncio.StreamReader,
        writer: asyncio.StreamWriter,
    ) -> None:
        try:
            try:
                async with asyncio.timeout(self.deadline_seconds):
                    line = await reader.readuntil(b"\n")
                    if len(line) - 1 > MAX_ADMIN_MESSAGE_BYTES:
                        raise AdminError("request_too_large", "Admin request exceeds the size limit")
                    request = self._decode_request(line[:-1])
                    response = {"ok": True, "data": await self._dispatch(request)}
            except TimeoutError:
                response = self._error_response("request_timeout", "Admin request deadline exceeded")
            except asyncio.LimitOverrunError:
                response = self._error_response("request_too_large", "Admin request exceeds the size limit")
            except asyncio.IncompleteReadError:
                response = self._error_response("request_invalid", "Admin request must end with a newline")
            except AdminError as error:
                response = self._error_response(error.code, str(error))
            except ConfirmationError as error:
                response = self._error_response(error.code, str(error))
            except SessionCanaryError as error:
                response = self._error_response(error.code, str(error))
            except ValueError:
                response = self._error_response("request_invalid", "Admin request is invalid")
            except Exception:
                response = self._error_response("internal_error", "Admin operation failed")

            encoded = json.dumps(response, separators=(",", ":"), ensure_ascii=True).encode() + b"\n"
            if len(encoded) > MAX_ADMIN_MESSAGE_BYTES + 1:
                encoded = (
                    json.dumps(
                        self._error_response("response_too_large", "Admin response exceeds the size limit"),
                        separators=(",", ":"),
                    ).encode()
                    + b"\n"
                )
            writer.write(encoded)
            await writer.drain()
        finally:
            writer.close()
            await writer.wait_closed()

    @staticmethod
    def _decode_request(payload: bytes) -> dict[str, Any]:
        try:
            value = json.loads(payload)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise AdminError("request_invalid", "Admin request must be valid JSON") from error
        if not isinstance(value, dict):
            raise AdminError("request_invalid", "Admin request must be a JSON object")
        operation = value.get("operation")
        if not isinstance(operation, str) or operation not in _ALLOWED_OPERATIONS:
            raise AdminError("operation_invalid", "Admin operation is not allowed")

        fields = set(value)
        if operation in _SIMPLE_OPERATIONS:
            expected = {"operation"}
        elif operation in _CONFIRMATION_OPERATIONS:
            expected = {"operation", "challengeId", "bindingHash"}
        elif operation == "session_canary":
            expected = {"operation", "routingBundleHash", "routingBundle"}
        else:
            expected = {"operation", "afterBootId", "afterSequence", "limit"}
        if fields != expected:
            raise AdminError("request_fields_invalid", "Admin request fields do not match the operation")
        return value

    async def _dispatch(self, request: dict[str, Any]) -> Any:
        operation = request["operation"]
        if operation == "contract":
            return capability_contract()
        if operation == "reload_routing":
            previous = self.routing.current
            updated = parse_routing_bundle_json(self.routing_path.read_bytes())
            self.routing.reload(updated)
            if (
                previous.globalPolicyRevisionId != updated.globalPolicyRevisionId
                or previous.globalPolicyHash != updated.globalPolicyHash
            ):
                self.confirmations.clear()
            return self._routing_status()
        if operation == "observe_contracts":
            if self.contract_observer is None:
                raise AdminError("contract_observer_unavailable", "Contract observation is unavailable")
            value = self.contract_observer()
            return await value if inspect.isawaitable(value) else value
        if operation == "session_canary":
            if self.session_canary is None:
                raise AdminError("session_canary_unavailable", "Session canary is unavailable")
            candidate = RoutingBundle.model_validate(request["routingBundle"])
            candidate_runtime = RoutingRuntime(candidate)
            if candidate.mode != "enforced" or request["routingBundleHash"] != candidate_runtime.bundle_hash:
                raise AdminError("routing_bundle_invalid", "Candidate routing bundle hash or mode is invalid")
            value = self.session_canary(candidate)
            result = await value if inspect.isawaitable(value) else value
            if not isinstance(result, dict) or result.get("routingBundleHash") != candidate_runtime.bundle_hash:
                raise AdminError("session_canary_invalid", "Session canary result does not match candidate routing")
            return result
        if operation == "list_confirmations":
            return {"items": self.confirmations.list_pending()}
        if operation in _CONFIRMATION_OPERATIONS:
            challenge_id = request["challengeId"]
            expected_hash = request["bindingHash"]
            if not isinstance(challenge_id, str) or len(challenge_id) != 64:
                raise AdminError("challenge_id_invalid", "Confirmation challenge ID is invalid")
            if not isinstance(expected_hash, str) or len(expected_hash) != 64:
                raise AdminError("binding_hash_invalid", "Confirmation binding hash is invalid")
            if operation == "approve_confirmation":
                response, binding = self.confirmations.approve_with_binding(challenge_id, expected_hash)
                self._record_confirmation(binding, "confirmed")
                return response
            response, binding = self.confirmations.reject_with_binding(challenge_id, expected_hash)
            self._record_confirmation(binding, "rejected")
            return response
        if operation == "drain_observations":
            after_boot_id = request["afterBootId"]
            after_sequence = request["afterSequence"]
            limit = request["limit"]
            if after_boot_id is not None and not isinstance(after_boot_id, str):
                raise AdminError("observation_cursor_invalid", "Observation boot cursor is invalid")
            if isinstance(after_sequence, bool) or not isinstance(after_sequence, int):
                raise AdminError("observation_cursor_invalid", "Observation sequence cursor is invalid")
            if isinstance(limit, bool) or not isinstance(limit, int):
                raise AdminError("observation_limit_invalid", "Observation drain limit is invalid")
            try:
                return self.observations.drain(
                    after_boot_id=after_boot_id,
                    after_sequence=after_sequence,
                    limit=limit,
                )
            except ValueError as error:
                raise AdminError("observation_request_invalid", "Observation drain request is invalid") from error
        if operation == "status":
            return {
                **self._routing_status(),
                "confirmations": self.confirmations.counts(),
                "observationBootId": self.observations.boot_id,
                "observationCount": self.observations.count(),
            }
        raise AdminError("operation_invalid", "Admin operation is not allowed")

    def _routing_status(self) -> dict[str, Any]:
        current = self.routing.current
        profiles = sorted(current.profiles, key=lambda profile: str(profile.profileId))
        return {
            "mode": current.mode,
            "relayConfigurationRevisionId": str(current.relayConfigurationRevisionId),
            "globalPolicyRevisionId": str(current.globalPolicyRevisionId),
            "routingBundleHash": self.routing.bundle_hash,
            "publishedProfileRevisions": [
                {
                    "profileId": str(profile.profileId),
                    "profileRevisionId": str(profile.profileRevisionId),
                    "profileRevisionHash": profile.profileRevisionHash,
                }
                for profile in profiles
            ],
        }

    def _record_expired_confirmation(self, binding: CallBinding) -> None:
        self._record_confirmation(binding, "expired", "timeout")

    def _record_confirmation(self, binding: CallBinding, outcome: str, error_class: str = "none") -> None:
        self.observations.record(
            profile_id=binding.profile_id,
            profile_revision_id=binding.profile_revision_id,
            server_id=binding.server_id,
            tool_id=binding.tool_id,
            decision=binding.decision,
            reason_codes=binding.reason_codes,
            outcome=outcome,
            duration_seconds=0,
            error_class=error_class,
        )

    @staticmethod
    def _error_response(code: str, message: str) -> dict[str, Any]:
        return {"ok": False, "error": {"code": code, "message": message}}
