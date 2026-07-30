#!/usr/bin/env sh
set -eu

base_url="${TOOLHUB_SMOKE_URL:-http://127.0.0.1:18480}"
username="${TOOLHUB_SMOKE_USERNAME:?TOOLHUB_SMOKE_USERNAME is required}"
password="${TOOLHUB_SMOKE_PASSWORD:?TOOLHUB_SMOKE_PASSWORD is required}"
smoke_dir="$(mktemp -d)"
trap 'rm -rf "$smoke_dir"' EXIT

login_status="$(curl --silent --show-error --output "$smoke_dir/login.json" --cookie-jar "$smoke_dir/cookies" --write-out '%{http_code}' -H 'Content-Type: application/json' --data "{\"username\":\"$username\",\"password\":\"$password\"}" "$base_url/api/v1/auth/login")"
[ "$login_status" = "200" ] || { printf 'login failed: HTTP %s\n' "$login_status"; exit 1; }

csrf="$(sed -n 's/.*"csrfToken":"\([^"]*\)".*/\1/p' "$smoke_dir/login.json")"
[ -n "$csrf" ] || { printf '%s\n' 'login response did not contain a CSRF token'; exit 1; }

overview_status="$(curl --silent --show-error --output /dev/null --cookie "$smoke_dir/cookies" --write-out '%{http_code}' "$base_url/api/v1/overview")"
[ "$overview_status" = "200" ] || { printf 'overview failed: HTTP %s\n' "$overview_status"; exit 1; }

targets_status="$(curl --silent --show-error --output "$smoke_dir/targets.json" --cookie "$smoke_dir/cookies" --write-out '%{http_code}' "$base_url/api/v1/targets")"
[ "$targets_status" = "200" ] || { printf 'targets failed: HTTP %s\n' "$targets_status"; exit 1; }
grep -q '"targetKey":"local/claude"' "$smoke_dir/targets.json" || { printf '%s\n' 'local Claude target is missing'; exit 1; }

csrf_status="$(curl --silent --show-error --output /dev/null --cookie "$smoke_dir/cookies" --write-out '%{http_code}' -H 'Content-Type: application/json' --data '{}' "$base_url/api/v1/updates/check")"
[ "$csrf_status" = "403" ] || { printf 'CSRF rejection failed: HTTP %s\n' "$csrf_status"; exit 1; }

logout_status="$(curl --silent --show-error --output /dev/null --cookie "$smoke_dir/cookies" --write-out '%{http_code}' -H "X-CSRF-Token: $csrf" -X POST "$base_url/api/v1/auth/logout")"
[ "$logout_status" = "204" ] || { printf 'logout failed: HTTP %s\n' "$logout_status"; exit 1; }

printf '%s\n' 'API smoke passed: username login, overview, local targets, CSRF rejection, logout'
