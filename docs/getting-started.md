# Getting started

This walkthrough installs `karpenter-provider-scaleway` on an existing
**Kapsule** cluster from the published Helm chart and provisions a worker
node on demand. Time budget : ~10 minutes once you have credentials in
hand.

Running on **Kosmos** (multicloud) ? Kosmos support is **experimental**:
follow the same steps, plus the extra prerequisites and the seed-node step
described in [Kosmos clusters](#kosmos-clusters) at the end of this page,
and read the known issues there first.

## Prerequisites

- A Scaleway **Kapsule** cluster at version 1.30+, reachable via
  `kubectl`, with at least one existing node pool (the controller pods
  run there).
- A Scaleway IAM API key for the controller.

  Permission sets validated on Kapsule, scoped to the cluster's project:
  - `KubernetesFullAccess`: create, list and delete pools; read the cluster.
  - `InstancesReadOnly`: server types and stock availability.
  - `MarketplaceReadOnly` if the console offers it (image label
    resolution; the endpoint is public and usually works without it).

  Scaleway has no pool-only permission set, so `KubernetesFullAccess` can
  also delete the cluster: scope the policy to the project and keep the key
  in a Kubernetes Secret (`scaleway.existingSecret`). Prices come from the
  public Product Catalog API and need no permission.

- `helm` 3.10+ (OCI support), `kubectl` 1.30+.

## Step 1 — Check the chart is reachable

The controller image and the Helm chart are published side-by-side in the
`karpenter-scaleway` namespace of the Scaleway Container Registry
(`fr-par`) :

- chart : `oci://rg.fr-par.scw.cloud/karpenter-scaleway/charts/karpenter-scaleway`
- image : `rg.fr-par.scw.cloud/karpenter-scaleway/karpenter-provider-scaleway`

```sh
helm show chart oci://rg.fr-par.scw.cloud/karpenter-scaleway/charts/karpenter-scaleway \
  --version 0.4.9
```

## Step 2 — Install the chart

```sh
helm install karpenter-scaleway \
  oci://rg.fr-par.scw.cloud/karpenter-scaleway/charts/karpenter-scaleway \
  --version 0.4.9 \
  --namespace karpenter --create-namespace \
  --set scaleway.accessKey=$SCW_ACCESS_KEY \
  --set scaleway.secretKey=$SCW_SECRET_KEY \
  --set scaleway.organizationID=$SCW_DEFAULT_ORGANIZATION_ID \
  --set scaleway.projectID=$SCW_DEFAULT_PROJECT_ID \
  --set scaleway.clusterID=$SCW_CLUSTER_ID \
  --set scaleway.region=fr-par
```

Notes on the values :

- **`scaleway.projectID` is mandatory whenever the cluster does not live
  in the organization's default project.** It is the Scaleway project the
  controller creates resources in, and must be the cluster's project (and
  one the IAM key is authorized for). When empty it falls back to
  `scaleway.organizationID`, which only matches the organization's
  default project — on any other project resources end up in the wrong
  project, or creation fails with `insufficient permissions`.
- The image tag defaults to the chart `appVersion`, so image and chart
  stay in lockstep ; there is no need to set `image.repository` or
  `image.tag`.
- To avoid passing keys on the command line, create a Secret with keys
  `SCW_ACCESS_KEY` / `SCW_SECRET_KEY` in the `karpenter` namespace and
  use `--set scaleway.existingSecret=<name>` instead of
  `accessKey` / `secretKey`.
