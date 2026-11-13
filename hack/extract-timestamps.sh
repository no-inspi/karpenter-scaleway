#!/usr/bin/env bash
# Parse the latest controller log file and emit, on a single space-separated
# line :
#
#   T0_B T1_B T0_A T1_A
#
# all as Unix epoch seconds. The latency.mark events are produced by
# pkg/metrics with both "unix=<epoch>" and "t=<RFC3339>" — we read the epoch
# directly to stay portable across macOS and Linux.
#
# Usage : extract-timestamps.sh <T0_C> <T1_C>
# T0_C / T1_C are Unix epochs from the measure loop ; we use them only as a
# sanity window and warn (not fail) on out-of-window events, so a clock skew
# inside the controller doesn't kill a 30-min run.
set -euo pipefail

if [ $# -lt 2 ]; then
  echo "usage: $0 <T0_C> <T1_C>" >&2
  exit 2
fi
T0_C=$1
T1_C=$2

LOG=$(ls -t controller-run-*.log 2>/dev/null | head -1)
if [ -z "$LOG" ]; then
  echo "no controller-run-*.log found in $PWD" >&2
  exit 1
fi

# Line shape (logr/zap structured) :
#   ... "msg":"latency.mark" ... "phase":"A" ... "point":"T0" ... "unix":1777551644 ...
# We tolerate either JSON or "key=value" log formats.
extract() {
  local phase=$1 point=$2
  grep "latency.mark" "$LOG" \
    | grep -E "\"phase\":\"$phase\"|phase=$phase" \
    | grep -E "\"point\":\"$point\"|point=$point" \
    | tail -1 \
    | grep -oE "\"unix\":[0-9]+|unix=[0-9]+" \
    | grep -oE "[0-9]+"
}

# Retry up to 30s: with the Kapsule backend, the Scaleway K8s API marks the
# node "ready" a few seconds AFTER kubelet has joined kube — so kubectl wait
# can return before our T1 marks are flushed.  Kosmos doesn't hit this since
# we poll kube ourselves, but the retry is harmless there.
T0_B="" T1_B="" T0_A="" T1_A=""
for _ in $(seq 1 30); do
  T0_B=$(extract B T0 || true)
  T1_B=$(extract B T1 || true)
  T0_A=$(extract A T0 || true)
  T1_A=$(extract A T1 || true)
  if [ -n "$T0_B" ] && [ -n "$T1_B" ] && [ -n "$T0_A" ] && [ -n "$T1_A" ]; then
    break
  fi
  sleep 1
done

for label in T0_B T1_B T0_A T1_A; do
  ts=$(eval echo \$$label)
  if [ -z "$ts" ]; then
    echo "WARN: $label not found in $LOG" >&2
    eval "$label=0"
  fi
done

# Sanity : all must sit inside [T0_C-30s, T1_C+30s]
for label in T0_B T1_B T0_A T1_A; do
  ts=$(eval echo \$$label)
  if [ "$ts" -ne 0 ] && { [ "$ts" -lt "$((T0_C - 30))" ] || [ "$ts" -gt "$((T1_C + 30))" ]; }; then
    echo "WARN: $label=$ts outside cycle window [$T0_C, $T1_C]" >&2
  fi
done

echo "$T0_B $T1_B $T0_A $T1_A"
