#!/usr/bin/env bash
# Run N E2E latency cycles against a running controller and a Kosmos cluster.
#
# Pre-requisites :
#   - terraform-applied infra (cluster + pool + IAM key)
#   - controller-runner (./karpenter-controller) launched in another shell,
#     tee'd into controller-run-<timestamp>.log in $PWD
#   - hack/karpenter-crds/ applied, hack/scalewaynodeclass.yaml applied,
#     hack/nodepool.yaml applied
#
# Outputs : measurements/run-<timestamp>.csv, one row per cycle.
set -euo pipefail

CYCLES=${1:-5}
NS=karpenter-spike
POD=stress-pod
THRESHOLD=${THRESHOLD:-240}
COOLDOWN=${COOLDOWN:-120}
CONSOLIDATE_WAIT=${CONSOLIDATE_WAIT:-90}
WAIT_TIMEOUT=${WAIT_TIMEOUT:-480}

mkdir -p measurements
CSV="measurements/run-$(date -u +%Y%m%d-%H%M).csv"
echo "cycle,instance_type,T0_C,T1_C,duration_C,duration_B,duration_A,verdict" > "$CSV"

for i in $(seq 1 "$CYCLES"); do
  echo "=== Cycle $i / $CYCLES ==="

  T0_C=$(date -u +%s)

  kubectl apply -f hack/stress-pod.yaml >/dev/null

  if ! kubectl wait --for=condition=Ready pod/$POD -n $NS --timeout=${WAIT_TIMEOUT}s; then
    T1_C=$(date -u +%s)
    DURATION_C=$((T1_C - T0_C))
    echo "$i,GP1-XS,$T0_C,$T1_C,$DURATION_C,,,FAIL" >> "$CSV"
    kubectl delete pod $POD -n $NS --ignore-not-found >/dev/null
    sleep "$CONSOLIDATE_WAIT"
    continue
  fi
  T1_C=$(date -u +%s)
  DURATION_C=$((T1_C - T0_C))

  read -r T0_B T1_B T0_A T1_A < <(hack/extract-timestamps.sh "$T0_C" "$T1_C")
  if [ "$T0_B" -ne 0 ] && [ "$T1_B" -ne 0 ]; then
    DURATION_B=$((T1_B - T0_B))
  else
    DURATION_B=
  fi
  if [ "$T0_A" -ne 0 ] && [ "$T1_A" -ne 0 ]; then
    DURATION_A=$((T1_A - T0_A))
  else
    DURATION_A=
  fi

  if [ "$DURATION_C" -lt "$THRESHOLD" ]; then
    VERDICT=PASS
  else
    VERDICT=FAIL
  fi
  echo "$i,GP1-XS,$T0_C,$T1_C,$DURATION_C,$DURATION_B,$DURATION_A,$VERDICT" >> "$CSV"

  kubectl delete pod $POD -n $NS --ignore-not-found >/dev/null
  echo "  waiting $CONSOLIDATE_WAIT s for node consolidation..."
  sleep "$CONSOLIDATE_WAIT"

  if [ "$i" -lt "$CYCLES" ]; then
    echo "  cool-down $COOLDOWN s ..."
    sleep "$COOLDOWN"
  fi
done

echo "=== Resume ==="
column -t -s, "$CSV"
echo ""
echo "CSV : $CSV"
