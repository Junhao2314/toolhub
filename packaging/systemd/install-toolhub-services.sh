#!/bin/sh
set -eu

managed_user="${1:?usage: install-toolhub-services.sh MANAGED_USER [MANAGED_GROUP] [BRIDGE_GROUP]}"
managed_group="${2:-$managed_user}"
bridge_group="${3:-toolhub}"
source_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

[ "$(id -u)" -eq 0 ] || { printf '%s\n' 'installer must run as root' >&2; exit 1; }
getent passwd "$managed_user" >/dev/null || { printf 'managed user %s does not exist\n' "$managed_user" >&2; exit 1; }
getent group "$managed_group" >/dev/null || { printf 'managed group %s does not exist\n' "$managed_group" >&2; exit 1; }
if ! getent group "$bridge_group" >/dev/null; then
    groupadd --system "$bridge_group"
fi
managed_home="$(getent passwd "$managed_user" | cut -d: -f6)"
case "$managed_home" in
    /*) ;;
    *) printf '%s\n' 'managed user home must be absolute' >&2; exit 1 ;;
esac
[ "$managed_home" != "/" ] || { printf '%s\n' 'managed user home cannot be /' >&2; exit 1; }
[ -d "$managed_home" ] || { printf '%s\n' 'managed user home must be an existing directory' >&2; exit 1; }
canonical_home="$(readlink -f -- "$managed_home")"
[ -n "$canonical_home" ] && [ "$canonical_home" = "$managed_home" ] || { printf '%s\n' 'managed user home must be canonical and must not be a symlink' >&2; exit 1; }
managed_home="$canonical_home"
[ -x /usr/local/sbin/toolhub-bridge ] || { printf '%s\n' '/usr/local/sbin/toolhub-bridge is missing' >&2; exit 1; }
[ -x /usr/bin/mcpm ] || { printf '%s\n' '/usr/bin/mcpm is missing; install a compatible ToolHub mcpm build before running this installer' >&2; exit 1; }
[ -x /usr/bin/python3 ] && [ -x /usr/bin/timeout ] && [ -x /usr/sbin/runuser ] || { printf '%s\n' 'python3, timeout, and runuser are required to validate mcpm' >&2; exit 1; }

if ! /usr/bin/timeout 5s /usr/sbin/runuser -u "$managed_user" -- /usr/bin/mcpm toolhub contract --json \
    | /usr/bin/python3 -c '
import json
import sys

required_features = {
    "profile-session-binding",
    "tool-filtering",
    "call-policy",
    "one-shot-confirmation",
    "payload-free-observations",
    "routing-hot-reload",
}
expected_fields = {
    "adminProtocolVersion",
    "features",
    "routingSchemaVersions",
    "runtime",
    "runtimeVersion",
}
try:
    contract = json.load(sys.stdin)
    valid = (
        isinstance(contract, dict)
        and set(contract) == expected_fields
        and contract.get("adminProtocolVersion") == 1
        and contract.get("runtime") == "mcpm"
        and isinstance(contract.get("runtimeVersion"), str)
        and 0 < len(contract["runtimeVersion"]) <= 64
        and isinstance(contract.get("features"), list)
        and all(isinstance(value, str) for value in contract["features"])
        and required_features.issubset(set(contract["features"]))
        and isinstance(contract.get("routingSchemaVersions"), list)
        and 1 in contract["routingSchemaVersions"]
    )
except (AttributeError, KeyError, TypeError, ValueError, json.JSONDecodeError):
    valid = False
raise SystemExit(0 if valid else 1)
'; then
    printf '%s\n' '/usr/bin/mcpm does not provide the required ToolHub routing/admin contract; no package was installed or updated' >&2
    exit 1
fi

install -d -m 0700 /etc/toolhub-bridge /var/lib/toolhub-bridge
if [ ! -f /etc/toolhub-bridge/hmac.key ]; then
    umask 077
    openssl rand -hex 32 > /etc/toolhub-bridge/hmac.key
fi
chown root:root /etc/toolhub-bridge/hmac.key
chmod 0600 /etc/toolhub-bridge/hmac.key
if [ ! -f /var/lib/toolhub-bridge/mcpm-relay.env ]; then
    printf '%s\n' 'TOOLHUB_RELAY_PORT=6276' > /var/lib/toolhub-bridge/mcpm-relay.env
fi
chown root:root /var/lib/toolhub-bridge/mcpm-relay.env
chmod 0600 /var/lib/toolhub-bridge/mcpm-relay.env

install -m 0755 "$source_dir/toolhub-relay-port-check.sh" /usr/local/sbin/toolhub-relay-port-check

sed -e "s|@TOOLHUB_MANAGED_HOME@|$managed_home|g" \
    -e "s|@TOOLHUB_BRIDGE_GROUP@|$bridge_group|g" \
    "$source_dir/toolhub-bridge.service" > /etc/systemd/system/toolhub-bridge.service
sed -e "s|@TOOLHUB_MANAGED_USER@|$managed_user|g" \
    -e "s|@TOOLHUB_MANAGED_GROUP@|$managed_group|g" \
    -e "s|@TOOLHUB_MANAGED_HOME@|$managed_home|g" \
    "$source_dir/toolhub-mcpm-relay.service" > /etc/systemd/system/toolhub-mcpm-relay.service
chmod 0644 /etc/systemd/system/toolhub-bridge.service /etc/systemd/system/toolhub-mcpm-relay.service
systemctl daemon-reload
systemctl enable --now toolhub-bridge.service

bridge_gid="$(getent group "$bridge_group" | cut -d: -f3)"
printf 'Bridge installed. Set TOOLHUB_BRIDGE_GID=%s and TOOLHUB_MANAGED_USERNAME=%s in .env.\n' "$bridge_gid" "$managed_user"
printf '%s\n' 'Set TOOLHUB_BRIDGE_HMAC_KEY to the exact value in /etc/toolhub-bridge/hmac.key.'
