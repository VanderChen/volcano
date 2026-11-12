#!/usr/bin/env bash
# Usage: run_probe.sh <worktree> <variant-dirs-under-probes, comma separated> [go test args...]
# Copies the probe test files of issue 004 into a throwaway Volcano worktree,
# runs them, and removes the copies again.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
worktree="$1"; variant="$2"; shift 2
dest="$worktree/pkg/scheduler/actions/allocate"
copied=()
cleanup() { for f in "${copied[@]}"; do rm -f "$f"; done; }
trap cleanup EXIT
IFS=',' read -r -a variants <<< "$variant"
for name in common "${variants[@]}"; do
  dir="$here/$name"
  [ -d "$dir" ] || continue
  for f in "$dir"/*_test.go; do
    [ -e "$f" ] || continue
    cp "$f" "$dest/"; copied+=("$dest/$(basename "$f")")
  done
done
cd "$worktree"
go test ./pkg/scheduler/actions/allocate/ -count=1 "$@"
