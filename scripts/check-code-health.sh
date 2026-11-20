#!/usr/bin/env bash

set -euo pipefail

# AGENT NOTE: This script is intentionally authoritative and broad. Never add
# partial modes, skip checks, or reduce its reporting scope to make local runs
# easier or faster. Any branching in this file is explicit repo-level policy,
# not a precedent for adding more fast paths. If you need a narrower local
# check, run the underlying tools directly instead of changing this file.

# The job scheduler uses associative arrays and `wait -n -p` (Bash 5.1).
if ((BASH_VERSINFO[0] < 5 || (BASH_VERSINFO[0] == 5 && BASH_VERSINFO[1] < 1))); then
  echo "health: requires Bash 5.1 or later, found $BASH_VERSION; on macOS run: brew install bash" >&2
  exit 2
fi

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# Go keys cached test results on every variable a test reads, including PATH
# for tool lookups such as codex. Agent sessions add private tool directories
# to PATH, and Codex adds a new one per session, so keep only shared, unique
# entries.
codex_home="${CODEX_HOME:-$HOME/.codex}"
path=""
IFS=: read -ra path_entries <<< "${PATH:-}"
for entry in "${path_entries[@]}"; do
  case "$entry" in "" | "$codex_home"/*) continue ;; esac
  case ":$path:" in *":$entry:"*) continue ;; esac
  path="${path:+$path:}$entry"
done
export PATH="$path"
# Without -trimpath, Go cache keys include the checkout path, so worktrees and
# CI checkouts could not reuse each other's build and test results.
export GOFLAGS="-trimpath"

# Checks run in parallel, at most one per CPU. Each output line starts with
# its check name. A failed check fails health after the running checks finish.
max_jobs="$(getconf _NPROCESSORS_ONLN)"
declare -A running=()
failed=()
reap() {
  local pid status=0
  wait -n -p pid || status=$?
  if ((status != 0)); then failed+=("${running[$pid]}"); fi
  unset "running[$pid]"
}
job() {
  local name="$1"
  shift
  while ((${#running[@]} >= max_jobs)); do reap; done
  ("$@") 2>&1 | sed -u "s|^|[$name] |" &
  running[$!]="$name"
}
# low COMMAND...: run a CPU-heavy check at low priority, so other work on the
# host keeps its CPUs.
low() {
  renice -n 10 -p "$BASHPID" > /dev/null
  "$@"
}
finish() {
  while ((${#running[@]} > 0)); do reap; done
  if ((${#failed[@]} > 0)); then
    echo "health: failed: ${failed[*]}" >&2
    exit 1
  fi
}

# The checks load the same compiled packages; compile them once here, or each
# would compile them in parallel.
go list -e -export -compiled -deps -test ./... > /dev/null
job go-test low go test -vet=off ./...
job slopelint low go run ./cmd/slopelint ./...
job golangci-lint low golangci-lint run --allow-parallel-runners ./...
job go-vet low go vet ./...
finish
