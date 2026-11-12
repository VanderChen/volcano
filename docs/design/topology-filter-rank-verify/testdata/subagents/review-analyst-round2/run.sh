#!/usr/bin/env bash
# Usage: run.sh <comma-separated task probe dirs> <comma-separated own probe dirs> [go test args...]
# Runs probe tests against /tmp/vc-probe-master2 WITHOUT writing into it: the
# test files are injected with `go test -overlay`.
set -euo pipefail
S="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TASK=/root/workspace/volcano-workspace/issues/features/004-topology-scheduling-refactor-reexamination-IP/probes
WT=/tmp/vc-probe-master2
DEST=$WT/pkg/scheduler/actions/allocate
taskdirs="$1"; owndirs="$2"; shift 2
mkdir -p "$S/gotmp" "$S/tmp"
json="$S/overlay.$$.json"; trap 'rm -rf "$json" "$S/gotmp" "$S/tmp"' EXIT
{
  printf '{"Replace":{'
  sep=""
  IFS=',' read -r -a tds <<< "$taskdirs"
  for d in common "${tds[@]}"; do
    [ -d "$TASK/$d" ] || continue
    for f in "$TASK/$d"/*_test.go; do
      [ -e "$f" ] || continue
      printf '%s"%s/%s":"%s"' "$sep" "$DEST" "$(basename "$f")" "$f"; sep=","
    done
  done
  IFS=',' read -r -a ods <<< "$owndirs"
  for d in "${ods[@]}"; do
    [ -n "$d" ] && [ -d "$S/probes/$d" ] || continue
    for f in "$S/probes/$d"/*_test.go; do
      [ -e "$f" ] || continue
      printf '%s"%s/zz_review_analyst_%s":"%s"' "$sep" "$DEST" "$(basename "$f")" "$f"; sep=","
    done
  done
  printf '}}\n'
} > "$json"
cd "$WT"
GOTMPDIR="$S/gotmp" TMPDIR="$S/tmp" go test -overlay="$json" ./pkg/scheduler/actions/allocate/ -count=1 "$@"
