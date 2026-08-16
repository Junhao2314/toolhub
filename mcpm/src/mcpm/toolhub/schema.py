"""Strict, immutable ToolHub routing bundle schema."""

import json
import re
from typing import Annotated, Any, Literal, Self
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field, StringConstraints, model_validator

MAX_PROFILES = 100
MAX_SERVERS = 500
MAX_TOOLS = 10_000
MAX_RULES = 20_000
MAX_ROUTING_BUNDLE_BYTES = 16 * 1024 * 1024
MAX_ROUTING_BUNDLE_DEPTH = 64

SHA256 = Annotated[str, StringConstraints(pattern=r"^[0-9a-f]{64}$")]
Name = Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=256)]
ReasonCode = Annotated[str, StringConstraints(pattern=r"^[a-z0-9][a-z0-9._-]{0,127}$")]
Decision = Literal["allow", "confirm", "deny"]
ClientKind = Literal["claude", "codex"]
VisibilityMode = Literal["all_accepted", "selected", "hidden"]

_FORBIDDEN_FIELD_NAMES = {
    "secretvalue",
    "secretvalues",
    "arguments",
    "result",
    "results",
    "prompt",
    "prompts",
    "rawerror",
    "sessionid",
}
_DECISION_RANK = {"allow": 0, "confirm": 1, "deny": 2}


def _normalized_field_name(value: str) -> str:
    return re.sub(r"[^a-z0-9]", "", value.lower())


def _validate_tree(
    value: Any,
    depth: int = 0,
    schema_data: bool = False,
    path: tuple[str, ...] = (),
) -> None:
    if depth > MAX_ROUTING_BUNDLE_DEPTH:
        raise ValueError(f"routing bundle depth exceeds {MAX_ROUTING_BUNDLE_DEPTH}")

    if isinstance(value, dict):
        for key, child in value.items():
            if not isinstance(key, str):
                raise ValueError("routing bundle object keys must be strings")
            normalized = _normalized_field_name(key)
            if not schema_data and normalized in _FORBIDDEN_FIELD_NAMES:
                raise ValueError(f"forbidden field in routing bundle: {key}")
            child_is_schema_data = schema_data or (
                path == ("servers", "[]", "tools", "[]")
                and normalized in {"inputschema", "outputschema"}
            )
            _validate_tree(child, depth + 1, child_is_schema_data, (*path, normalized))
    elif isinstance(value, (list, tuple)):
        for child in value:
            _validate_tree(child, depth + 1, schema_data, (*path, "[]"))


def _reject_duplicates(values: list[Any], label: str) -> None:
    if len(values) != len(set(values)):
        raise ValueError(f"duplicate {label}")


class StrictFrozenModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


class ContractTool(StrictFrozenModel):
    toolId: UUID
    name: Name
    inputSchema: dict[str, Any]
    outputSchema: dict[str, Any] | None
    annotations: dict[str, Any]
    globalDecision: Decision
    reasonCodes: Annotated[tuple[ReasonCode, ...], Field(max_length=32)]
    paused: bool = False


class ServerContract(StrictFrozenModel):
    serverId: UUID
    serverName: Name
    mcpConfigRevisionId: UUID
    acceptedContractRevisionId: UUID | None
    acceptedContractHash: SHA256 | None
    tools: tuple[ContractTool, ...]

    @model_validator(mode="after")
    def validate_contract_pointer(self) -> Self:
        if (self.acceptedContractRevisionId is None) != (self.acceptedContractHash is None):
            raise ValueError("accepted contract revision and hash must be provided together")
        _reject_duplicates([tool.toolId for tool in self.tools], "tool id")
        _reject_duplicates([tool.name for tool in self.tools], "tool name")
        return self


class ToolVisibilityOverride(StrictFrozenModel):
    toolId: UUID
    visible: bool


class ToolPolicyRule(StrictFrozenModel):
    toolId: UUID
    decision: Decision


class ProfileServerRouting(StrictFrozenModel):
    serverId: UUID
    mcpConfigRevisionId: UUID
    acceptedContractRevisionId: UUID | None
    # A profile that does not specify a visibility mode should expose every
    # tool from its pinned accepted contract. Operators can still opt into the
    # narrower `selected` or `hidden` modes explicitly.
    visibilityMode: VisibilityMode = "all_accepted"
    toolOverrides: tuple[ToolVisibilityOverride, ...]
    toolRules: tuple[ToolPolicyRule, ...]

    @model_validator(mode="after")
    def validate_tool_assignments(self) -> Self:
        _reject_duplicates([override.toolId for override in self.toolOverrides], "tool visibility override")
        _reject_duplicates([rule.toolId for rule in self.toolRules], "tool policy rule")
        return self


