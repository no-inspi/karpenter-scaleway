# Changelog

## [Unreleased]

## [0.4.9] — 2026-11-17 (Scaleway CSI zone label)

### Fixed

- Pods that mount a Scaleway Block Storage volume can now be provisioned
  and their nodes replaced. The CSI driver pins PVs with
  `topology.csi.scaleway.com/zone`, which Karpenter did not map to
  `topology.kubernetes.io/zone`, so no instance type matched: drift and
  expiry replacements were blocked (`DisruptionBlocked`, misleadingly
  reported as "all available instance types exceed limits").

## [0.4.8] — 2026-11-17 (Kapsule root volume size, ephemeral-storage)

### Fixed

- Kapsule: `spec.rootVolumeSizeGB` is now applied to the node pool instead
  of being ignored. Changing it marks existing Kapsule NodeClaims as drifted
  (`RootVolumeSizeDrift`) so they are replaced with the new disk size.
- Instance types now advertise `ephemeral-storage` (90% of the root volume,
  20 GB by default on Kapsule, minus a 10% eviction threshold). Before,
  Karpenter could never place a pod that requests `ephemeral-storage`, and
  could not bin-pack disk-heavy pods. On Kosmos it is advertised only when
  `rootVolumeSizeGB` is set.

### Changed

- Kapsule is the supported backend; **Kosmos is marked experimental**
  (clear-text node registration key in cloud-init, replacement node
  without pod CIDR after expiry). README and getting-started say so.
- Go module renamed to `github.com/no-inspi/karpenter-scaleway` to match
  the repository.
- getting-started lists the IAM permission sets validated on Kapsule.
- NodePool examples declare Cilium's `node.cilium.io/agent-not-ready` as a
  startup taint (Kapsule default CNI), otherwise Karpenter launches one
  extra node per NodeClaim.

## [0.4.7] — 2026-11-10 (karpenter.sh/unregistered startup taint)

### Fixed

- Nodes now start with the `karpenter.sh/unregistered:NoExecute` taint,
  which Karpenter removes once it has applied the NodePool labels and
  taints. Without it, pods could land on a new node before then, and every
  NodeClaim emitted `UnregisteredTaintMissing`.
  - Kapsule: set as a pool startup taint (applied once, not reconciled, so
    Karpenter can remove it). Bumps `scaleway-sdk-go` to v1.0.0-beta.37,
    the first release to expose `StartupTaints` on `CreatePoolRequest`.
  - Kosmos: the cloud-init kubelet drop-in used `NoSchedule`. Karpenter
    matches the key and the effect, so it now uses `NoExecute`.

## [0.4.6] — 2026-11-10 (live prices from the Product Catalog API)

### Changed

- Pricing: live prices now come from the public Scaleway Product Catalog
  API (`product_catalog/v2alpha1`, `ListPublicCatalogProducts` filtered on
  instance products per zone, hourly retail price keyed by
  `properties.instance.offer_id`). The Instance API still provides stock
  availability. The 12h refresh keeps the 0.4.5 safety net: a type the
  catalog does not price keeps its previous price.
- `make generate-catalog` reads prices from the same API and skips
  offerings without a catalog price. The embedded catalog is regenerated:
  119 types (29 new BASIC2/3, COMPUTE3, MEMORY3, POP2, STANDARD2/3 sizes),
  prices about 2 % higher than the May 2026 baseline (e.g. PRO2-XXS
  0.0561 EUR/h in fr-par-1).
- The public catalog needs no IAM permission.

### Fixed

- Kapsule: when `Create` gets back a pool that already exists for the
  NodeClaim (idempotent `CreatePool`, e.g. a Create resumed by a new leader
  after a rollout), the NodeClaim's `instance-type` and `zone` labels now
  come from that pool rather than from this call's own pick. Before, a pool
  created in PRO2-S by the previous leader was labelled PRO2-XXS by the new
  one, triggering an immediate `InstanceTypeDrift` and an extra node. A pool
  whose type or zone is not among the NodeClaim's candidates is deleted and
  `Create` returns an error instead of adopting it.

## [0.4.5] — 2026-11-10 (pricing: ignore zero live prices)

### Fixed

- Pricing: the Scaleway Instance API now returns `hourly_price=0` for every
  server type. The 12h refresh overwrote the embedded catalog prices with
  0, so every offering looked free: the cheapest-pick of 0.4.4 fell back to
  the alphabetically first type (PRO2-S instead of PRO2-XXS on pprod), and
  Karpenter's own cost-based decisions (consolidation, cost metrics) saw
  0 EUR for every node. A zero live price is now treated as unknown and the
  previous price (catalog baseline) is kept; a log line reports how many
  server types the API did not price.
