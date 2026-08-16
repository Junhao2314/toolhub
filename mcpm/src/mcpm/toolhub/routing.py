"""Immutable routing snapshots and exact MCP session bindings."""

from dataclasses import dataclass, field
from threading import RLock
from typing import Literal
from uuid import UUID

from mcpm.toolhub.confirmation import CallBinding, canonical_json_hash
from mcpm.toolhub.policy import PolicyDecision, effective
from mcpm.toolhub.schema import ContractTool, ProfileServerRouting, PublishedProfile, RoutingBundle, ServerContract

ClientKind = Literal["claude", "codex"]

_DEFAULT_PROFILE_ID = UUID(int=0)
_DEFAULT_PROFILE_REVISION_ID = UUID(int=0)
_DEFAULT_PROFILE_HASH = "0" * 64
_DEFAULT_PROFILE_NAME = "default"


class RoutingError(ValueError):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


class RoutingDenied(RoutingError):
    def __init__(self, code: str, message: str, reason_codes: tuple[str, ...] = ()):
        super().__init__(code, message)
        self.reasonCodes = reason_codes


@dataclass(frozen=True)
class SessionBinding:
    profileId: UUID
    profileRevisionId: UUID
    # Kept as profile metadata for existing confirmation/telemetry envelopes.
    # It is never used to authorize or select a session.
    clientKind: ClientKind
    _bundle: RoutingBundle = field(repr=False, compare=False)
    _profile: PublishedProfile = field(repr=False, compare=False)
    is_default: bool = False


@dataclass(frozen=True)
class _ToolIdentity:
    server: ServerContract
    tool: ContractTool


