"""Machine-readable capability contract for ToolHub integration."""

import json

from mcpm import __version__
from mcpm.utils.rich_click_config import click

TOOLHUB_FEATURES = (
    "profile-session-binding",
    "tool-filtering",
    "call-policy",
    "one-shot-confirmation",
    "payload-free-observations",
    "routing-hot-reload",
    "session-canary",
)


def capability_contract() -> dict[str, object]:
    return {
        "adminProtocolVersion": 1,
        "features": list(TOOLHUB_FEATURES),
        "routingSchemaVersions": [1],
        "runtime": "mcpm",
        "runtimeVersion": f"{__version__}-toolhub.1",
    }


@click.group()
@click.help_option("-h", "--help")
def toolhub():
    """Expose the guarded ToolHub relay integration contract."""


@toolhub.command(name="contract")
@click.option("--json", "json_output", is_flag=True, help="Write the capability contract as JSON.")
@click.help_option("-h", "--help")
def contract_command(json_output: bool):
    """Report ToolHub relay capabilities."""
    if not json_output:
        raise click.UsageError("--json is required")

    click.echo(json.dumps(capability_contract(), separators=(",", ":")))
