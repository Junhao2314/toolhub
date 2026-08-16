"""Profile run command."""

import asyncio
import logging
from pathlib import Path

from rich.console import Console
from rich.panel import Panel

from mcpm.fastmcp_integration.proxy import close_toolhub_upstream, create_mcpm_proxy

# Removed SessionAction import - using strings directly
from mcpm.profile.profile_config import ProfileConfigManager
from mcpm.toolhub.admin import AdminServer
from mcpm.toolhub.canary import ProxyCanaryUpstream, run_session_canary
from mcpm.toolhub.confirmation import ConfirmationStore
from mcpm.toolhub.observations import ObservationRing
from mcpm.toolhub.routing import RoutingRuntime
from mcpm.toolhub.schema import parse_routing_bundle_json
from mcpm.utils.config import DEFAULT_PORT
from mcpm.utils.logging_config import (
    ensure_dependency_logging_suppressed,
    get_uvicorn_log_level,
    setup_dependency_logging,
)
from mcpm.utils.rich_click_config import click

profile_config_manager = ProfileConfigManager()
logger = logging.getLogger(__name__)
console = Console()


async def observe_toolhub_contracts(proxy, routing: RoutingRuntime) -> dict[str, object]:
    """Read the upstream catalog without invoking tools or exposing proxy metadata."""
    tools = await proxy.get_tools()
    bundle = routing.current
    servers = bundle.servers
    grouped: dict[str, list[dict[str, object]]] = {server.serverName: [] for server in servers}

    for runtime_name, tool in sorted(tools.items()):
        server_name, tool_name = _split_runtime_tool_name(runtime_name, tuple(server.serverName for server in servers))
        annotations = tool.annotations.model_dump(by_alias=True, mode="json") if tool.annotations is not None else {}
        grouped[server_name].append(
            {
                "name": tool_name,
                "runtimeName": runtime_name,
                "title": tool.title or (tool.annotations.title if tool.annotations is not None else None),
                "description": tool.description,
                "inputSchema": tool.parameters,
                "outputSchema": tool.output_schema,
                "annotations": annotations,
            }
        )

    return {
        "relayConfigurationRevisionId": str(bundle.relayConfigurationRevisionId),
        "servers": [
            {
                "serverId": str(server.serverId),
                "serverName": server.serverName,
                "mcpConfigRevisionId": str(server.mcpConfigRevisionId),
                "tools": grouped[server.serverName],
            }
            for server in servers
        ],
    }


def _split_runtime_tool_name(runtime_name: str, server_names: tuple[str, ...]) -> tuple[str, str]:
    if len(server_names) == 1:
        return server_names[0], runtime_name

    matches = [server_name for server_name in server_names if runtime_name.startswith(f"{server_name}_")]
    if not matches:
        raise ValueError("Upstream tool name does not match a configured ToolHub server")
    server_name = max(matches, key=len)
    tool_name = runtime_name[len(server_name) + 1 :]
    if not tool_name:
        raise ValueError("Upstream tool name is empty")
    return server_name, tool_name


async def find_available_port(preferred_port, max_attempts=10):
    """Find an available port starting from preferred_port."""
    import socket

    for attempt in range(max_attempts):
        port_to_try = preferred_port + attempt

        # Check if port is available
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
                s.bind(("127.0.0.1", port_to_try))
                return port_to_try
        except OSError:
            continue  # Port is busy, try next one

    # If no port found, return the original (will likely fail but user will see the error)
    return preferred_port