class PublishedProfile(StrictFrozenModel):
    profileId: UUID
    profileRevisionId: UUID
    profileRevisionHash: SHA256
    profileName: Name
    clientKind: ClientKind
    servers: tuple[ProfileServerRouting, ...]

    @model_validator(mode="after")
    def validate_servers(self) -> Self:
        _reject_duplicates([server.serverId for server in self.servers], "profile server")
        return self


class RoutingBundle(StrictFrozenModel):
    schemaVersion: Literal[1]
    mode: Literal["compatibility", "enforced"]
    relayConfigurationRevisionId: UUID
    relayConfigurationHash: SHA256
    globalPolicyRevisionId: UUID
    globalPolicyHash: SHA256
    defaultProfileId: UUID | None
    servers: Annotated[tuple[ServerContract, ...], Field(max_length=MAX_SERVERS)]
    profiles: Annotated[tuple[PublishedProfile, ...], Field(max_length=MAX_PROFILES)]

    @model_validator(mode="before")
    @classmethod
    def validate_untyped_tree(cls, value: Any) -> Any:
        _validate_tree(value)
        return value

    @model_validator(mode="after")
    def validate_bundle_invariants(self) -> Self:
        _reject_duplicates([server.serverId for server in self.servers], "server id")
        _reject_duplicates([server.serverName for server in self.servers], "server name")
        _reject_duplicates([profile.profileId for profile in self.profiles], "profile id")
        _reject_duplicates([profile.profileName for profile in self.profiles], "profile name")

        prefix_names = len(self.servers) > 1
        runtime_names = [
            f"{server.serverName}_{tool.name}" if prefix_names else tool.name
            for server in self.servers
            for tool in server.tools
        ]
        _reject_duplicates(runtime_names, "runtime tool name")
        server_names = [server.serverName for server in self.servers]
        for server_name in server_names:
            if any(
                other_name != server_name and other_name.startswith(f"{server_name}_") for other_name in server_names
            ):
                raise ValueError("ambiguous server name prefix")

        if sum(len(server.tools) for server in self.servers) > MAX_TOOLS:
            raise ValueError(f"tools exceed limit of {MAX_TOOLS}")
        if sum(len(server.toolRules) for profile in self.profiles for server in profile.servers) > MAX_RULES:
            raise ValueError(f"rules exceed limit of {MAX_RULES}")

        profiles_by_id = {profile.profileId: profile for profile in self.profiles}
        if self.defaultProfileId is not None and self.defaultProfileId not in profiles_by_id:
            raise ValueError("defaultProfileId must reference a published profile")

        servers_by_id = {server.serverId: server for server in self.servers}
        if self.mode == "enforced":
            for server in self.servers:
                if server.acceptedContractRevisionId is None:
                    raise ValueError("enforced mode requires every server to pin an accepted contract")

        for profile in self.profiles:
            for profile_server in profile.servers:
                server = servers_by_id.get(profile_server.serverId)
                if server is None:
                    raise ValueError("profile server pin references an unknown server")
                if profile_server.mcpConfigRevisionId != server.mcpConfigRevisionId:
                    raise ValueError("profile MCP config pin does not match the relay server")
                if profile_server.acceptedContractRevisionId != server.acceptedContractRevisionId:
                    raise ValueError("profile accepted contract pin does not match the relay server")
                if self.mode == "enforced" and profile_server.acceptedContractRevisionId is None:
                    raise ValueError("enforced mode requires every profile server to pin an accepted contract")

                tools_by_id = {tool.toolId: tool for tool in server.tools}
                for override in profile_server.toolOverrides:
                    if override.toolId not in tools_by_id:
                        raise ValueError("tool visibility override references an unknown server tool")
                for rule in profile_server.toolRules:
                    tool = tools_by_id.get(rule.toolId)
                    if tool is None:
                        raise ValueError("tool policy rule references an unknown server tool")
                    if _DECISION_RANK[rule.decision] < _DECISION_RANK[tool.globalDecision]:
                        raise ValueError("profile tool policy rule cannot loosen the global decision")

        return self


def parse_routing_bundle_json(payload: str | bytes | bytearray) -> RoutingBundle:
    encoded = payload.encode("utf-8") if isinstance(payload, str) else bytes(payload)
    if len(encoded) > MAX_ROUTING_BUNDLE_BYTES:
        raise ValueError(f"routing bundle size exceeds {MAX_ROUTING_BUNDLE_BYTES} bytes")

    value = json.loads(encoded)
    return RoutingBundle.model_validate(value)
