#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

run_go_formatters() {
  (
    cd "$repo_root"
    # Keep this script formatter-only; lint/autofix checks run in check-code-health.sh.
    golangci-lint fmt
  )
}

run_go_formatters
