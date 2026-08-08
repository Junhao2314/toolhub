#!/usr/bin/env bash

set -u

port="${TOOLHUB_RELAY_PORT:-}"
case "$port" in
    ''|*[!0-9]*) exit 1 ;;
esac

# MCPM's HTTP mode auto-selects the next port when bind() fails. Wait for the
# fixed port to become bindable before starting it, so clients never observe a
# silently changed endpoint after a relay restart. Streamable HTTP connections
# can leave the old listener in TCP cleanup for about one minute.
for _ in $(/usr/bin/seq 1 1200); do
    if /usr/bin/python3 - "$port" <<'PY'
import socket
import sys

sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
try:
    sock.bind(("127.0.0.1", int(sys.argv[1])))
except OSError:
    raise SystemExit(1)
finally:
    sock.close()
PY
    then
        exit 0
    fi
    /usr/bin/sleep 0.1
done
exit 1
