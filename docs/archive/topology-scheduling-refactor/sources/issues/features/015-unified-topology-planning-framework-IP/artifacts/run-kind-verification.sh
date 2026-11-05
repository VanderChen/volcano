#!/usr/bin/env bash

set -euo pipefail

context="${KUBE_CONTEXT:-kind-volcano-dev-015-nolegacy}"
artifact_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
worker1="volcano-dev-015-nolegacy-worker"
worker2="volcano-dev-015-nolegacy-worker2"
worker3="volcano-dev-015-nolegacy-worker3"

k() {
  kubectl --context "${context}" "$@"
}

reset_scenario() {
  k delete namespace feature-015 --ignore-not-found --wait=true
}

count_node() {
  local placements="$1"
  local node="$2"
  awk -v node="${node}" '$0 == node { count++ } END { print count + 0 }' <<<"${placements}"
}

k label node "${worker1}" feature-015.volcano.sh/target=true feature-015.volcano.sh/fit-first=primary --overwrite
k label node "${worker2}" feature-015.volcano.sh/target=true feature-015.volcano.sh/fit-first=secondary --overwrite
k label node "${worker3}" feature-015.volcano.sh/target=true --overwrite

for iteration in $(seq 1 20); do
  reset_scenario
  k apply -f "${artifact_dir}/k1-podgroups.yaml"
  k apply -f "${artifact_dir}/k1-occupied-deployment.yaml"
  k rollout status -n feature-015 deployment/occupied-a --timeout=90s
  k apply -f "${artifact_dir}/k1-target-deployment.yaml"
  k rollout status -n feature-015 deployment/exact-bc --timeout=180s

  placements="$(k get pods -n feature-015 -l app=exact-bc \
    -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}')"
  worker1_count="$(count_node "${placements}" "${worker1}")"
  worker2_count="$(count_node "${placements}" "${worker2}")"
  worker3_count="$(count_node "${placements}" "${worker3}")"
  if [[ "${worker1_count}" -ne 0 || "${worker2_count}" -ne 2 || "${worker3_count}" -ne 2 ]]; then
    printf 'K1 iteration %02d failed: worker1=%s worker2=%s worker3=%s\n' \
      "${iteration}" "${worker1_count}" "${worker2_count}" "${worker3_count}" >&2
    exit 1
  fi
  printf 'K1 iteration %02d passed: worker1=0 worker2=2 worker3=2\n' "${iteration}"
done

reset_scenario
k create namespace feature-015
k apply -f "${artifact_dir}/k2-podgroup.yaml"
k apply -f "${artifact_dir}/k2-deployments.yaml"
k rollout status -n feature-015 deployment/subgroup-prefill --timeout=180s
k rollout status -n feature-015 deployment/subgroup-decode --timeout=180s
k2_prefill="$(k get pods -n feature-015 -l app=subgroup-prefill -o jsonpath='{.items[0].spec.nodeName}')"
k2_decode="$(k get pods -n feature-015 -l app=subgroup-decode -o jsonpath='{.items[0].spec.nodeName}')"
if [[ -z "${k2_prefill}" || "${k2_prefill}" != "${k2_decode}" ]]; then
  printf 'K2 failed: prefill=%s decode=%s\n' "${k2_prefill}" "${k2_decode}" >&2
  exit 1
fi
printf 'K2 passed: prefill=%s decode=%s\n' "${k2_prefill}" "${k2_decode}"

reset_scenario
k create namespace feature-015
k apply -f "${artifact_dir}/k3-podgroup.yaml"
k apply -f "${artifact_dir}/k3-deployments.yaml"
k rollout status -n feature-015 deployment/fit-first-task-a --timeout=180s
k rollout status -n feature-015 deployment/fit-first-task-b --timeout=180s
k3_a="$(k get pods -n feature-015 -l app=fit-first-task-a -o jsonpath='{.items[0].spec.nodeName}')"
k3_b="$(k get pods -n feature-015 -l app=fit-first-task-b -o jsonpath='{.items[0].spec.nodeName}')"
if [[ "${k3_a}" != "${worker2}" || "${k3_b}" != "${worker1}" ]]; then
  printf 'K3 failed: task-a=%s task-b=%s\n' "${k3_a}" "${k3_b}" >&2
  exit 1
fi
printf 'K3 passed: task-a=%s task-b=%s\n' "${k3_a}" "${k3_b}"

reset_scenario
k create namespace feature-015
k apply -f "${artifact_dir}/k4-podgroup.yaml"
k apply -f "${artifact_dir}/k4-deployment.yaml"
k rollout status -n feature-015 deployment/ordinary --timeout=180s
k4_running="$(k get pods -n feature-015 -l app=ordinary \
  -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name}{"\n"}{end}' |
  awk 'NF { count++ } END { print count + 0 }')"
if [[ "${k4_running}" -ne 2 ]]; then
  printf 'K4 failed: running=%s\n' "${k4_running}" >&2
  exit 1
fi
printf 'K4 passed: running=2\n'

reset_scenario
