#!/usr/bin/env sh
set -eu

base_url="${TOOLHUB_SMOKE_URL:-http://127.0.0.1:18480}"
email="${TOOLHUB_SMOKE_EMAIL:?TOOLHUB_SMOKE_EMAIL is required}"
username="${TOOLHUB_SMOKE_USERNAME:-admin}"
password="${TOOLHUB_SMOKE_PASSWORD:?TOOLHUB_SMOKE_PASSWORD is required}"
smoke_dir="$(mktemp -d)"
trap 'rm -rf "$smoke_dir"' EXIT

login_status="$(curl --silent --show-error --output "$smoke_dir/login.json" --cookie-jar "$smoke_dir/cookies" --write-out '%{http_code}' -H 'Content-Type: application/json' --data "{\"identifier\":\"$email\",\"password\":\"$password\"}" "$base_url/api/v1/auth/login")"
[ "$login_status" = "200" ] || { printf 'login failed: HTTP %s\n' "$login_status"; exit 1; }

csrf="$(sed -n 's/.*"csrfToken":"\([^"]*\)".*/\1/p' "$smoke_dir/login.json")"
[ -n "$csrf" ] || { printf '%s\n' 'login response did not contain a CSRF token'; exit 1; }

overview_status="$(curl --silent --show-error --output /dev/null --cookie "$smoke_dir/cookies" --write-out '%{http_code}' "$base_url/api/v1/overview")"
[ "$overview_status" = "200" ] || { printf 'overview failed: HTTP %s\n' "$overview_status"; exit 1; }

nodes_status="$(curl --silent --show-error --output "$smoke_dir/nodes.json" --cookie "$smoke_dir/cookies" --write-out '%{http_code}' "$base_url/api/v1/nodes")"
[ "$nodes_status" = "200" ] || { printf 'nodes failed: HTTP %s\n' "$nodes_status"; exit 1; }
grep -q '"isLocal":true' "$smoke_dir/nodes.json" || { printf '%s\n' 'project-host node is missing'; exit 1; }

discoveries_status="$(curl --silent --show-error --output "$smoke_dir/discoveries.json" --cookie "$smoke_dir/cookies" --write-out '%{http_code}' "$base_url/api/v1/discoveries")"
[ "$discoveries_status" = "200" ] || { printf 'discoveries failed: HTTP %s\n' "$discoveries_status"; exit 1; }
grep -q '"items":' "$smoke_dir/discoveries.json" || { printf '%s\n' 'discoveries response is missing items'; exit 1; }

csrf_status="$(curl --silent --show-error --output /dev/null --cookie "$smoke_dir/cookies" --write-out '%{http_code}' -H 'Content-Type: application/json' --data '{}' "$base_url/api/v1/sync")"
[ "$csrf_status" = "403" ] || { printf 'CSRF rejection failed: HTTP %s\n' "$csrf_status"; exit 1; }

reconcile_status="$(curl --silent --show-error --output "$smoke_dir/reconcile.json" --cookie "$smoke_dir/cookies" --write-out '%{http_code}' -H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf" --data '{}' "$base_url/api/v1/reconcile")"
[ "$reconcile_status" = "202" ] || { printf 'reconcile failed: HTTP %s\n' "$reconcile_status"; exit 1; }
grep -q '"jobs":' "$smoke_dir/reconcile.json" || { printf '%s\n' 'reconcile response is missing jobs'; exit 1; }

logout_status="$(curl --silent --show-error --output /dev/null --cookie "$smoke_dir/cookies" --write-out '%{http_code}' -H "X-CSRF-Token: $csrf" -X POST "$base_url/api/v1/auth/logout")"
[ "$logout_status" = "204" ] || { printf 'logout failed: HTTP %s\n' "$logout_status"; exit 1; }

username_login_status="$(curl --silent --show-error --output "$smoke_dir/username-login.json" --cookie-jar "$smoke_dir/cookies" --write-out '%{http_code}' -H 'Content-Type: application/json' --data "{\"identifier\":\"$username\",\"password\":\"$password\"}" "$base_url/api/v1/auth/login")"
[ "$username_login_status" = "200" ] || { printf 'username login failed: HTTP %s\n' "$username_login_status"; exit 1; }

printf '%s\n' 'API smoke passed: email/username login, project host, overview, discoveries, dual reconcile, CSRF rejection, logout'
