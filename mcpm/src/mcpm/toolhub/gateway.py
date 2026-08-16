"""Profile-aware gateway over one shared upstream MCP pool."""

import time
from collections.abc import Sequence
from typing import Any, NoReturn, Protocol

from fastmcp import FastMCP
from fastmcp.exceptions import ToolError
from fastmcp.server.low_level import MiddlewareServerSession
from fastmcp.server.middleware.middleware import CallNext, Middleware, MiddlewareContext
from fastmcp.tools.tool import Tool, ToolResult
from mcp.shared.exceptions import McpError
from mcp.shared.message import ServerMessageMetadata
from mcp.types import (
    INVALID_PARAMS,
    CallToolRequestParams,
    CallToolResult,
    ErrorData,
    InitializeRequest,
    ListToolsRequest,
    TextContent,
)

from mcpm.toolhub.confirmation import ConfirmationError, ConfirmationStore
from mcpm.toolhub.observations import ObservationRing
from mcpm.toolhub.policy import PolicyDecision
from mcpm.toolhub.routing import (
    RoutingDenied,
    RoutingError,
    RoutingRuntime,
    SessionBinding,
)

_ROUTING_ATTRIBUTE = "_toolhub_routing"
_BINDING_ATTRIBUTE = "_toolhub_binding"
_HOOK_ATTRIBUTE = "_toolhub_initialize_hook_installed"


class SharedUpstream(Protocol):
    async def list_tools(self) -> list[Any]: ...

    async def call_tool(self, name: str, arguments: dict[str, Any]) -> Any: ...


class GatewayError(RuntimeError):
    def __init__(self, code: str, message: str, reason_codes: tuple[str, ...] = ()):
        super().__init__(message)
        self.code = code
        self.reasonCodes = reason_codes


class NotDispatchedError(RuntimeError):
    """The upstream adapter proved that no tool execution was attempted."""


class ToolHubGateway:
    def __init__(self, routing: RoutingRuntime, upstream: SharedUpstream):
        self.routing = routing
        self.upstream = upstream

    async def list_tools(self, binding: SessionBinding) -> list[Any]:
        tools = await self.upstream.list_tools()
        names = [self._tool_name(tool) for tool in tools]
        visible = set(self.routing.visible_tool_names(binding, names))
        return [tool for tool, name in zip(tools, names, strict=True) if name in visible]

    async def call_tool(self, binding: SessionBinding, name: str, arguments: dict[str, Any]) -> Any:
        try:
            decision = self.routing.authorize(binding, name)
        except RoutingDenied as error:
            raise GatewayError(error.code, str(error), error.reasonCodes) from error

        if decision.decision == "confirm":
            raise GatewayError(
                "confirmation_required",
                "ToolHub confirmation is required before dispatch",
                decision.reasonCodes,
            )

        return await self.upstream.call_tool(name, arguments)

    @staticmethod
    def _tool_name(tool: Any) -> str:
        if isinstance(tool, str):
            return tool
        name = getattr(tool, "key", None) or getattr(tool, "name", None)
        if not isinstance(name, str):
            raise TypeError("upstream tool has no stable name")
        return name


def install_toolhub_session_binding(server: FastMCP, routing: RoutingRuntime) -> None:
    """Bind ToolHub profiles before FastMCP completes HTTP initialization."""

    setattr(server, _ROUTING_ATTRIBUTE, routing)
    if getattr(MiddlewareServerSession, _HOOK_ATTRIBUTE, False):
        return

    # FastMCP 2.13 runs initialize middleware before its request context exists.
    original = MiddlewareServerSession._received_request

    async def received_request(session, responder):
        session_routing = getattr(session.fastmcp, _ROUTING_ATTRIBUTE, None)
        if session_routing is not None and isinstance(responder.request.root, InitializeRequest):
            error = _bind_initialize_request(session, session_routing, responder.message_metadata)
            if error is not None:
                with responder:
                    await responder.respond(error)
                return
        await original(session, responder)

    MiddlewareServerSession._received_request = received_request
    setattr(MiddlewareServerSession, _HOOK_ATTRIBUTE, True)


