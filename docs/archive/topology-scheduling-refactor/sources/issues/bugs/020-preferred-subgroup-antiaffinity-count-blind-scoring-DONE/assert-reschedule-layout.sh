#!/usr/bin/env bash

set -euo pipefail

context="${KUBE_CONTEXT:-kind-volcano-dev-020}"
task_dir="$(cd "$(dirname "$0")" && pwd)"

kubectl --context "${context}" apply -f "${task_dir}/reschedule-podgroup.yaml" >/dev/null
kubectl --context "${context}" wait --for='jsonpath={.status.phase}=Inqueue' podgroup/bug020-reschedule --timeout=120s >/dev/null
kubectl --context "${context}" apply -f "${task_dir}/reschedule-baseline.yaml" >/dev/null
kubectl --context "${context}" wait --for=condition=Ready pod -l issue=020,case=reschedule --timeout=180s >/dev/null

rows="$(kubectl --context "${context}" get pods -l issue=020,case=reschedule \
  -o custom-columns=NODE:.spec.nodeName --no-headers)"
counts="$(sort <<<"${rows}" | uniq -c | awk '{print $1}' | sort -n | paste -sd, -)"
if [[ "${counts}" != "1,1,2" ]]; then
  echo "baseline counts=${counts}, want 1,1,2" >&2
  exit 1
fi
crowded_node="$(sort <<<"${rows}" | uniq -c | awk '$1 == 2 {print $2}')"

for attempt in 1 2; do
  kubectl --context "${context}" apply -f "${task_dir}/reschedule-target.yaml" >/dev/null
  kubectl --context "${context}" wait --for=condition=Ready pod/bug020-reschedule-target --timeout=180s >/dev/null
  target_node="$(kubectl --context "${context}" get pod bug020-reschedule-target -o jsonpath='{.spec.nodeName}')"
  echo "reschedule attempt=${attempt} crowdedNode=${crowded_node} targetNode=${target_node}"
  if [[ "${target_node}" == "${crowded_node}" ]]; then
    echo "target selected the domain with two existing peers" >&2
    exit 1
  fi
  kubectl --context "${context}" delete pod bug020-reschedule-target --wait=true >/dev/null
done