class RoutingRuntime:
    def __init__(self, bundle: RoutingBundle):
        self._lock = RLock()
        self._bundle = bundle
        self._bundle_hash = canonical_json_hash(bundle.model_dump(mode="json"))

    @property
    def current(self) -> RoutingBundle:
        with self._lock:
            return self._bundle

    @property
    def bundle_hash(self) -> str:
        with self._lock:
            return self._bundle_hash

    def reload(self, bundle: RoutingBundle) -> None:
        with self._lock:
            self._bundle = bundle
            self._bundle_hash = canonical_json_hash(bundle.model_dump(mode="json"))

    def bind(self, profile_name: str) -> SessionBinding:
        bundle = self.current
        if not isinstance(profile_name, str) or not profile_name.strip():
            raise RoutingError("profile_missing", "profile is required")

        profile = next((item for item in bundle.profiles if item.profileName == profile_name), None)
        if profile is None:
            raise RoutingError("profile_unknown", "ToolHub profile is not published")

        return SessionBinding(
            profileId=profile.profileId,
            profileRevisionId=profile.profileRevisionId,
            clientKind=profile.clientKind,
            _bundle=bundle,
            _profile=profile,
        )

    def bind_default(self) -> SessionBinding:
        """Bind the implicit all-tools session used when no profile is given."""
        bundle = self.current
        profile = PublishedProfile(
            profileId=_DEFAULT_PROFILE_ID,
            profileRevisionId=_DEFAULT_PROFILE_REVISION_ID,
            profileRevisionHash=_DEFAULT_PROFILE_HASH,
            profileName=_DEFAULT_PROFILE_NAME,
            clientKind="claude",
            servers=tuple(
                ProfileServerRouting(
                    serverId=server.serverId,
                    mcpConfigRevisionId=server.mcpConfigRevisionId,
                    acceptedContractRevisionId=server.acceptedContractRevisionId,
                    visibilityMode="all_accepted",
                    toolOverrides=(),
                    toolRules=(),
                )
                for server in bundle.servers
            ),
        )
        return SessionBinding(
            profileId=profile.profileId,
            profileRevisionId=profile.profileRevisionId,
            clientKind=profile.clientKind,
            _bundle=bundle,
            _profile=profile,
            is_default=True,
        )

    def visible_tool_names(self, binding: SessionBinding, upstream_names: list[str]) -> list[str]:
        return [name for name in upstream_names if self._is_visible(binding, name)]

    def authorize(self, binding: SessionBinding, runtime_name: str) -> PolicyDecision:
        identity = self._tool_identity(binding._bundle, runtime_name)
        if identity is None:
            raise RoutingDenied("tool_unknown", "Tool is not in the bound accepted contract")

        if not self._profile_visibility(binding, identity):
            raise RoutingDenied("tool_hidden", "Tool is hidden by the bound ToolHub profile")

        current_identity = self._current_tool(identity)
        if current_identity is None or current_identity.tool.paused:
            raise RoutingDenied("tool_paused", "Tool is paused because its contract changed")

        profile_decision = self._profile_decision(binding._profile, identity)
        decision = effective(current_identity.tool.globalDecision, profile_decision)
        reason_codes = current_identity.tool.reasonCodes
        if profile_decision is not None:
            reason_codes = (*reason_codes, "profile-rule")
        if decision == "deny":
            raise RoutingDenied(
                "tool_denied",
                "Tool is denied by ToolHub call policy",
                reason_codes,
            )
        return PolicyDecision(decision=decision, reasonCodes=reason_codes)

    def call_binding(
        self,
        binding: SessionBinding,
        runtime_name: str,
        decision: PolicyDecision,
    ) -> CallBinding:
        identity = self._tool_identity(binding._bundle, runtime_name)
        if identity is None:
            raise RoutingDenied("tool_unknown", "Tool is not in the bound accepted contract")
        bound_server = identity.server
        if bound_server.acceptedContractRevisionId is None or bound_server.acceptedContractHash is None:
            raise RoutingDenied("contract_unavailable", "The accepted ToolHub contract is unavailable")
        current = self.current

        return CallBinding(
            profile_id=str(binding._profile.profileId),
            profile_revision_id=str(binding._profile.profileRevisionId),
            profile_revision_hash=binding._profile.profileRevisionHash,
            profile_name=binding._profile.profileName,
            client_kind=binding.clientKind,
            server_id=str(bound_server.serverId),
            server_name=bound_server.serverName,
            tool_id=str(identity.tool.toolId),
            tool_name=identity.tool.name,
            runtime_name=runtime_name,
            mcp_config_revision_id=str(bound_server.mcpConfigRevisionId),
            contract_revision_id=str(bound_server.acceptedContractRevisionId),
            contract_revision_hash=bound_server.acceptedContractHash,
            global_policy_revision_id=str(current.globalPolicyRevisionId),
            global_policy_hash=current.globalPolicyHash,
            decision=decision.decision,
            reason_codes=decision.reasonCodes,
        )

    def _is_visible(self, binding: SessionBinding, runtime_name: str) -> bool:
        identity = self._tool_identity(binding._bundle, runtime_name)
        if identity is None:
            return False

        if not self._profile_visibility(binding, identity):
            return False

        current_identity = self._current_tool(identity)
        if current_identity is None or current_identity.tool.paused:
            return False
        profile_decision = self._profile_decision(binding._profile, identity)
        return effective(current_identity.tool.globalDecision, profile_decision) != "deny"

    def _profile_visibility(self, binding: SessionBinding, identity: _ToolIdentity) -> bool:
        if binding.is_default:
            return True
        profile_server = next(
            (server for server in binding._profile.servers if server.serverId == identity.server.serverId), None
        )
        if profile_server is None:
            return False

        visible = profile_server.visibilityMode == "all_accepted"
        override = next((item for item in profile_server.toolOverrides if item.toolId == identity.tool.toolId), None)
        return override.visible if override is not None else visible

    @staticmethod
    def _profile_decision(profile: PublishedProfile | None, identity: _ToolIdentity):
        if profile is None:
            return None
        profile_server = next(
            (server for server in profile.servers if server.serverId == identity.server.serverId), None
        )
        if profile_server is None:
            return None
        rule = next((item for item in profile_server.toolRules if item.toolId == identity.tool.toolId), None)
        return rule.decision if rule is not None else None

    def _current_tool(self, bound: _ToolIdentity) -> _ToolIdentity | None:
        bundle = self.current
        for server in bundle.servers:
            if server.serverId != bound.server.serverId:
                continue
            if server.mcpConfigRevisionId != bound.server.mcpConfigRevisionId:
                return None
            tool = next((item for item in server.tools if item.toolId == bound.tool.toolId), None)
            if tool is None:
                return None
            same_contract = (
                server.acceptedContractRevisionId == bound.server.acceptedContractRevisionId
                and server.acceptedContractHash == bound.server.acceptedContractHash
            )
            if not same_contract and (
                tool.name != bound.tool.name
                or tool.inputSchema != bound.tool.inputSchema
                or tool.outputSchema != bound.tool.outputSchema
                or tool.annotations != bound.tool.annotations
            ):
                return None
            return _ToolIdentity(server, tool)
        return None

    @staticmethod
    def _tool_identity(bundle: RoutingBundle, runtime_name: str) -> _ToolIdentity | None:
        prefix_names = len(bundle.servers) > 1
        for server in bundle.servers:
            for tool in server.tools:
                expected_name = f"{server.serverName}_{tool.name}" if prefix_names else tool.name
                if runtime_name == expected_name:
                    return _ToolIdentity(server, tool)
        return None
