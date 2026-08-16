"""Bounded session-routing canary over the live shared upstream pool."""

import asyncio
from collections.abc import Mapping
from typing import Any, Protocol

from mcpm.toolhub.gateway import ToolHubGateway
from mcpm.toolhub.routing import RoutingError, RoutingRuntime
from mcpm.toolhub.schema import PublishedProfile, RoutingBundle


class SessionCanaryError(RuntimeError):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


class SessionCanaryUpstream(Protocol):
    async def list_tools(self) -> list[Any]: ...

    def process_counts(self) -> Mapping[str, int]: ...


class ProxyCanaryUpstream:
    """Adapt the running FastMCP proxy without creating another upstream client."""

    def __init__(self, proxy: Any):
        self.proxy = proxy

    async def list_tools(self) -> list[Any]:
        tools = await self.proxy.get_tools()
        if not isinstance(tools, dict):
            raise SessionCanaryError("catalog_invalid", "The shared upstream catalog is invalid")
        return list(tools.values())

    def process_counts(self) -> Mapping[str, int]:
        inspect = getattr(self.proxy, "_toolhub_upstream_process_counts", None)
        if not callable(inspect):
            raise SessionCanaryError("upstream_inspection_unavailable", "Shared upstream inspection is unavailable")
        counts = inspect()
        if not isinstance(counts, Mapping):
            raise SessionCanaryError("upstream_inspection_invalid", "Shared upstream inspection is invalid")
        return counts


async def run_session_canary(bundle: RoutingBundle, upstream: SessionCanaryUpstream) -> dict[str, Any]:
    if bundle.mode != "enforced":
        raise SessionCanaryError("routing_mode_invalid", "Session canary requires enforced routing")

    profiles = {kind: _profile_for_client(bundle, kind) for kind in ("claude", "codex")}
    before_counts = _validated_process_counts(bundle, upstream.process_counts())
    routing = RoutingRuntime(bundle)
    gateway = ToolHubGateway(routing, upstream)

    claude_tools = await _catalog(gateway, routing, profiles["claude"])
    codex_tools = await _catalog(gateway, routing, profiles["codex"])
    default_tools = await _catalog_default(gateway, routing)

    invalid_code = ""
    try:
        routing.bind(_unknown_profile_name(bundle))
    except RoutingError as error:
        invalid_code = error.code
    if invalid_code != "profile_unknown":
        raise SessionCanaryError("invalid_profile_behavior", "Unknown Profile did not fail closed")

    concurrent = await asyncio.gather(
        _catalog(gateway, routing, profiles["claude"]),
        _catalog(gateway, routing, profiles["codex"]),
    )
    if len(concurrent) != 2:
        raise SessionCanaryError("concurrent_session_invalid", "Concurrent session canary did not complete")

    after_counts = _validated_process_counts(bundle, upstream.process_counts())
    if after_counts != before_counts:
        raise SessionCanaryError("upstream_process_count_changed", "Session canary changed the shared upstream set")

    return {
        "routingBundleHash": routing.bundle_hash,
        "profiles": [
            _profile_result(profiles["claude"], len(claude_tools)),
            _profile_result(profiles["codex"], len(codex_tools)),
        ],
        "missingProfile": {
            "behavior": "default",
            "profileId": None,
            "profileRevisionId": None,
            "toolCount": len(default_tools),
        },
        "invalidProfileErrorCode": invalid_code,
        "concurrentSessionCount": len(concurrent),
        "upstreamProcesses": [
            {"serverId": str(server.serverId), "processCount": after_counts[server.serverName]}
            for server in bundle.servers
        ],
    }


def _profile_for_client(bundle: RoutingBundle, client_kind: str) -> PublishedProfile:
    profiles = sorted(
        (profile for profile in bundle.profiles if profile.clientKind == client_kind),
        key=lambda profile: str(profile.profileId),
    )
    if not profiles:
        raise SessionCanaryError("profile_missing", f"Published {client_kind} Profile is required")
    return profiles[0]


def _unknown_profile_name(bundle: RoutingBundle) -> str:
    published = {profile.profileName for profile in bundle.profiles}
    candidate = "__unknown_profile__"
    while candidate in published:
        candidate += "_"
    return candidate


async def _catalog(
    gateway: ToolHubGateway,
    routing: RoutingRuntime,
    profile: PublishedProfile,
) -> list[Any]:
    try:
        binding = routing.bind(profile.profileName)
    except RoutingError as error:
        raise SessionCanaryError("profile_binding_invalid", "Explicit Profile binding failed") from error
    return await _list_tools(gateway, binding)


async def _catalog_default(gateway: ToolHubGateway, routing: RoutingRuntime) -> list[Any]:
    try:
        return await _list_tools(gateway, routing.bind_default())
    except SessionCanaryError:
        raise
    except Exception as error:
        raise SessionCanaryError("catalog_unavailable", "Shared upstream catalog is unavailable") from error


async def _list_tools(gateway: ToolHubGateway, binding: Any) -> list[Any]:
    try:
        return await gateway.list_tools(binding)
    except SessionCanaryError:
        raise
    except Exception as error:
        raise SessionCanaryError("catalog_unavailable", "Shared upstream catalog is unavailable") from error


def _profile_result(profile: PublishedProfile, tool_count: int) -> dict[str, Any]:
    return {
        "clientKind": profile.clientKind,
        "profileId": str(profile.profileId),
        "profileRevisionId": str(profile.profileRevisionId),
        "toolCount": tool_count,
    }


def _validated_process_counts(bundle: RoutingBundle, counts: Mapping[str, int]) -> dict[str, int]:
    expected = {server.serverName for server in bundle.servers}
    if set(counts) != expected:
        raise SessionCanaryError("upstream_process_set_invalid", "Shared upstream set does not match candidate routing")
    result: dict[str, int] = {}
    for name, value in counts.items():
        if isinstance(value, bool) or not isinstance(value, int) or value != 1:
            raise SessionCanaryError("upstream_process_count_invalid", "Shared upstream process count must remain one")
        result[name] = value
    return result