- `make generate-catalog` refuses to write a catalog with zero prices.

### Known limits

- Prices come from the embedded catalog baseline until the controller
  reads the Scaleway Product Catalog API instead of the Instance API.

## [0.4.4] — 2026-11-10 (price-aware instance type and zone pick)

### Fixed

- Kapsule and Kosmos launched the first instance type and zone listed on
  the NodeClaim. Karpenter serializes those requirements as alphabetical
  sets, not by price, so a 1-CPU pod on a test cluster got a PRO2-S
  (8 vCPU, 0.219 EUR/h) instead of a PRO2-XXS (2 vCPU, 0.055 EUR/h).
  `Create` now picks the cheapest priced (instance type, zone) pair among
  the candidates (`pkg/backend/selection`), skipping offerings masked
  after a recent `creation_error`, and falls back to the first candidate
  when no price is known.

## [0.4.3] — 2026-11-10 (Kapsule drift fix)

### Fixed

- Kapsule: every NodeClaim was flagged `InstanceTypeDrift` right after
  launch and replaced in a loop. The Kapsule API accepts `PRO2-S` on
  CreatePool but returns the pool's `node_type` as `pro2_s`, and drift
  compared the raw value with the `node.kubernetes.io/instance-type` label.
  Pool node types are now normalized to the catalog form (`PRO2-S`) when
  read, which also fixes the instance-type label on NodeClaims rebuilt by
  `List`. Found on the first pprod NodeClaim.

## [0.4.2] — 2026-11-10 (chart RBAC fix)

### Fixed

- Chart RBAC: the ClusterRole was missing `persistentvolumeclaims`,
  `persistentvolumes`, `replicationcontrollers` (get/list/watch),
  `customresourcedefinitions` (list/watch) and `pods` (delete) compared to
  the upstream Karpenter v1.10 core ClusterRole. On any cluster with PVCs
  the informer cache never synced and the controller stayed not Ready.

### Changed

- Chart alert `KarpenterScalewayCreationErrors` now also counts
  `outcome=ice` (Kapsule node in `creation_error`), so repeated capacity
  failures in a zone raise a warning instead of being retried silently.

## [0.4.1] — 2026-11-10 (Kapsule creation_error fail-fast + monitoring)

### Added

- Helm chart monitoring, all opt-in (off by default):
  - `serviceMonitor.enabled`: Prometheus Operator `ServiceMonitor` on the
    metrics port (labels, namespace, interval, relabelings configurable).
  - `prometheusRule.enabled`: `PrometheusRule` with alerts for controller
    down / no leader, repeated NodeClaim creation errors
    (`outcome=fail|rejected`), Kapsule pool quota near limit and quota
    rejections, NodeClaims stuck non-Ready, and stale pricing. Thresholds,
    per-alert disable list and extra labels/rules configurable.
  - `grafanaDashboard.enabled`: Grafana dashboard ConfigMap (sidecar label)
    built from `charts/karpenter-scaleway/dashboards/karpenter-scaleway.json`
    — managed nodes, estimated cost, disruption, provisioning, quota, pricing.

### Changed

- Installation docs (`docs/getting-started.md`, `README.md`) now install
  from the published OCI chart
  (`oci://rg.fr-par.scw.cloud/karpenter-scaleway/charts/karpenter-scaleway`)
  instead of building the image locally, focus on Kapsule (Kosmos moved
  to a dedicated section), and document `scaleway.projectID` as required
  when the cluster is not in the organization's default project. README
  status aligned with this changelog (v0.3.0 and v0.4.0 released,
  distribution shipped in 0.4.0).

### Fixed

- Kapsule: a node stuck in Scaleway `creation_error` (assumed capacity
  shortage) no longer blocks Create for the full 4-min timeout. The failed
  pool is deleted (retried for up to 2 min while Scaleway still reports it
  as `scaling`, failures logged), the instance-type/zone offering is
  reported unavailable for 3 min, and Create returns
  `InsufficientCapacityError` so Karpenter relaunches on another type or
  zone. Transient `ListNodes` errors (429/5xx) during the readiness wait
  are now retried instead of aborting Create. The provisioning histogram
  gains an `outcome=ice` label value, distinct from `fail`.

## [0.4.0] — 2026-11-10 (*Kapsule quota robustness* + *distribution*)

