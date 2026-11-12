#!/usr/bin/env bash
# wrapper: run task probe runner against a scratch copy, keeping all temp files out of /tmp
set -euo pipefail
S=/root/workspace/volcano-workspace/.review-behavior-scratch
export TMPDIR=$S/tmp GOTMPDIR=$S/tmp
tree="$1"; shift
exec "$S/probes/run_probe.sh" "$S/$tree" "$@"
