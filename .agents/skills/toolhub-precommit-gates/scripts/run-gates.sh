#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  printf '%s\n' 'run-gates.sh must be run inside the ToolHub git repository' >&2
  exit 2
}
cd "$repo_root"

gocache="${GOCACHE:-/tmp/toolhub-gocache}"
race_mode="${TOOLHUB_GATES_RACE:-auto}"
e2e_url="${TOOLHUB_E2E_URL:-http://127.0.0.1:18480}"
smoke_url="${TOOLHUB_SMOKE_URL:-$e2e_url}"
smoke_username="${TOOLHUB_SMOKE_USERNAME:-${TOOLHUB_E2E_USERNAME:-}}"
smoke_password="${TOOLHUB_SMOKE_PASSWORD:-${TOOLHUB_E2E_PASSWORD:-}}"

declare -a failures=()
declare -a changed_paths=()

run_gate() {
  local name="$1"
  shift
  printf '\n==> %s\n' "$name"
  if "$@"; then
    printf 'PASS: %s\n' "$name"
  else
    printf 'FAIL: %s\n' "$name" >&2
    failures+=("$name")
  fi
}

run_shell_gate() {
  local name="$1"
  local command="$2"
  run_gate "$name" bash -o pipefail -c "$command"
}

collect_changed_paths() {
  mapfile -t changed_paths < <(
    {
      git diff --name-only HEAD
      git ls-files --others --exclude-standard
    } | sed '/^$/d' | sort -u
  )
}