def _bind_initialize_request(
    session: MiddlewareServerSession,
    routing: RoutingRuntime,
    metadata: Any,
) -> ErrorData | None:
    request = metadata.request_context if isinstance(metadata, ServerMessageMetadata) else None
    query_params = getattr(request, "query_params", None)
    if query_params is None:
        return _error_data("transport_unsupported", "ToolHub routing requires an HTTP MCP session")

    try:
        profile_values = query_params.getlist("profile")
        if len(profile_values) > 1:
            raise RoutingError("profile_ambiguous", "profile must be supplied exactly once")
        if not profile_values:
            binding = routing.bind_default()
        elif not profile_values[0].strip():
            raise RoutingError("profile_missing", "profile is required")
        else:
            binding = routing.bind(profile_values[0])
    except RoutingError as error:
        return _error_data(error.code, str(error))

    setattr(session, _BINDING_ATTRIBUTE, binding)
    return None


def _error_data(code: str, message: str) -> ErrorData:
    return ErrorData(
        code=INVALID_PARAMS,
        message=message,
        data={"toolhubCode": code},
    )


class ToolHubToolErrorResult:
    """Tool-level error that survives FastMCP's result normalization."""

    def __init__(
        self,
        code: str,
        message: str,
        reason_codes: tuple[str, ...] = (),
        details: dict[str, Any] | None = None,
    ):
        self.code = code
        self.message = message
        self.reason_codes = reason_codes
        self.details = details or {}

    def to_mcp_result(self) -> CallToolResult:
        return CallToolResult(
            content=[TextContent(type="text", text=self.message)],
            structuredContent={
                "toolhubError": {
                    "code": self.code,
                    "reasonCodes": list(self.reason_codes),
                    **self.details,
                }
            },
            isError=True,
        )


