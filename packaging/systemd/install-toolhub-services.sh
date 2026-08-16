#!/bin/sh
set -eu

usage='usage: install-toolhub-services.sh MANAGED_USER TOOLHUB_REPOSITORY [MANAGED_GROUP] [BRIDGE_GROUP]'
managed_user="${1:?$usage}"
toolhub_repository="${2:?$usage}"
managed_group="${3:-$managed_user}"
bridge_group="${4:-toolhub}"
source_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

[ "$(id -u)" -eq 0 ] || { printf '%s\n' 'installer must run as root' >&2; exit 1; }
getent passwd "$managed_user" >/dev/null || { printf 'managed user %s does not exist\n' "$managed_user" >&2; exit 1; }
getent group "$managed_group" >/dev/null || { printf 'managed group %s does not exist\n' "$managed_group" >&2; exit 1; }
if ! getent group "$bridge_group" >/dev/null; then
    groupadd --system "$bridge_group"
fi

canonical_repository() {
    repository="$1"
    case "$repository" in
        /*) ;;
        *) printf 'repository path must be absolute: %s\n' "$repository" >&2; exit 1 ;;
    esac
    [ -d "$repository" ] || { printf 'repository path must be a directory: %s\n' "$repository" >&2; exit 1; }
    canonical="$(readlink -f -- "$repository")"
    [ "$canonical" = "$repository" ] || { printf 'repository path must be canonical and must not be a symlink: %s\n' "$repository" >&2; exit 1; }
    [ "$(stat -c %F -- "$repository")" = 'directory' ] || { printf 'repository path must be a regular directory: %s\n' "$repository" >&2; exit 1; }
    [ "$(stat -c %u -- "$repository")" -eq 0 ] || { printf 'repository path must be root-owned: %s\n' "$repository" >&2; exit 1; }
    printf '%s\n' "$canonical"
}

toolhub_repository="$(canonical_repository "$toolhub_repository")"
mcpm_repository="$toolhub_repository/mcpm"
[ -d "$mcpm_repository" ] || { printf 'embedded mcpm project is missing: %s\n' "$mcpm_repository" >&2; exit 1; }
[ "$(readlink -f -- "$mcpm_repository")" = "$mcpm_repository" ] || { printf 'embedded mcpm project must not be a symlink: %s\n' "$mcpm_repository" >&2; exit 1; }
[ "$(stat -c %u -- "$mcpm_repository")" -eq 0 ] || { printf 'embedded mcpm project must be root-owned: %s\n' "$mcpm_repository" >&2; exit 1; }

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

bridge_binary="$toolhub_repository/bin/toolhub-bridge"
mcpm_launcher="$mcpm_repository/.venv/bin/mcpm"
mcpm_python="$mcpm_repository/.venv/bin/python"
if [ ! -x "$mcpm_python" ]; then
    mcpm_python="$mcpm_repository/.venv/bin/python3"
fi
for required in "$bridge_binary" "$mcpm_launcher"; do
    [ "$(stat -c %F -- "$required" 2>/dev/null || true)" = 'regular file' ] || { printf 'required path must be a non-symlink regular file: %s\n' "$required" >&2; exit 1; }
    [ "$(stat -c %u -- "$required")" -eq 0 ] || { printf 'required path must be root-owned: %s\n' "$required" >&2; exit 1; }
    [ -x "$required" ] || { printf 'required path must be executable: %s\n' "$required" >&2; exit 1; }
done

shebang="$(sed -n '1p' -- "$mcpm_launcher")"
[ "$shebang" = "#!$mcpm_python" ] || { printf 'mcpm launcher shebang must be %s\n' "#!$mcpm_python" >&2; exit 1; }
resolved_interpreter="$(readlink -f -- "$mcpm_python")"
case "$resolved_interpreter" in
    /root/.local/share/uv/python/*) ;;
    *) printf 'mcpm interpreter must resolve below /root/.local/share/uv/python: %s\n' "$resolved_interpreter" >&2; exit 1 ;;
esac
[ "$(stat -c %F -- "$resolved_interpreter")" = 'regular file' ] || { printf 'resolved mcpm interpreter must be a regular file\n' >&2; exit 1; }
[ "$(stat -c %u -- "$resolved_interpreter")" -eq 0 ] || { printf 'resolved mcpm interpreter must be root-owned\n' >&2; exit 1; }
[ -x "$resolved_interpreter" ] || { printf 'resolved mcpm interpreter must be executable\n' >&2; exit 1; }
[ -x /usr/bin/python3 ] && [ -x /usr/bin/timeout ] && [ -x /usr/sbin/runuser ] || { printf '%s\n' 'python3, timeout, and runuser are required to validate mcpm' >&2; exit 1; }

if ! /usr/bin/timeout 5s /usr/sbin/runuser -u "$managed_user" -- "$mcpm_launcher" toolhub contract --json \
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
    printf '%s\n' "$mcpm_launcher does not provide the required ToolHub routing/admin contract; no package was installed or updated" >&2
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

sed -e "s|@TOOLHUB_REPOSITORY@|$toolhub_repository|g" \
    -e "s|@MCPM_INTERPRETER@|$resolved_interpreter|g" \
    -e "s|@TOOLHUB_MANAGED_HOME@|$managed_home|g" \
    -e "s|@TOOLHUB_BRIDGE_GROUP@|$bridge_group|g" \
    "$source_dir/toolhub-bridge.service" > /etc/systemd/system/toolhub-bridge.service
sed -e "s|@TOOLHUB_REPOSITORY@|$toolhub_repository|g" \
    -e "s|@MCPM_INTERPRETER@|$resolved_interpreter|g" \
    -e "s|@TOOLHUB_MANAGED_USER@|$managed_user|g" \
    -e "s|@TOOLHUB_MANAGED_GROUP@|$managed_group|g" \
    -e "s|@TOOLHUB_MANAGED_HOME@|$managed_home|g" \
    "$source_dir/toolhub-mcpm-relay.service" > /etc/systemd/system/toolhub-mcpm-relay.service
chmod 0644 /etc/systemd/system/toolhub-bridge.service /etc/systemd/system/toolhub-mcpm-relay.service
systemctl daemon-reload
systemctl enable --now toolhub-bridge.service

bridge_gid="$(getent group "$bridge_group" | cut -d: -f3)"
printf 'Bridge installed. Set TOOLHUB_BRIDGE_GID=%s and TOOLHUB_MANAGED_USERNAME=%s in .env.\n' "$bridge_gid" "$managed_user"
printf '%s\n' 'Set TOOLHUB_BRIDGE_HMAC_KEY to the exact value in /etc/toolhub-bridge/hmac.key.'