print_changed_paths() {
  printf 'Changed paths considered by the gates:\n'
  if ((${#changed_paths[@]} == 0)); then
    printf '  (working tree is clean; gates still run against HEAD)\n'
    return
  fi
  printf '  %s\n' "${changed_paths[@]}"
}

check_commands() {
  local command
  for command in git go python3 npm curl make docker rg; do
    command -v "$command" >/dev/null 2>&1 || {
      printf 'required command is missing: %s\n' "$command" >&2
      return 1
    }
  done
}

check_required_environment() {
  local missing=()
  [[ -n "${TOOLHUB_TEST_DATABASE_URL:-}" ]] || missing+=(TOOLHUB_TEST_DATABASE_URL)
  [[ -n "${TOOLHUB_E2E_USERNAME:-}" ]] || missing+=(TOOLHUB_E2E_USERNAME)
  [[ -n "${TOOLHUB_E2E_PASSWORD:-}" ]] || missing+=(TOOLHUB_E2E_PASSWORD)
  if ((${#missing[@]} != 0)); then
    printf 'missing required environment: %s\n' "${missing[*]}" >&2
    printf '%s\n' 'Integration and browser tests are required; configure disposable local/CI values instead of allowing skips.' >&2
    return 1
  fi
  if [[ "$race_mode" != auto && "$race_mode" != always && "$race_mode" != never ]]; then
    printf 'TOOLHUB_GATES_RACE must be auto, always, or never (got %s)\n' "$race_mode" >&2
    return 1
  fi
}

check_diff_hygiene() {
  local path
  local bad_paths=()
  for path in "${changed_paths[@]}"; do
    case "$path" in
      .env|.env.*|cmd/toolhub/dist/assets/*|web/dist/*|bin/*|coverage/*|playwright-report/*|test-results/*|*.log|*.tmp|*.db|*.sqlite|*.sqlite3)
        [[ "$path" == ".env.example" ]] || bad_paths+=("$path")
        ;;
    esac
  done

  local plan_count=0
  if [[ -d plans ]]; then
    plan_count="$(find plans -type f -print | wc -l | tr -d ' ')"
  fi
  if [[ "$plan_count" -gt 0 ]]; then
    printf '%s\n' 'plans/ contains completed/ignored plan files; remove them before committing.' >&2
    while IFS= read -r path; do
      printf '  %s\n' "$path" >&2
    done < <(find plans -type f -print | sort)
    return 1
  fi

  if ((${#bad_paths[@]} != 0)); then
    printf '%s\n' 'generated, runtime, or environment files are in the diff:' >&2
    printf '  %s\n' "${bad_paths[@]}" >&2
    return 1
  fi
}

check_added_secrets() {
  local diff secret_lines path
  diff="$(git diff --no-ext-diff --text --unified=0 HEAD -- || true)"

  secret_lines="$(printf '%s\n' "$diff" | rg '^\+[^+].*(BEGIN (RSA|OPENSSH|EC|DSA) PRIVATE KEY|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9]{20,})' || true)"
  if [[ -n "$secret_lines" ]]; then
    printf '%s\n' 'possible private key or provider token found in added lines' >&2
    return 1
  fi

  secret_lines="$(printf '%s\n' "$diff" | rg '^\+[^+].*TOOLHUB_(MASTER_KEY|BRIDGE_HMAC_KEY)\s*[:=]\s*[A-Za-z0-9+/=_-]{32,}' | rg -v '0123456789abcdef|test-only|ToolHubLocal-|example|placeholder' || true)"
  if [[ -n "$secret_lines" ]]; then
    printf '%s\n' 'possible ToolHub master/HMAC key found in added lines' >&2
    return 1
  fi

  while IFS= read -r path; do
    [[ -f "$path" ]] || continue
    git ls-files --error-unmatch -- "$path" >/dev/null 2>&1 && continue
    secret_lines="$(rg 'BEGIN (RSA|OPENSSH|EC|DSA) PRIVATE KEY|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9]{20,}' "$path" || true)"
    if [[ -n "$secret_lines" ]]; then
      printf 'possible provider secret found in untracked file: %s\n' "$path" >&2
      return 1
    fi
  done < <(printf '%s\n' "${changed_paths[@]}")
}

high_risk_diff() {
  local path
  for path in "${changed_paths[@]}"; do
    case "$path" in
      internal/security/*|internal/store/*|internal/bridge/*|internal/bridgeprotocol/*|internal/bridgeclient/*|internal/runtime/*|internal/saltdriver/*|internal/worker/*|internal/configmigration/*|cmd/toolhub/*|cmd/toolhub-bridge/*|internal/httpapi/*|api/*)
        return 0
        ;;
    esac
  done
  return 1
}

check_openapi_yaml() {
  python3 - <<'PY'
import pathlib
import sys

import yaml

for name in ("api/openapi.yaml", "api/bridge-openapi.yaml"):
    path = pathlib.Path(name)
    if not path.is_file():
        raise SystemExit(f"missing OpenAPI file: {name}")
    with path.open(encoding="utf-8") as handle:
        document = yaml.safe_load(handle)
    if not isinstance(document, dict) or not document.get("openapi"):
        raise SystemExit(f"invalid OpenAPI document: {name}")
    print(f"parsed {name}")
PY
}

collect_changed_paths
print_changed_paths
run_gate 'tool availability' check_commands
run_gate 'required integration/E2E environment' check_required_environment
run_gate 'diff, generated-file, plan hygiene' check_diff_hygiene
run_gate 'added-line secret scan' check_added_secrets
run_shell_gate 'whitespace' 'git diff --check HEAD'

run_shell_gate 'Go tests (PostgreSQL integration enabled)' "GOCACHE=\"$gocache\" go test -count=1 ./..."
run_shell_gate 'Go vet' "GOCACHE=\"$gocache\" go vet ./..."

build_dir="$(mktemp -d "${TMPDIR:-/tmp}/toolhub-gates-build.XXXXXX")"
trap 'rm -rf "$build_dir"' EXIT
run_shell_gate 'Go command builds' "GOCACHE=\"$gocache\" go build -o \"$build_dir/toolhub\" ./cmd/toolhub && GOCACHE=\"$gocache\" go build -o \"$build_dir/toolhub-bridge\" ./cmd/toolhub-bridge && GOCACHE=\"$gocache\" go build -o \"$build_dir/toolhub-config-migrate\" ./cmd/toolhub-config-migrate"

if [[ "$race_mode" == always ]] || { [[ "$race_mode" == auto ]] && high_risk_diff; }; then
  run_shell_gate 'Go race tests (high-risk/shared state)' "GOCACHE=\"$gocache\" go test -race -count=1 ./..."
elif [[ "$race_mode" == never ]] && high_risk_diff; then
  run_gate 'Go race tests are mandatory for this high-risk diff' false
else
  printf '\nPASS: Go race tests (not required for this low-risk diff; use TOOLHUB_GATES_RACE=always to force)\n'
fi

run_shell_gate 'Salt Python tests' "PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s packaging/salt/tests -p '*_test.py'"
run_gate 'OpenAPI YAML parse' check_openapi_yaml
run_shell_gate 'Compose configuration' 'make docker-config'

if [[ ! -d web/node_modules ]] || git diff --name-only HEAD | rg -q '^web/package(-lock)?\.json$'; then
  run_shell_gate 'Web dependency lock install' 'cd web && npm ci --ignore-scripts'
else
  printf '\nPASS: Web dependency lock install (node_modules present and lockfile unchanged)\n'
fi
run_shell_gate 'Web audit' 'cd web && npm audit --audit-level=high'
run_shell_gate 'Web typecheck' 'cd web && npm run typecheck'
run_shell_gate 'Web production build' 'cd web && npm run build'

run_gate 'live backend health' bash -c 'curl --fail --silent --show-error --max-time 10 "$1/healthz" >/dev/null' _ "$e2e_url"
run_gate 'API smoke' env TOOLHUB_SMOKE_URL="$smoke_url" TOOLHUB_SMOKE_USERNAME="$smoke_username" TOOLHUB_SMOKE_PASSWORD="$smoke_password" sh scripts/smoke-api.sh
run_gate 'desktop/mobile Playwright E2E' env TOOLHUB_E2E_URL="$e2e_url" TOOLHUB_E2E_USERNAME="${TOOLHUB_E2E_USERNAME:-}" TOOLHUB_E2E_PASSWORD="${TOOLHUB_E2E_PASSWORD:-}" bash -c 'cd web && npm run test:e2e'

printf '\n'
if ((${#failures[@]} != 0)); then
  printf 'GATES FAILED (%d):\n' "${#failures[@]}" >&2
  printf '  %s\n' "${failures[@]}" >&2
  printf '%s\n' 'Do not commit or push until every failed gate is fixed and the full script passes.' >&2
  exit 1
fi

printf '%s\n' 'ALL TOOLHUB PRE-COMMIT GATES PASSED'
