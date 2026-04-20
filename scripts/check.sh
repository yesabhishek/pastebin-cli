#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$ROOT"

require_tool() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required tool: $1" >&2
    exit 1
  fi
}

require_tool git
require_tool go
require_tool actionlint
require_tool shellcheck

go_files=()
while IFS= read -r file; do
  go_files+=("$file")
done < <(git ls-files '*.go')
if ((${#go_files[@]} > 0)); then
  unformatted="$(gofmt -l "${go_files[@]}")"
  if [[ -n "$unformatted" ]]; then
    echo "gofmt needs to be run on:" >&2
    printf '%s\n' "$unformatted" >&2
    exit 1
  fi
fi

go vet ./...
go test ./...
actionlint
shellcheck scripts/install.sh scripts/check.sh .githooks/pre-commit