async def run_profile_fastmcp(
    profile_servers,
    profile_name,
    http_mode=False,
    sse_mode=False,
    port=DEFAULT_PORT,
    host="127.0.0.1",
    toolhub_routing: Path | None = None,
    toolhub_admin_socket: Path | None = None,
    toolhub_observation_limit: int = 100_000,
):
    """Run profile servers using FastMCP proxy for proper aggregation."""
    if toolhub_routing is not None and (not http_mode or sse_mode):
        raise ValueError("ToolHub mode requires the fixed HTTP transport")
    if toolhub_routing is not None and host != "127.0.0.1":
        raise ValueError("ToolHub mode requires the 127.0.0.1 loopback host")

    server_count = len(profile_servers)
    logger.debug(f"Using FastMCP proxy to aggregate {server_count} server(s)")
    mode = "SSE" if sse_mode else "HTTP" if http_mode else "stdio"
    logger.debug(f"Mode: {mode}")

    proxy = None
    try:
        routing_runtime = None
        confirmations = None
        observations = None
        if toolhub_routing is not None:
            routing_runtime = RoutingRuntime(parse_routing_bundle_json(toolhub_routing.read_bytes()))
            confirmations = ConfirmationStore()
            observations = ObservationRing(limit=toolhub_observation_limit)
            setup_dependency_logging()

        # Create FastMCP proxy for profile servers
        if sse_mode:
            action = "profile_run_sse"
        elif http_mode:
            action = "profile_run_http"
        else:
            action = "profile_run"

        proxy = await create_mcpm_proxy(
            servers=profile_servers,
            name=f"profile-{profile_name}",
            stdio_mode=not (http_mode or sse_mode),  # stdio_mode=False for HTTP/SSE
            action=action,
            profile_name=profile_name,
            tracking_enabled=toolhub_routing is None,
            toolhub_routing=routing_runtime,
            toolhub_confirmations=confirmations,
            toolhub_observations=observations,
            toolhub_observation_limit=toolhub_observation_limit,
        )

        logger.debug(f"FastMCP proxy initialized with: {[s.name for s in profile_servers]}")

        # Set up dependency logging for FastMCP/MCP libraries
        setup_dependency_logging()

        # Re-suppress library logging after FastMCP initialization
        ensure_dependency_logging_suppressed()

        # Note: Usage tracking is handled by proxy middleware

        if http_mode or sse_mode:
            # Try to find an available port if the requested one is taken
            actual_port = await find_available_port(port)
            if actual_port != port:
                if routing_runtime is not None:
                    raise RuntimeError("The configured ToolHub relay port is unavailable")
                logger.debug(f"Port {port} is busy, using port {actual_port} instead")

            # Display profile information in a nice panel
            if sse_mode:
                server_url = f"http://{host}:{actual_port}/sse/"
                title = "📡 SSE Profile Running"
            else:
                server_url = f"http://{host}:{actual_port}/mcp/"
                title = "📁 Profile Running Locally"

            # Build server list
            server_list = "\n".join([f"  • [cyan]{server.name}[/]" for server in profile_servers])

            panel_content = f"[bold]Profile:[/] {profile_name}\n[bold]URL:[/] [cyan]{server_url}[/cyan]\n\n[bold]Servers:[/]\n{server_list}\n\n[dim]Press Ctrl+C to stop the profile[/]"

            panel = Panel(
                panel_content,
                title=title,
                title_align="left",
                border_style="green",
                padding=(1, 2),
            )
            console.print(panel)

            mode = "SSE" if sse_mode else "HTTP"
            logger.debug(f"Starting FastMCP proxy for profile '{profile_name}' in {mode} mode on {host}:{actual_port}")

            # Run the aggregated proxy over HTTP/SSE with uvicorn logging control
            transport = "sse" if sse_mode else "http"
            run_options = {
                "host": host,
                "port": actual_port,
                "transport": transport,
                "uvicorn_config": {"log_level": get_uvicorn_log_level()},
            }
            if routing_runtime is not None:
                if toolhub_admin_socket is None or confirmations is None or observations is None:
                    raise ValueError("ToolHub mode requires an admin socket and shared runtime stores")
                async with AdminServer(
                    toolhub_admin_socket,
                    toolhub_routing,
                    routing_runtime,
                    confirmations,
                    observations,
                    contract_observer=lambda: observe_toolhub_contracts(proxy, routing_runtime),
                    session_canary=lambda candidate: run_session_canary(candidate, ProxyCanaryUpstream(proxy)),
                ):
                    await proxy.run_http_async(**run_options)
            else:
                await proxy.run_http_async(**run_options)
        else:
            # Run the aggregated proxy over stdio (default)
            logger.info(f"Starting profile '{profile_name}' over stdio")
            await proxy.run_stdio_async(show_banner=False)

        return 0

    except KeyboardInterrupt:
        logger.info("Profile execution interrupted")
        if http_mode or sse_mode:
            logger.warning("\nProfile execution interrupted")
        return 130
    except Exception as error:
        if toolhub_routing is not None:
            logger.error("Error running ToolHub profile '%s': relay startup failed", profile_name)
        else:
            logger.error(f"Error running profile '{profile_name}': {error}")
        return 1
    finally:
        if proxy is not None:
            await close_toolhub_upstream(proxy)


