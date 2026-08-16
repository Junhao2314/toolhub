"""
FastMCP proxy factory for MCPM server aggregation.
"""

import logging
from contextlib import suppress
from typing import Any, Dict, List, Optional

from fastmcp import FastMCP
from fastmcp.mcp_config import MCPConfig, RemoteMCPServer, StdioMCPServer
from fastmcp.server.proxy import ProxyClient, ProxyToolManager

from mcpm.core.schema import CustomServerConfig, RemoteServerConfig, ServerConfig, STDIOServerConfig
from mcpm.monitor.base import AccessMonitor, SessionTransport
from mcpm.monitor.sqlite import SQLiteAccessMonitor
from mcpm.toolhub.confirmation import ConfirmationStore
from mcpm.toolhub.gateway import NotDispatchedError, ToolHubRoutingMiddleware, install_toolhub_session_binding
from mcpm.toolhub.observations import MAX_OBSERVATIONS, ObservationRing
from mcpm.toolhub.routing import RoutingRuntime

# FastMCP config models are available if needed in the future
# from .config import create_mcp_config, create_stdio_server_config, create_remote_server_config
from .middleware import MCPMAuthMiddleware, MCPMUnifiedTrackingMiddleware

logger = logging.getLogger(__name__)


async def _reject_toolhub_sampling(*_args, **_kwargs):
    raise RuntimeError("ToolHub relay disables upstream sampling")


async def _reject_toolhub_elicitation(*_args, **_kwargs):
    raise RuntimeError("ToolHub relay disables upstream elicitation")


async def _discard_toolhub_callback(*_args, **_kwargs) -> None:
    return None


class ToolHubProxyClient(ProxyClient):
    """Shared client that never forwards callbacks into a downstream context."""

    def __init__(self, transport, **kwargs):
        kwargs.setdefault("roots", [])
        kwargs.setdefault("sampling_handler", _reject_toolhub_sampling)
        kwargs.setdefault("elicitation_handler", _reject_toolhub_elicitation)
        kwargs.setdefault("log_handler", _discard_toolhub_callback)
        kwargs.setdefault("progress_handler", _discard_toolhub_callback)
        super().__init__(transport, **kwargs)

    async def __aenter__(self):
        try:
            return await super().__aenter__()
        except NotDispatchedError:
            raise
        except Exception as error:
            raise NotDispatchedError("The upstream MCP session was not established") from error


class ToolHubProxyToolManager(ProxyToolManager):
    """Expose the exact boundary before an upstream tools/call is attempted."""

    async def get_tool(self, key: str):
        try:
            return await super().get_tool(key)
        except NotDispatchedError:
            raise
        except Exception as error:
            raise NotDispatchedError("The upstream tool catalog was unavailable") from error

    async def call_tool(self, key: str, arguments: dict[str, Any]):
        try:
            tool = await self.get_tool(key)
        except NotDispatchedError:
            raise
        except Exception as error:
            raise NotDispatchedError("The upstream tool catalog was unavailable") from error

        return await tool.run(arguments)


async def close_toolhub_upstream(proxy: FastMCP) -> None:
    owner = getattr(proxy, "_toolhub_upstream_client", None)
    if owner is None:
        return
    try:
        await owner.__aexit__(None, None, None)
    finally:
        await owner.transport.close()


def _shared_upstream_process_inspector(owner: ToolHubProxyClient, server_names: tuple[str, ...]):
    def inspect() -> dict[str, int]:
        transport = owner.transport
        configured = tuple(getattr(getattr(transport, "config", None), "mcpServers", {}))
        underlying = tuple(getattr(transport, "_underlying_transports", ()))
        owner_state = getattr(owner, "_session_state", None)
        owner_count = getattr(owner_state, "nesting_counter", 0)
        topology_valid = (
            owner.is_connected()
            and owner_count == 1
            and configured == server_names
            and len(underlying) == len(server_names)
            and len({id(item) for item in underlying}) == len(server_names)
        )
        counts: dict[str, int] = {}
        for name, item in zip(server_names, underlying, strict=False):
            active = topology_valid
            task = getattr(item, "_connect_task", None)
            if task is not None:
                active = active and not task.done()
            counts[name] = 1 if active else 0
        if len(counts) != len(server_names):
            return {name: 0 for name in server_names}
        return counts

    return inspect


