#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <repetitions> <baseline|fixed>" >&2
  exit 2
fi

repetitions="$1"
mode="$2"
context="${KUBE_CONTEXT:-kind-volcano-dev-020}"
task_dir="$(cd "$(dirname "$0")" && pwd)"
failures=0

for run in $(seq 1 "${repetitions}"); do
  kubectl --context "${context}" apply -f "${task_dir}/workloads.yaml" >/dev/null
  kubectl --context "${context}" wait --for=condition=Ready pod -l issue=020,case=layout --timeout=180s >/dev/null

  rows="$(kubectl --context "${context}" get pods -l issue=020,case=layout \
    -o custom-columns=NODE:.spec.nodeName,ROLE:.metadata.labels.role --no-headers)"
  layout="$(awk '
    { nodes[$1]=1; if ($2 == "decode") decode[$1]++; else if ($2 == "prefill") prefill[$1]++ }
    END { for (node in nodes) printf "%s:d=%d,p=%d,gpu=%d ", node, decode[node], prefill[node], 2*decode[node]+prefill[node] }
  ' <<<"${rows}")"
  decode_counts="$(awk '$2 == "decode" { count[$1]++ } END { for (node in count) print count[node] }' <<<"${rows}" | sort -n | paste -sd, -)"

  ideal=true
  if [[ "${decode_counts}" != "2,3,3" ]]; then
    ideal=false
    failures=$((failures + 1))
  fi
  echo "mode=${mode} run=${run} ideal=${ideal} decode=${decode_counts} ${layout}"

  kubectl --context "${context}" delete pod -l issue=020,case=layout --wait=true >/dev/null
done

echo "mode=${mode} repetitions=${repetitions} nonIdeal=${failures}"
if [[ "${mode}" == "fixed" && "${failures}" -ne 0 ]]; then
  exit 1
fi