### Added

- Kapsule pool-quota observability + backpressure preflight, addressing
  the Scaleway **150 pools/cluster** hard limit that is invisible on small
  clusters but blocks serious deployments.
- `KapsuleAPI.CountPools` — single `ListPools` roundtrip returning both the
  cluster-wide pool count (out-of-band pools created via console/Terraform
  count against the quota too) and the karpenter-managed subset.
- Backpressure preflight on `Backend.Create` (Kapsule): once the observed
  pool count reaches the threshold, Create is rejected with a typed
  `ErrQuotaExceeded` and recorded as `outcome=rejected` on the provisioning
  histogram. `CountPools` transient failures degrade to best-effort —
  missing observability beats blocking Create on a Scaleway 5xx flake.
- Configurable quota via env: `KAPSULE_POOL_QUOTA` (default `150`) and
  `KAPSULE_POOL_QUOTA_THRESHOLD` (default `140`, clamped to `Limit-1` on
  misconfiguration).
- Metrics:
  - `karpenter_scaleway_kapsule_pools_total{origin=karpenter|other}` gauge.
  - `karpenter_scaleway_kapsule_pool_quota_remaining` gauge (negative when
    the cluster already exceeds the limit).
  - `karpenter_scaleway_kapsule_pool_quota_rejections_total` counter.

### Distribution

- First stable release published by `.github/workflows/release.yml`: the
  controller image, the Helm chart (OCI) and the `linux/{amd64,arm64}`
  binaries plus `SHA256SUMS` all carry the git tag version (without the `v`).
- New public registry namespace `karpenter-scaleway` (replaces the
  pre-release `test-karpenter-pkg`):
  - image `rg.fr-par.scw.cloud/karpenter-scaleway/karpenter-provider-scaleway:0.4.0`
  - chart `oci://rg.fr-par.scw.cloud/karpenter-scaleway/charts/karpenter-scaleway --version 0.4.0`
- The controller logs its version at boot (`pkg/version`, set via ldflags).

### Fixed

- Chart: new `scaleway.projectID` value. `SCW_DEFAULT_PROJECT_ID` used to be
  set to the organization ID, so resources were created in the wrong project
  Set this value whenever the cluster does not live in the organization's
  default project.

### Out of scope

- Preemptible / capacity types — Decision B retired: `scaleway-sdk-go`
  `k8s.CreatePoolRequest` exposes no spot/preemptible/lifecycle field.
- Bin-packing quota fallback (multiple NodeClaims per pool) — deferred; the
  `1 pool = 1 NodeClaim` invariant is preserved.

## [0.3.0] — 2026-05-05 (*Drift detection*)

### Added

- `Backend.IsDrifted` is no longer a stub. The Kapsule and Kosmos
  backends now compare the live Scaleway resource against the state
  captured on the NodeClaim at Create time and surface a structured
  `cloudprovider.DriftReason` when they diverge. Karpenter's disruption
  controller picks up the reason on `NodeClaim.status.conditions[Drifted]`
  and recycles the NodeClaim through the standard drift workflow.
- DriftReason values (stable strings, written into the Drifted condition):
  - `InstanceTypeDrift` — both backends; live `pool.NodeType` /
    `instance.Type` no longer matches the NodeClaim's
    `node.kubernetes.io/instance-type` label.
  - `ZoneDrift` — both backends; live zone no longer matches the
    NodeClaim's `topology.kubernetes.io/zone` label.
  - `PoolResizeDrift` — Kapsule only; the pool was scaled outside
    Karpenter (`pool.Size != 1`).
  - `TagsDrift` — Kosmos only; the `nodeclaim=<name>` correlation tag
    was stripped from the Instance.
  - `ImageLabelDrift` — Kosmos only; `NodeClass.Spec.ImageLabel` changed
    after the NodeClaim was created.
- Per-NodeClaim escape hatch
  `karpenter.scaleway.com/skip-drift=true` — when set, the backend
  skips its own drift checks. Karpenter's static-fields/requirements
  drift checks still fire. Use case: operator-led debug session on a
  manually mutated pool/instance.
- Kosmos Create now persists the resolved imageLabel on the NodeClaim as
  the `karpenter.scaleway.com/imagelabel-snapshot` annotation. This is
  the source of truth `IsDrifted` compares the current
  `NodeClass.Spec.ImageLabel` against.

### Changed

- `pkg/scaleway.KapsulePool` exposes the pool `Size` (`uint32`) — the
  drift check on Kapsule reads it. The struct continues to omit fields
  Karpenter does not consume.
