#!/usr/bin/env bash
# List Scaleway Instances tagged "karpenter-managed" that have no matching
# NodeClaim in the kube cluster — i.e. provider-side leaks.
#
# Pure read-only by default. Pass --terminate to actually destroy the
# orphans. Requires the same admin SCW_SECRET_KEY/SCW_ACCESS_KEY env used by
# terraform.
set -euo pipefail

ZONE=${ZONE:-fr-par-1}
TERMINATE=false
[ "${1:-}" = "--terminate" ] && TERMINATE=true

if [ -z "${SCW_SECRET_KEY:-}" ]; then
  echo "SCW_SECRET_KEY env var required" >&2
  exit 1
fi

# 1. List Karpenter-managed Instances
servers_json=$(curl -fsS -H "X-Auth-Token: $SCW_SECRET_KEY" \
  "https://api.scaleway.com/instance/v1/zones/$ZONE/servers?tags=karpenter-managed&per_page=100")
ids=$(echo "$servers_json" | jq -r '.servers[] | .id')

if [ -z "$ids" ]; then
  echo "No Karpenter-managed Instances at all in $ZONE."
  exit 0
fi

# 2. Cross-check against NodeClaims (best-effort : if no kubeconfig is
#    reachable, we still print the Instance list).
nodeclaims_json=""
if kubectl get nodeclaims -A -o json 2>/dev/null > /tmp/nc.json; then
  nodeclaims_json=$(cat /tmp/nc.json)
fi

orphans=()
for id in $ids; do
  if [ -z "$nodeclaims_json" ]; then
    orphans+=("$id")
    continue
  fi
  if ! echo "$nodeclaims_json" \
       | jq -e --arg id "$id" '.items[] | select(.metadata.annotations["karpenter.scaleway.com/instance-id"] == $id)' \
       > /dev/null; then
    orphans+=("$id")
  fi
done

if [ ${#orphans[@]} -eq 0 ]; then
  echo "No orphans."
  exit 0
fi

echo "Orphans (${#orphans[@]}) :"
for id in "${orphans[@]}"; do
  name=$(echo "$servers_json" | jq -r --arg id "$id" '.servers[] | select(.id == $id) | .name')
  state=$(echo "$servers_json" | jq -r --arg id "$id" '.servers[] | select(.id == $id) | .state')
  echo "  $id  $name  ($state)"
done

if $TERMINATE; then
  for id in "${orphans[@]}"; do
    echo "Terminating $id ..."
    curl -s -X POST -H "X-Auth-Token: $SCW_SECRET_KEY" \
      "https://api.scaleway.com/instance/v1/zones/$ZONE/servers/$id/action" \
      -d '{"action":"terminate"}' | jq -r '.action.action // "no-action"'
  done
fi