class MCPMProxyFactory:
    """Factory for creating FastMCP proxies with MCPM integration."""

    def __init__(
        self,
        auth_enabled: bool = False,
        api_key: Optional[str] = None,
        access_monitor: Optional[AccessMonitor] = None,
        tracking_enabled: bool = True,
        toolhub_routing: RoutingRuntime | None = None,
        toolhub_confirmations: ConfirmationStore | None = None,
        toolhub_observations: ObservationRing | None = None,
        toolhub_observation_limit: int = MAX_OBSERVATIONS,
    ):
        """
        Initialize the proxy factory.

        Args:
            auth_enabled: Whether authentication is enabled.
            api_key: The API key to use for authentication.
            access_monitor: Access monitor for tracking. If None, creates DuckDBMonitor.
            tracking_enabled: Whether to attach MCPM's payload-recording usage middleware.
            toolhub_routing: Optional ToolHub routing runtime for per-session governance.
            toolhub_confirmations: Optional shared ToolHub confirmation store.
            toolhub_observations: Optional shared payload-free observation ring.
            toolhub_observation_limit: Observation capacity when the factory creates the ring.
        """
        self.auth_enabled = auth_enabled
        self.api_key = api_key

        tracking_enabled = tracking_enabled and toolhub_routing is None
        if tracking_enabled and access_monitor is None:
            access_monitor = SQLiteAccessMonitor()
        self.access_monitor = access_monitor if tracking_enabled else None
        self.toolhub_routing = toolhub_routing
        self.toolhub_confirmations = (
            toolhub_confirmations
            if toolhub_confirmations is not None
            else ConfirmationStore()
            if toolhub_routing is not None
            else None
        )
        self.toolhub_observations = (
            toolhub_observations
            if toolhub_observations is not None
            else ObservationRing(limit=toolhub_observation_limit)
            if toolhub_routing is not None
            else None
        )

    async def create_proxy_for_servers(
        self,
        servers: List[ServerConfig],
        name: Optional[str] = None,
        stdio_mode: bool = True,
        action: str = "proxy",
        profile_name: Optional[str] = None,
    ) -> FastMCP:
        """
        Create a FastMCP proxy that aggregates multiple MCPM servers.

        Args:
            servers: List of ServerConfig objects to aggregate
            name: Optional name for the proxy
            stdio_mode: If True, skip auth middleware (for stdio operations)

        Returns:
            FastMCP proxy instance with MCPM middleware
        """
        if not servers:
            raise ValueError("At least one server must be provided")

        # Create FastMCP server configurations as plain dictionaries
        server_configs: Dict[str, StdioMCPServer | RemoteMCPServer] = {}

        for server in servers:
            # Handle different server transport types with proper type checking
            if isinstance(server, STDIOServerConfig):
                # STDIOServerConfig - command must be a list of strings
                # The command should be a list containing the command and its arguments
                command_parts = [server.command]
                if server.args:
                    command_parts.extend(server.args)

                # Always provide environment variables to trigger FastMCP's os.environ.copy()
                # This fixes a bug where stdio subprocesses get no PATH if env_vars is empty
                env_config = {"MCPM_STDIO_SERVER": "true"}
                if server.env:
                    # Ensure all environment values are strings (server.env is Dict[str, str])
                    env_config.update(server.env)

                server_configs[server.name] = StdioMCPServer(
                    command=server.command,
                    args=server.args or [],
                    env=env_config,
                )
            elif isinstance(server, RemoteServerConfig):
                # RemoteServerConfig - HTTP/SSE transport
                # Convert all header values to strings (FastMCP expects Dict[str, str])
                string_headers = {k: str(v) for k, v in server.headers.items()} if server.headers else {}
                server_configs[server.name] = RemoteMCPServer(
                    url=server.url,
                    headers=string_headers,
                )
            elif isinstance(server, CustomServerConfig):
                # CustomServerConfig is for non-standard client configs - skip it
                # These are client-specific configurations that don't go through the proxy
                continue
            else:
                # Unknown server type
                raise ValueError(f"Server {server.name} has unsupported configuration type: {type(server).__name__}")

        # Check if we have any servers to proxy after filtering
        if not server_configs:
            raise ValueError("No supported servers to proxy (all servers were skipped or unsupported)")

        # Create the proxy configuration dictionary directly
        proxy_config = MCPConfig(mcpServers=server_configs)

        if self.toolhub_routing is None:
            proxy = FastMCP.as_proxy(proxy_config, name=name or "mcpm-aggregated")
        else:
            upstream_client = ToolHubProxyClient(proxy_config)
            try:
                await upstream_client.__aenter__()
                proxy = FastMCP.as_proxy(upstream_client, name=name or "mcpm-aggregated")
            except BaseException:
                with suppress(BaseException):
                    await upstream_client.__aexit__(None, None, None)
                with suppress(BaseException):
                    await upstream_client.transport.close()
                raise
            setattr(proxy, "_toolhub_upstream_client", upstream_client)
            setattr(
                proxy,
                "_toolhub_upstream_process_counts",
                _shared_upstream_process_inspector(upstream_client, tuple(server_configs)),
            )
            proxy._tool_manager = ToolHubProxyToolManager(
                client_factory=proxy.client_factory,
                transformations=proxy._tool_manager.transformations,
            )

        # Add MCPM middleware
        # For single server proxies, use the server name for tracking
        server_name = servers[0].name if len(servers) == 1 else None
        self._add_mcpm_middleware(
            proxy, stdio_mode=stdio_mode, server_name=server_name, action=action, profile_name=profile_name
        )

        return proxy

    async def create_proxy_for_profile(
        self, profile_servers: List[ServerConfig], profile_name: str, stdio_mode: bool = True
    ) -> FastMCP:
        """
        Create a FastMCP proxy for a specific MCPM profile.

        Args:
            profile_servers: List of servers in the profile
            profile_name: Name of the profile
            stdio_mode: If True, skip auth middleware (for stdio operations)

        Returns:
            FastMCP proxy instance configured for the profile
        """
        return await self.create_proxy_for_servers(
            profile_servers, name=f"mcpm-profile-{profile_name}", stdio_mode=stdio_mode
        )

    def _add_mcpm_middleware(
        self,
        proxy: FastMCP,
        stdio_mode: bool = True,
        server_name: Optional[str] = None,
        action: str = "proxy",
        profile_name: Optional[str] = None,
    ) -> None:
        """Add MCPM-specific middleware to the proxy."""

        # Add unified tracking middleware (replaces both monitoring and usage tracking)
        if self.access_monitor:
            transport = SessionTransport.STDIO if stdio_mode else SessionTransport.HTTP
            unified_middleware = MCPMUnifiedTrackingMiddleware(
                access_monitor=self.access_monitor,
                server_name=server_name,
                action=action,
                profile_name=profile_name,
                transport=transport,
            )
            proxy.add_middleware(unified_middleware)

            # Store reference for cleanup
            setattr(proxy, "_mcpm_unified_middleware", unified_middleware)

        # Add authentication middleware (only for HTTP/network operations, not stdio)
        if self.auth_enabled and not stdio_mode and self.api_key:
            proxy.add_middleware(MCPMAuthMiddleware(self.api_key))

        if self.toolhub_routing is not None:
            install_toolhub_session_binding(proxy, self.toolhub_routing)
            proxy.add_middleware(
                ToolHubRoutingMiddleware(
                    self.toolhub_routing,
                    confirmations=self.toolhub_confirmations,
                    observations=self.toolhub_observations,
                )
            )
            setattr(proxy, "_toolhub_confirmations", self.toolhub_confirmations)
            setattr(proxy, "_toolhub_observations", self.toolhub_observations)