class ToolHubRoutingMiddleware(Middleware):
    """Filter and authorize tools for a session bound during initialization."""

    def __init__(
        self,
        routing: RoutingRuntime,
        *,
        confirmations: ConfirmationStore | None = None,
        observations: ObservationRing | None = None,
    ):
        self.routing = routing
        self.confirmations = confirmations
        self.observations = observations

    async def on_list_tools(
        self,
        context: MiddlewareContext[ListToolsRequest],
        call_next: CallNext[ListToolsRequest, Sequence[Tool]],
    ) -> Sequence[Tool]:
        binding = self._binding(context)
        tools = await call_next(context)
        names = [ToolHubGateway._tool_name(tool) for tool in tools]
        visible = set(self.routing.visible_tool_names(binding, names))
        return [tool for tool, name in zip(tools, names, strict=True) if name in visible]

    async def on_call_tool(
        self,
        context: MiddlewareContext[CallToolRequestParams],
        call_next: CallNext[CallToolRequestParams, ToolResult],
    ) -> ToolResult | ToolHubToolErrorResult:
        try:
            binding = self._binding(context)
            decision = self.routing.authorize(binding, context.message.name)
        except McpError as error:
            data = error.error.data if isinstance(error.error.data, dict) else {}
            code = data.get("toolhubCode", "toolhub_error")
            return ToolHubToolErrorResult(code, error.error.message)
        except RoutingDenied as error:
            self._record_denied(binding, context.message.name, error)
            return ToolHubToolErrorResult(error.code, str(error), error.reasonCodes)

        if decision.decision == "confirm":
            if self.confirmations is None:
                return ToolHubToolErrorResult(
                    "confirmation_required",
                    "ToolHub confirmation is required before dispatch",
                    decision.reasonCodes,
                )
            try:
                call_binding = self.routing.call_binding(binding, context.message.name, decision)
                granted = self.confirmations.consume_grant(call_binding, context.message.arguments or {})
                if not granted:
                    challenge = self.confirmations.create_challenge(
                        call_binding,
                        context.message.arguments or {},
                    )
                    self._record(call_binding, "confirmation_required", 0, "confirmation")
                    return ToolHubToolErrorResult(
                        "confirmation_required",
                        "ToolHub confirmation is required before dispatch",
                        decision.reasonCodes,
                        {
                            "challengeId": challenge.challenge_id,
                            "bindingHash": challenge.binding_hash,
                            "expiresAt": challenge.expires_at,
                        },
                    )
            except ConfirmationError as error:
                error_class = "rate_limited" if error.code == "profile_rate_limited" else "confirmation"
                self._record(call_binding, "failed", 0, error_class)
                return ToolHubToolErrorResult(error.code, str(error), decision.reasonCodes)
            except RoutingDenied as error:
                return ToolHubToolErrorResult(error.code, str(error), error.reasonCodes)

            started = time.perf_counter()
            try:
                result = await call_next(context)
            except NotDispatchedError:
                self._record(call_binding, "not_executed", time.perf_counter() - started, "transport")
                return ToolHubToolErrorResult(
                    "not_executed",
                    "The upstream adapter proved that execution did not start",
                    decision.reasonCodes,
                )
            except ToolError:
                self._record(call_binding, "failed", time.perf_counter() - started, "upstream")
                return ToolHubToolErrorResult(
                    "execution_failed",
                    "The upstream tool reported a failure",
                    decision.reasonCodes,
                )
            except Exception:
                self._record(call_binding, "unknown", time.perf_counter() - started, "transport")
                return ToolHubToolErrorResult(
                    "execution_unknown",
                    "Upstream execution may have completed; inspect state before retrying",
                    decision.reasonCodes,
                )
            self._record(call_binding, "executed", time.perf_counter() - started, "none")
            return result

        try:
            call_binding = self.routing.call_binding(binding, context.message.name, decision)
        except RoutingDenied:
            return await call_next(context)

        started = time.perf_counter()
        try:
            result = await call_next(context)
        except NotDispatchedError:
            self._record(call_binding, "not_executed", time.perf_counter() - started, "transport")
            return ToolHubToolErrorResult(
                "not_executed",
                "The upstream adapter proved that execution did not start",
                decision.reasonCodes,
            )
        except ToolError:
            self._record(call_binding, "failed", time.perf_counter() - started, "upstream")
            raise
        except Exception:
            self._record(call_binding, "unknown", time.perf_counter() - started, "transport")
            raise
        self._record(call_binding, "executed", time.perf_counter() - started, "none")
        return result

    def _record_denied(self, binding: SessionBinding, runtime_name: str, error: RoutingDenied) -> None:
        if self.observations is None:
            return
        decision = PolicyDecision(decision="deny", reasonCodes=error.reasonCodes or (error.code,))
        try:
            call_binding = self.routing.call_binding(binding, runtime_name, decision)
        except RoutingDenied:
            return
        self._record(call_binding, "denied", 0, "policy")

    def _record(self, binding, outcome: str, duration: float, error_class: str) -> None:
        if self.observations is None:
            return
        self.observations.record(
            profile_id=binding.profile_id,
            profile_revision_id=binding.profile_revision_id,
            server_id=binding.server_id,
            tool_id=binding.tool_id,
            decision=binding.decision,
            reason_codes=binding.reason_codes,
            outcome=outcome,
            duration_seconds=duration,
            error_class=error_class,
        )

    def _binding(self, context: MiddlewareContext[Any]) -> SessionBinding:
        binding = getattr(self._session(context), _BINDING_ATTRIBUTE, None)
        if not isinstance(binding, SessionBinding):
            self._fail("session_unbound", "ToolHub session has no validated profile binding")
        return binding

    @staticmethod
    def _session(context: MiddlewareContext[Any]) -> Any:
        if context.fastmcp_context is None:
            ToolHubRoutingMiddleware._fail("session_unbound", "ToolHub session context is unavailable")
        try:
            return context.fastmcp_context.session
        except (RuntimeError, ValueError):
            ToolHubRoutingMiddleware._fail("session_unbound", "ToolHub session context is unavailable")

    @staticmethod
    def _fail(code: str, message: str) -> NoReturn:
        raise McpError(_error_data(code, message))