- `pkg/scaleway.IsNotFound` is now exported. Backends use it to
  translate Scaleway 404s into "not drift" without re-coding the SDK
  error matching pattern.

### Out of scope

- Drift on tags propagated from `NodeClass.Spec.Tags` (only the
  karpenter correlation tag is checked).
- Drift on `RootVolumeSizeGB` / `RootVolumeType` / `PublicIP` — the
  Scaleway Instance API does not expose these as readable post-create.
- Auto-repair of detected drift; Karpenter handles replacement.

## [0.2.0] — 2026-05-05 (*Multi-zone & dynamic catalog*)

### Breaking

- `ScalewayNodeClass.spec.zone` (string, required) **removed**, replaced
  by `spec.zones` (`[]string`, optional whitelist). Existing manifests
  must migrate `spec.zone: "X"` → `spec.zones: ["X"]`. Empty / omitted
  means "all 9 Scaleway zones". Apply the new CRD before `helm upgrade`:
  ```sh
  kubectl apply -f charts/karpenter-scaleway/crds/karpenter.scaleway.com_scalewaynodeclasses.yaml
  ```
- Helm value `scaleway.zone` **removed**; the controller is zone-agnostic
  since this release. The `SCW_DEFAULT_ZONE` ConfigMap key is no longer
  rendered.

### Added

- Generated instance type catalog: 90 types across 9 Scaleway zones
  (15 ARM, GP1/PRO2/PLAY2/POP2/COMPUTE3/STANDARD3/MEMORY3/BASIC2/3
  families), built at release time via `make generate-catalog` from the
  live `instance.ListServersTypes` API and embedded into the binary.
  Filters out `EndOfService`, GPU types, and the `STARDUST` family.
- Live pricing refresh every 12h ([`pkg/pricing.Provider`](pkg/pricing/provider.go))
  aligned with `karpenter-provider-azure`. Embedded baseline from the
  generated catalog is the fallback on API failure.
- Metrics:
  - `karpenter_scaleway_nodeclaim_provisioning_duration_seconds{backend, zone, outcome}`
    histogram (buckets 10s..600s).
  - `karpenter_scaleway_pricing_last_update_seconds_ago` gauge.
  - `karpenter_scaleway_pricing_refresh_total{outcome}` counter.
- ARM (`BASIC2-A*`, `STANDARD2-A*`) instance types supported in scheduling.
- `cmd/generate-catalog`: build-time tool that scans every Scaleway zone,
  applies the denylist, and writes a deterministic `pkg/instancetype/catalog.json`.
- `Makefile` with `build`, `test`, `lint`, `generate-catalog`, `generate-crd`.

### Changed

- [`pkg/scaleway`](pkg/scaleway/) clients are now zone-agnostic — zone is
  passed per-call instead of fixed at construction. `NewClient`,
  `NewKapsuleClient` signatures are simpler.
- `Backend.Create` on both Kapsule and Kosmos honors the
  `topology.kubernetes.io/zone` requirement chosen by Karpenter, then
  guards it against the NodeClass whitelist.
- `Backend.GetInstanceTypes` filters the embedded catalog by the
  NodePool requirements and the NodeClass `spec.zones`, then hydrates
  offering prices through the pricing provider.
- NodeClass reconciler validates `spec.zones` against the catalog and
  surfaces `Status.ResolvedZones`. Unknown zones produce
  `Reason=ZoneUnknown`.
- `cmd/controller`: pricing provider boots before the manager and its
  refresh loop is registered via `mgr.Add(manager.RunnableFunc(...))`,
  so a single replica polls Scaleway every 12h after leader election.

### NFR

- Provisioning P95 target: **<120s** on both backends (relaxed from the
  earlier <60s/<90s split). The earlier targets were below the Scaleway
  API floor itself; the new gate is tracked live via the provisioning
  histogram.

### Out of scope

- Drift detection (shipped in 0.3.0).
- Preemptible / capacity types.
- Kosmos Helm bootstrap.
- Karpenter `karpenter.sh/unregistered` taint (shipped in 0.4.7).
- Drift-churn investigation.

## [0.1.0] — 2026-05-03 (*Foundations*)

Initial productisation release. v1alpha1 CRD, Backend interface,
NodeClass reconciler with Marketplace round-trip, Helm chart skeleton.
Mono-zone (`fr-par-1`), hardcoded instance type catalog, no drift
detection (parity with the initial prototype).