async def create_mcpm_proxy(
    servers: List[ServerConfig],
    name: Optional[str] = None,
    auth_enabled: bool = False,
    api_key: Optional[str] = None,
    access_monitor: Optional[AccessMonitor] = None,
    stdio_mode: bool = True,
    action: str = "proxy",
    profile_name: Optional[str] = None,
    tracking_enabled: bool = True,
    toolhub_routing: RoutingRuntime | None = None,
    toolhub_confirmations: ConfirmationStore | None = None,
    toolhub_observations: ObservationRing | None = None,
    toolhub_observation_limit: int = MAX_OBSERVATIONS,
) -> FastMCP:
    """
    Convenience function to create a FastMCP proxy with MCPM integration.

    Args:
        servers: List of ServerConfig objects to aggregate
        name: Optional name for the proxy
        auth_enabled: Whether authentication is enabled.
        api_key: The API key to use for authentication.
        access_monitor: Optional access monitor
        stdio_mode: If True, skip auth middleware (for stdio operations)
        tracking_enabled: Whether to attach MCPM's payload-recording usage middleware
        toolhub_routing: Optional ToolHub routing runtime for per-session governance
        toolhub_confirmations: Optional shared ToolHub confirmation store
        toolhub_observations: Optional shared payload-free observation ring
        toolhub_observation_limit: Observation capacity when the factory creates the ring

    Returns:
        Configured FastMCP proxy instance
    """
    factory = MCPMProxyFactory(
        auth_enabled=auth_enabled,
        api_key=api_key,
        access_monitor=access_monitor,
        tracking_enabled=tracking_enabled,
        toolhub_routing=toolhub_routing,
        toolhub_confirmations=toolhub_confirmations,
        toolhub_observations=toolhub_observations,
        toolhub_observation_limit=toolhub_observation_limit,
    )
    proxy = await factory.create_proxy_for_servers(
        servers, name, stdio_mode=stdio_mode, action=action, profile_name=profile_name
    )

    # Initialize the access monitor if provided
    if access_monitor:
        await access_monitor.initialize_storage()

    return proxy
