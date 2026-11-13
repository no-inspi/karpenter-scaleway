# Troubleshooting

A field-tested checklist for the most common failure modes. Read top to
bottom — entries are roughly ordered by frequency.

## 1. The pod stays `CrashLoopBackOff` with "missing required env var"

The controller validates every required env at boot. The pod log will
name the missing key. Most often :

- `SCW_CLUSTER_ID` not set in the ConfigMap → set
  `--set scaleway.clusterID=$SCW_CLUSTER_ID` at install.
- `SCW_KOSMOS_POOL_ID` / `SCW_KOSMOS_API_KEY` missing on a Kosmos cluster
  → these are conditional but **required** when the backend resolves to
  Kosmos.

## 2. Auto-detection fails : "unsupported cluster.Type"

The Scaleway K8s API returned a value the provider doesn't recognise.
Check the type :

```sh
scw k8s cluster get $SCW_CLUSTER_ID
```

Expected types are `kapsule`, `kapsule-dedicated-*`, `multicloud`,
`multicloud-dedicated-*`. Anything else is currently unsupported. The
documented kill-switch is to set `forceBackend` in the Helm values to one
of `kapsule` or `kosmos`, then redeploy.

## 3. `ScalewayNodeClass` stuck on `Ready=False`, reason `ImageNotFound`

The Marketplace lookup returned no image for `(zone, imageLabel)`. Most
common cause : a typo (`ubuntu_jammyy`). Confirm the label exists :

```sh
scw marketplace local-image list zone=fr-par-1 commercial-type=GP1-XS \
  | jq '.[].label' | grep ubuntu
```

The reconciler retries every 30 s once the spec is corrected.

## 4. `ScalewayNodeClass` stuck on `Ready=False`, reason `ValidationPending`

Marketplace API is returning 5xx. The reconciler retries every 10 s. If
the state persists more than a few minutes, check `status.scaleway.com`
or the controller's `karpenter_scaleway_marketplace_errors_total` metric.

## 5. NodeClaim stuck with deletion timestamp and `karpenter.sh/termination` finalizer

This used to bite the initial prototype. The current backends
return `NodeClaimNotFoundError` on successful Delete, so this should not
happen. If it does, verify on the cloud provider that the underlying
resource really is gone :

- Kapsule : `scw k8s pool list cluster-id=$SCW_CLUSTER_ID` — there should
  be no pool tagged `nodeclaim=<stuck-name>`.
- Kosmos  : `scw instance server list tags.0=karpenter-managed` — same.

If the resource is gone but the finalizer hangs, restart the controller
pod ; if it persists, file a bug.

## 6. Kosmos node never reaches Ready

Symptoms : a Scaleway Instance is created, but no kube Node ever appears.

- Confirm the Instance has a public IPv4 (`publicIP: true` in the
  NodeClass) — without it, the k8s-agent cannot download itself.
- Check the cloud-init log on the Instance via the Scaleway console
  Serial Console: `cat /var/log/cloud-init-output.log`. Most failures are
  network-related (registries, k8s-agent download URL).
- Verify `SCW_KOSMOS_API_KEY` carries the
  `KubernetesExternalNodeRegister` permission set.

## 7. Kapsule pool created but node never joins

The Kapsule pool is visible in the Scaleway console with status
`scaling`, but `kubectl get nodes` doesn't show it. Usually a Scaleway
control-plane delay — wait a few minutes. If it stays in `scaling` >5
minutes, the underlying instance might have failed cloud-init ; open the
pool's instance in the console and check the Serial Console.

## 8. Karpenter logs `missing taint prevents registration-related race conditions`

Fixed in v0.4.7: Kapsule pools carry the startup taint
`karpenter.sh/unregistered:NoExecute` and Kosmos nodes register with it
via the kubelet flag. If the event `UnregisteredTaintMissing` still shows,
the node was created by an older controller version (it disappears once
the node is replaced), or the pool was created out-of-band.

## 8b. Karpenter launches one node too many on Kapsule

Symptom: right when a NodeClaim turns `Initialized`, Karpenter creates
another NodeClaim for the pod that was waiting on it. The extra node stays
empty and is consolidated a few minutes later.

Cause: Cilium (the Kapsule default CNI) taints every new node with
`node.cilium.io/agent-not-ready:NoSchedule` until its agent is up. That
taint is not in Karpenter's list of ephemeral taints, so a node that still
carries it looks unusable to the scheduler simulation. Declare it in the
NodePool so Karpenter waits for it to go before treating the node as ready:

```yaml
spec:
  template:
    spec:
      startupTaints:
        - key: node.cilium.io/agent-not-ready
          value: "true"
          effect: NoSchedule
```

Changing the template marks the existing nodes as drifted, so apply it
when the NodePool is empty or accept a rolling replacement. Seen on the
pprod cluster on 2026-11-12, fixed by this setting on 2026-11-13.

## 9. Reading the conditions

```sh
kubectl get scalewaynodeclasses default -o yaml
```

Inspect the `status.conditions` block. Each entry has `type`, `status`,
`reason`, `message`, `lastTransitionTime` — the controller updates
`lastTransitionTime` only when the status flips, so a stale timestamp
combined with a `Ready=True` is normal.

## 10. Useful commands

```sh
# Live tail of the controller's logs.
kubectl -n karpenter logs -l app.kubernetes.io/name=karpenter-scaleway -f

# Force a re-reconcile of a NodeClass (touches metadata).
kubectl -n karpenter annotate scalewaynodeclass default kick=$(date +%s) --overwrite

# Watch NodeClaim transitions.
kubectl get nodeclaims -w
```