- The full list of values is in the
  [chart README](../charts/karpenter-scaleway/README.md#values-reference).

Verify the controller is up and that auto-detection picked the right
backend :

```sh
kubectl -n karpenter get pods
kubectl -n karpenter logs -l app.kubernetes.io/name=karpenter-scaleway --tail=30 | grep "backend resolved"
```

You should see one line like `backend resolved {backend: kapsule, source: auto, ...}`.

## Step 3 — Apply a `ScalewayNodeClass`

```yaml
# nodeclass.yaml
apiVersion: karpenter.scaleway.com/v1alpha1
kind: ScalewayNodeClass
metadata:
  name: default
spec:
  zones: ["fr-par-1"]
  imageLabel: ubuntu_jammy
```

`spec.zones` is the whitelist of Scaleway zones the controller may pick
from. List several zones (e.g. `["fr-par-1", "fr-par-2"]`) to spread
multi-zone workloads, or omit the field to allow all 9 zones.

```sh
kubectl apply -f nodeclass.yaml
kubectl get scalewaynodeclasses default -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
# True
```

If the status stays `False` with reason `ImageNotFound`, double-check
`spec.imageLabel` against the Scaleway Marketplace.

## Step 4 — Apply a `NodePool` and a deployment

```yaml
# nodepool.yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: default
spec:
  template:
    spec:
      nodeClassRef:
        group: karpenter.scaleway.com
        kind: ScalewayNodeClass
        name: default
      # Kapsule (Cilium): Cilium taints new nodes until its agent is up.
      # Karpenter must know it, or it launches an extra node for each pod
      # waiting on a node that is Initialized but still tainted.
      startupTaints:
        - key: node.cilium.io/agent-not-ready
          value: "true"
          effect: NoSchedule
      requirements:
        - key: node.kubernetes.io/instance-type
          operator: In
          values: ["GP1-XS"]
        - key: kubernetes.io/arch
          operator: In
          values: ["amd64"]
  limits:
    cpu: "32"
    memory: 64Gi
  disruption:
    consolidationPolicy: WhenEmptyOrUnderutilized
```

```sh
kubectl apply -f nodepool.yaml
kubectl create deployment hammer --image=nginx --replicas=10
kubectl get nodeclaims --watch
```

A NodeClaim should appear, then transition to Ready within ~60 s. On
Kapsule each NodeClaim is backed by a dedicated single-node pool :

```sh
scw k8s pool list cluster-id=$SCW_CLUSTER_ID
```

### Note on `topology.kubernetes.io/zone`

The `NodePool` above does not carry an explicit
`topology.kubernetes.io/zone` requirement. When that requirement is
absent, the controller falls back to the first zone listed under
`ScalewayNodeClass.spec.zones` (here `fr-par-1`). For multi-zone
deployments where the scheduler should drive the spread, declare the
requirement explicitly:

```yaml
      requirements:
        - key: topology.kubernetes.io/zone
          operator: In
          values: ["fr-par-1", "fr-par-2"]
```

The values must be a subset of `ScalewayNodeClass.spec.zones` — the
controller rejects launches outside that whitelist.

## Cleaning up

```sh
kubectl delete deployment hammer
# Wait for consolidation to drain the node.
helm uninstall karpenter-scaleway -n karpenter
```

Helm 3 doesn't remove CRDs on uninstall — drop them manually if you want
a clean wipe :

```sh
kubectl delete crd scalewaynodeclasses.karpenter.scaleway.com
```

## Kosmos clusters

> **Kosmos support is experimental.** It is not covered by the
> validation done on Kapsule and has two known issues:
> - the node registration key (`SCW_SECRET_KEY`) is written in clear text
>   into the instance cloud-init, so anyone who can read the instance
>   user-data or `/var/log/cloud-init*` on the node can read it. Use a
>   dedicated key limited to `KubernetesExternalNodeRegister` and rotate it;
> - a node replacing an expired one can come up without a pod CIDR and stay
>   `NotReady` (CNI error `no IP ranges specified`).
>
> Do not use the Kosmos backend in production yet.

The same chart and image work on Kosmos — the binary picks the Kosmos
backend from `cluster.Type`. The differences with the Kapsule walkthrough
above are listed here.

### Extra prerequisites

- A seed pool ID and a dedicated IAM API key carrying the
  `KubernetesExternalNodeRegister` permission set (used by `k8s-agent`
  on the worker). See `terraform/iam.tf` for the spike provisioning
  pattern.

### Extra chart values

Add these flags to the `helm install` command of Step 2 :

```sh
  --set scaleway.kosmos.poolID=$SCW_KOSMOS_POOL_ID \
  --set scaleway.kosmos.apiKey=$SCW_KOSMOS_API_KEY
```

On Kosmos the controller creates Scaleway Instances in
`scaleway.projectID` (see the note in Step 2) : if the cluster is not in
the organization's default project and the value is left empty, instance
creation fails with `insufficient permissions: create compute_servers`.

The `backend resolved` log line should read `backend: kosmos`.

### Seed a worker node before installing

> **Kosmos chicken-and-egg.** A pure-Kosmos cluster created with
> `node_type = "external"` has zero pre-existing worker nodes : Scaleway
> does not provision them. The Karpenter controller is itself a pod that
> needs a node to land on, so `helm install` would leave the controller
> pods `Pending` forever — there is nobody to provision the first node.
> If your Kosmos cluster already has a worker node attached, skip this
> step.

Pick one Scaleway Instance to act as the bootstrap node. It will host the
controller pod and any other system workload until Karpenter's first
NodeClaim lands ; you can drain and delete it afterwards if you want a
fully Karpenter-managed fleet.

```sh
# Inputs : the same pool ID and Kosmos IAM key used by the chart.
SEED_REGION=fr-par
SEED_ZONE=fr-par-1
SEED_TYPE=DEV1-S
K8S_AGENT_URL=https://github.com/scaleway/k8s-agent/releases/download/v0.1.5/k8s-agent_linux_amd64

cat > /tmp/seed-cloud-init.sh <<EOF
#!/bin/bash
set -euo pipefail
curl --retry 5 --retry-delay 2 --retry-all-errors \\
     -fsSLo /usr/local/bin/k8s-agent "${K8S_AGENT_URL}"
chmod +x /usr/local/bin/k8s-agent
POOL_ID=${SCW_KOSMOS_POOL_ID} \\
POOL_REGION=${SEED_REGION} \\
SCW_SECRET_KEY=${SCW_KOSMOS_API_KEY} \\
/usr/local/bin/k8s-agent --kosmos
EOF

scw instance server create \
  zone=${SEED_ZONE} \
  type=${SEED_TYPE} \
  image=ubuntu_jammy \
  name=karpenter-seed \
  cloud-init=@/tmp/seed-cloud-init.sh \
  tags.0=karpenter-seed
```

The script mirrors what the controller's own cloud-init does
([`pkg/scaleway/cloudinit.go`](../pkg/scaleway/cloudinit.go)) — pull
`k8s-agent`, then run it once with the pool's API key and region. Wait
until the node is `Ready` :

```sh
kubectl get nodes --watch
# karpenter-seed   Ready   <none>   90s   v1.30.x
```

Now run the `helm install` command of Step 2 (with the Kosmos flags
above). The controller pod schedules on the seed, and from that point
Karpenter can create new Kosmos nodes on demand.

> **Why not automate this in the chart ?** The two alternatives we
> considered both add more moving parts than they remove :
> embedding a Helm `pre-install` Job that calls the Scaleway API would
> need credentials and a place to run before any node exists, and adding
> a managed Kapsule pool to the Kosmos cluster is not supported on
> `type = "multicloud"`.

### Provisioning

Steps 3 and 4 are identical. Kosmos NodeClaims typically reach Ready
within ~90 s ; each one is backed by a Scaleway Instance :

```sh
scw instance server list tags.0=karpenter-managed
```

`ScalewayNodeClass` fields such as `rootVolumeType` and `publicIP` are
Kosmos-only (ignored on Kapsule); `rootVolumeSizeGB` applies to both — see the
[API reference](api/scalewaynodeclass.md).