@click.command()
@click.argument("profile_name")
@click.option("--http", is_flag=True, help="Run profile over HTTP instead of stdio")
@click.option("--sse", is_flag=True, help="Run profile over SSE instead of stdio")
@click.option("--port", type=int, default=DEFAULT_PORT, help=f"Port for HTTP / SSE mode (default: {DEFAULT_PORT})")
@click.option("--host", type=str, default="127.0.0.1", help="Host address for HTTP / SSE mode (default: 127.0.0.1)")
@click.option(
    "--toolhub-routing",
    type=click.Path(path_type=Path, dir_okay=False, resolve_path=True),
    default=None,
    help="Use the guarded ToolHub routing bundle at PATH.",
)
@click.option(
    "--toolhub-admin-socket",
    type=click.Path(path_type=Path, dir_okay=False, resolve_path=True),
    default=None,
    help="Expose the guarded ToolHub admin protocol at PATH.",
)
@click.option(
    "--toolhub-observation-limit",
    type=click.IntRange(min=1, max=100_000),
    default=100_000,
    show_default=True,
    help="Maximum payload-free observations retained in ToolHub mode.",
)
@click.help_option("-h", "--help")
def run(profile_name, http, sse, port, host, toolhub_routing, toolhub_admin_socket, toolhub_observation_limit):
    """Execute all servers in a profile over stdio, HTTP, or SSE.

    Uses FastMCP proxy to aggregate servers into a unified MCP interface
    with proper capability namespacing. By default runs over stdio.

    Examples:

    \b
        mcpm profile run web-dev                          # Run over stdio (default)
        mcpm profile run --http web-dev                   # Run over HTTP on 127.0.0.1:6276
        mcpm profile run --sse web-dev                    # Run over SSE on 127.0.0.1:6276
        mcpm profile run --http --port 9000 ai            # Run over HTTP on 127.0.0.1:9000
        mcpm profile run --sse --port 9000 ai             # Run over SSE on 127.0.0.1:9000
        mcpm profile run --http --host 0.0.0.0 web-dev    # Run over HTTP on 0.0.0.0:6276

    Debug logging: Set MCPM_DEBUG=1 for verbose output
    """
    # Validate profile name
    if not profile_name or not profile_name.strip():
        logger.error("Profile name cannot be empty")
        return 1

    profile_name = profile_name.strip()

    # Validate mutually exclusive options
    if http and sse:
        logger.error("Error: Cannot use both --http and --sse flags together")
        return 1

    if (toolhub_routing is None) != (toolhub_admin_socket is None):
        raise click.UsageError("--toolhub-routing and --toolhub-admin-socket must be provided together")
    if toolhub_routing is not None and not http:
        raise click.UsageError("ToolHub mode requires --http")
    if toolhub_routing is not None and host != "127.0.0.1":
        raise click.UsageError("ToolHub mode requires host 127.0.0.1")

    # Check if profile exists
    try:
        profile_servers = profile_config_manager.get_profile(profile_name)
        if profile_servers is None:
            logger.error(f"Profile '{profile_name}' not found")
            logger.info("Available options:")
            logger.info("  • Run 'mcpm profile ls' to see available profiles")
            logger.info("  • Run 'mcpm profile create {name}' to create a profile")
            return 1
    except Exception as e:
        logger.error(f"Error accessing profile '{profile_name}': {e}")
        return 1

    if not profile_servers:
        logger.warning(f"Profile '{profile_name}' has no servers configured")
        logger.info("Add servers to this profile with:")
        logger.info(f"  mcpm profile edit {profile_name}")
        return 0

    logger.info(f"Running profile '{profile_name}' with {len(profile_servers)} server(s)")

    # Log debug info about servers (controlled by MCPM_DEBUG environment variable)
    logger.debug("Servers to run:")
    for server_config in profile_servers:
        if toolhub_routing is not None:
            logger.debug("  - %s (%s)", server_config.name, type(server_config).__name__)
        else:
            logger.debug(f"  - {server_config.name}: {server_config}")

    # Use FastMCP proxy for all cases (single or multiple servers)
    logger.debug(f"Using FastMCP proxy for {len(profile_servers)} server(s)")
    mode = "SSE" if sse else "HTTP" if http else "stdio"
    logger.debug(f"Mode: {mode}")
    if http or sse:
        logger.debug(f"Port: {port}")

    # Run the async function
    return asyncio.run(
        run_profile_fastmcp(
            profile_servers,
            profile_name,
            http_mode=http,
            sse_mode=sse,
            port=port,
            host=host,
            toolhub_routing=toolhub_routing,
            toolhub_admin_socket=toolhub_admin_socket,
            toolhub_observation_limit=toolhub_observation_limit,
        )
    )
