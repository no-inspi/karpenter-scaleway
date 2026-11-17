# karpenter-provider-scaleway

A [Karpenter](https://karpenter.sh) cloudprovider for Scaleway-managed
Kubernetes — one binary, one Helm chart, auto-detects the cluster type at
boot.

| Cluster type | Support |
|---|---|
| **Kapsule** | Supported. Each NodeClaim is a Kapsule pool of size 1. |
| **Kosmos** (multicloud) | **Experimental**, see the warning below. |

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

## Status

- **v0.1.0** (*Foundations*) — figures the v1alpha1 CRD, Backend
  interface, NodeClass reconciler with Marketplace round-trip, and Helm
  chart skeleton. Mono-zone, hardcoded instance type catalog, no drift
  detection (parity with the spike).
- **v0.2.0** (*Multi-zone & dynamic catalog*) — multi-zone
  scheduling across the 9 Scaleway zones, generated instance type
  catalog (`make generate-catalog`) embedded at build time, live
  pricing refresh every 12h, breaking CRD change `spec.zone` →
  `spec.zones`.
- **v0.3.0** (*Drift detection*) — `Backend.IsDrifted` implemented
  on both backends (instance type, zone, pool resize, tags, image label),
  plus the per-NodeClaim `karpenter.scaleway.com/skip-drift=true` escape
  hatch.
- **v0.4.0** (*Kapsule quota robustness* + *distribution*) —
  observability and backpressure preflight for the Scaleway 150
  pools/cluster limit (`KAPSULE_POOL_QUOTA` /
  `KAPSULE_POOL_QUOTA_THRESHOLD`, pool quota metrics), and first stable
  release published by the release workflow: controller image, OCI Helm
  chart and `linux/{amd64,arm64}` binaries in the public
  `karpenter-scaleway` registry namespace (see [RELEASE.md](RELEASE.md)).
  Preemptible / capacity types were dropped (not exposed by the Scaleway
  Kapsule API).
- **v0.4.1** — Kapsule fails fast on nodes stuck in Scaleway
  `creation_error` (relaunch on another type/zone instead of a 4-min
  hang), plus opt-in `ServiceMonitor`, `PrometheusRule` and Grafana
  dashboard in the chart.
- **v0.4.2** — chart RBAC aligned with the upstream Karpenter core
  ClusterRole (the controller never became Ready on clusters with PVCs);
  creation-errors alert also counts `outcome=ice`.
- **v0.4.3** — Kapsule no longer flags every NodeClaim as drifted
  (`pro2_s` vs `PRO2-S` node type mismatch caused a replacement loop).
- **v0.4.4** — `Create` picks the cheapest candidate instance type and
  zone instead of the alphabetically first one.
- **v0.4.5** — live pricing ignores the zero `hourly_price` now returned
  by the Instance API and keeps the catalog baseline.
- **v0.4.6** — live prices from the Scaleway Product Catalog API; embedded
  catalog regenerated (119 types).
- **v0.4.7** — nodes start with the `karpenter.sh/unregistered:NoExecute`
  taint (Kapsule pool startup taint, Kosmos kubelet flag).
- **v0.4.8** — `rootVolumeSizeGB` applied on Kapsule; instance types
  advertise `ephemeral-storage` sized from the root volume.
- **v0.4.9** — pods with Scaleway Block Storage volumes can be provisioned
  and their nodes replaced (`topology.csi.scaleway.com/zone` mapped).
- **Next** — automated real-cluster E2E tests in CI.

See [CHANGELOG.md](CHANGELOG.md) for the full release notes.

## Architecture in 60 seconds

```
                                 ┌──────────────────────────┐
                                 │  ScalewayNodeClass CRD   │
                                 │  (zone, imageLabel, …)   │
                                 └────────────┬─────────────┘
                                              │ watch
        ┌──────────────────────┐   ┌──────────┴──────────────┐
        │   Karpenter core     │   │  NodeClass reconciler   │
        │   (NodePool/Claim)   │   │  Marketplace round-trip │
        └──────────┬───────────┘   └──────────┬──────────────┘
                   │ create/delete/get/list             │ status
                   ▼                                    ▼
   ┌─────────────────────────────────────────────────────────────┐
   │  pkg/cloudprovider thin wrapper (karpcp.CloudProvider)      │
   └────────────────┬────────────────────────────────────────────┘
                    │ delegates
                    ▼
   ┌──────────────────────────────────────────────────────────────┐
   │  pkg/backend.Backend                                         │
   │   ├─ pkg/backend/kapsule  ←─  Scaleway K8s pool API          │
   │   └─ pkg/backend/kosmos   ←─  Scaleway Instance API + cloud-init │
   └──────────────────────────────────────────────────────────────┘
```

The Backend resolved at boot is immutable for the lifetime of the process.
Auto-detection reads `cluster.Type` from the Scaleway K8s API; the
`SCW_FORCE_BACKEND` env (or `--set forceBackend=` Helm value) is the
documented kill-switch.

## Install (Helm)

The chart and the controller image are published as OCI artifacts in the
`karpenter-scaleway` namespace of the Scaleway Container Registry; see
[docs/getting-started.md](docs/getting-started.md) for the full
walkthrough.

### Kapsule

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

`scaleway.projectID` is **mandatory when the cluster is not in the
organization's default project**: it falls back to
`scaleway.organizationID`, which only matches the default project.

### Kosmos (experimental)

> Experimental: read the known issues at the top of this page first.

Same command, plus the Kosmos seed-pool credentials (the IAM key with
`KubernetesExternalNodeRegister`) :

```sh
  --set scaleway.kosmos.poolID=$SCW_KOSMOS_POOL_ID \
  --set scaleway.kosmos.apiKey=$SCW_KOSMOS_API_KEY
```

A pure-Kosmos cluster has no worker node for the controller to run on:
seed one first, see
[getting-started — Kosmos clusters](docs/getting-started.md#kosmos-clusters).

The same chart and image work for both backends — the binary picks the
right implementation based on `cluster.Type`.

## Example NodeClass + NodePool

```yaml
apiVersion: karpenter.scaleway.com/v1alpha1
kind: ScalewayNodeClass
metadata:
  name: default
spec:
  # Optional whitelist; empty / omitted means "all 9 Scaleway zones".
  zones: ["fr-par-1", "fr-par-2", "fr-par-3"]
  imageLabel: ubuntu_jammy
  rootVolumeSizeGB: 50         # root disk; also sets the ephemeral-storage capacity
  publicIP: true               # kosmos-only, ignored on kapsule
---
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
          values: ["GP1-XS", "GP1-S"]
        - key: kubernetes.io/arch
          operator: In
          values: ["amd64", "arm64"]
        - key: topology.kubernetes.io/zone
          operator: In
          values: ["fr-par-1", "fr-par-2"]   # narrows further than the NodeClass whitelist
```

The Karpenter scheduler picks one zone per NodeClaim from the
intersection of `NodePool.requirements[topology.kubernetes.io/zone]`
and `NodeClass.spec.zones`; the provider provisions in that exact zone.
With `topologySpreadConstraints` on the workload, NodeClaims fan out
across multiple zones.

| Backend | Target P95 latency (Pending → Running) |
|---|---|
| Kapsule | < 120 s |
| Kosmos  | < 120 s |

Latency is dominated by the Scaleway API itself (`k8s.CreatePool` for
Kapsule, `instance.CreateServer` + cloud-init + k8s-agent registration
for Kosmos), tracked live via
`karpenter_scaleway_nodeclaim_provisioning_duration_seconds`.

## Documentation

| Topic | File |
|---|---|
| Getting started | [docs/getting-started.md](docs/getting-started.md) |
| `ScalewayNodeClass` API reference | [docs/api/scalewaynodeclass.md](docs/api/scalewaynodeclass.md) |
| Operations / troubleshooting | [docs/operations/troubleshooting.md](docs/operations/troubleshooting.md) |

## Versions

See [VERSIONS.md](VERSIONS.md) for the pinned dependency matrix.

## Repository layout

```
api/v1alpha1/                CRD types + status
cmd/controller/              Single binary entrypoint
cmd/generate-catalog/        Build-time tool: live API → catalog.json
charts/karpenter-scaleway/   Helm chart (incl. CRD)
pkg/backend/                 Backend interface + auto-detect + factory
pkg/backend/kapsule/         Kapsule implementation
pkg/backend/kosmos/          Kosmos implementation
pkg/cloudprovider/           Thin wrapper to karpcp.CloudProvider
pkg/controllers/nodeclass/   ScalewayNodeClass reconciler + image cache
pkg/scaleway/                Scaleway SDK client + interfaces (zone-agnostic)
pkg/scheme/                  Centralized runtime.Scheme builder
pkg/instancetype/            Embedded generated catalog + Filter
pkg/pricing/                 Live pricing provider (12h refresh, baseline fallback)
pkg/metrics/                 Latency markers + provisioning histogram
hack/                        Codegen / measurement scripts
docs/                        Operator-facing documentation
```

## Contributing

Contributions go through an issue first: open an issue, and once it is
labelled `accepted`, open a pull request that links it. See
[CONTRIBUTING.md](CONTRIBUTING.md). Security issues: see
[SECURITY.md](SECURITY.md).

## License

[Apache License 2.0](LICENSE).
