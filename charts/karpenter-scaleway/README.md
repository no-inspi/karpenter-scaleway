# karpenter-scaleway

Helm chart for the Karpenter cloudprovider for Scaleway-managed Kubernetes
(Kapsule and Kosmos). Auto-detects the cluster type at boot via
`k8s.GetCluster`; can be overridden with `--set forceBackend=kapsule|kosmos`.

## Install

```sh
helm install karpenter-scaleway ./charts/karpenter-scaleway \
  --namespace karpenter --create-namespace \
  --set scaleway.accessKey=$SCW_ACCESS_KEY \
  --set scaleway.secretKey=$SCW_SECRET_KEY \
  --set scaleway.organizationID=$SCW_DEFAULT_PROJECT_ID \
  --set scaleway.clusterID=$SCW_CLUSTER_ID \
  --set scaleway.region=fr-par
```

### Install from the OCI registry

Released charts are published to the Scaleway Container Registry:

```sh
helm install karpenter-scaleway \
  oci://rg.fr-par.scw.cloud/karpenter-scaleway/charts/karpenter-scaleway \
  --version 0.4.8 \
  --namespace karpenter --create-namespace \
  --set scaleway.accessKey=$SCW_ACCESS_KEY \
  --set scaleway.secretKey=$SCW_SECRET_KEY \
  --set scaleway.organizationID=$SCW_DEFAULT_PROJECT_ID \
  --set scaleway.clusterID=$SCW_CLUSTER_ID \
  --set scaleway.region=fr-par
```

Since v0.2.0 the controller is zone-agnostic — the zone is picked per
NodeClaim from the Karpenter scheduler. Restrict the allowed zones via
`ScalewayNodeClass.spec.zones`; empty means "all 9 Scaleway zones".

For a Kosmos cluster, additionally pass:

```sh
  --set scaleway.kosmos.poolID=$SCW_KOSMOS_POOL_ID \
  --set scaleway.kosmos.apiKey=$SCW_KOSMOS_API_KEY
```

## Values reference

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `2` | Number of controller replicas (HA via leader election). |
| `image.repository` | `rg.fr-par.scw.cloud/karpenter-scaleway/karpenter-provider-scaleway` | OCI image. |
| `image.tag` | `""` | Tag (defaults to `appVersion`). |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | |
| `scaleway.accessKey` | `""` | IAM access key. Required if `existingSecret` is empty. |
| `scaleway.secretKey` | `""` | IAM secret key. Required if `existingSecret` is empty. |
| `scaleway.organizationID` | `""` | Organization ID. Fallback for `projectID` when the latter is empty. |
| `scaleway.projectID` | `""` | Project the controller creates resources in. **Required when the cluster is not in the organization's default project**; must be one the IAM key is authorized for. Defaults to `organizationID`. |
| `scaleway.clusterID` | `""` | Scaleway-managed cluster UUID. |
| `scaleway.region` | `""` | e.g. `fr-par`. |
| `scaleway.existingSecret` | `""` | Pre-provisioned Secret with `SCW_ACCESS_KEY`/`SCW_SECRET_KEY`. |
| `scaleway.kosmos.poolID` | `""` | Kosmos seed pool ID. Required for Kosmos. |
| `scaleway.kosmos.apiKey` | `""` | Kosmos seed IAM key. Required for Kosmos. |
| `scaleway.kosmos.agentURL` | `""` | Override the k8s-agent download URL. |
| `forceBackend` | `""` | `""` = auto, `"kapsule"`, `"kosmos"`. |
| `logLevel` | `info` | |
| `leaderElection.enabled` | `true` | |
| `metrics.port` | `8080` | |
| `probes.port` | `8081` | |
| `resources.requests` | 100m / 128Mi | |
| `resources.limits` | 500m / 512Mi | |
| `serviceMonitor.enabled` | `false` | Render a Prometheus Operator `ServiceMonitor` for the metrics port. |
| `serviceMonitor.labels` | `{}` | Extra labels (match your Prometheus `serviceMonitorSelector`). |
| `serviceMonitor.namespace` | `""` | Defaults to the release namespace. |
| `serviceMonitor.interval` / `scrapeTimeout` | `30s` / `10s` | |
| `serviceMonitor.relabelings` / `metricRelabelings` | `[]` | Passed through to the endpoint. |
| `prometheusRule.enabled` | `false` | Render a `PrometheusRule` with the alerts below. |
| `prometheusRule.labels` | `{}` | Extra labels (match your Prometheus `ruleSelector`). |
| `prometheusRule.namespace` | `""` | Defaults to the release namespace. |
| `prometheusRule.additionalAlertLabels` | `{}` | Labels merged into every alert. |
| `prometheusRule.disabled` | `[]` | Alert names to skip. |
| `prometheusRule.thresholds.*` | see `values.yaml` | Alert thresholds. |
| `prometheusRule.additionalRules` | `[]` | Extra raw rules appended to the group. |
| `grafanaDashboard.enabled` | `false` | Ship `dashboards/karpenter-scaleway.json` as a ConfigMap for the Grafana sidecar. |
| `grafanaDashboard.labels` | `grafana_dashboard: "1"` | Sidecar discovery label. |
| `grafanaDashboard.namespace` | `""` | Defaults to the release namespace. |

## Monitoring

The controller serves Prometheus metrics on `metrics.port` (`/metrics`,
plain HTTP), exposed by the chart's Service under the `metrics` port name.
The `ServiceMonitor` and `PrometheusRule` need the Prometheus Operator CRDs
(`monitoring.coreos.com/v1`, e.g. kube-prometheus-stack):

```sh
helm upgrade karpenter-scaleway ./charts/karpenter-scaleway --reuse-values \
  --set serviceMonitor.enabled=true \
  --set serviceMonitor.labels.release=kube-prometheus-stack \
  --set prometheusRule.enabled=true \
  --set prometheusRule.labels.release=kube-prometheus-stack \
  --set grafanaDashboard.enabled=true
```

The alerts select series on `job="<release fullname>"` and
`namespace="<release namespace>"`, which is what the bundled `ServiceMonitor`
produces. If you scrape the controller some other way, keep these two labels
(or don't relabel `job`), otherwise those alerts silently match nothing.
`KarpenterScalewayControllerDown` is an `absent(up{...})` alert, so it is only
rendered when `serviceMonitor.enabled=true`; without the bundled scrape it
would fire permanently.

| Alert | Severity | Fires when |
|---|---|---|
| `KarpenterScalewayControllerDown` | critical | No replica scraped with `up == 1` for 5m (only rendered with `serviceMonitor.enabled`). |
| `KarpenterScalewayNoLeader` | critical | Replicas are up but none holds the leader lease for 10m (only rendered with `leaderElection.enabled`). |
| `KarpenterScalewayNodeClaimCreationErrors` | warning | ≥ `thresholds.creationErrors` creations with `outcome=fail\|rejected` over `thresholds.creationErrorsWindow` (`karpenter_scaleway_nodeclaim_provisioning_duration_seconds`). `rejected` = any preflight refusal (NodeClass not found, zone not allowed, Kapsule quota threshold); `fail` = Scaleway call or wait-for-Ready failed. |
| `KarpenterScalewayPoolQuotaNearLimit` | warning | `karpenter_scaleway_kapsule_pool_quota_remaining` below `thresholds.poolQuotaRemaining` (Kapsule only). |
| `KarpenterScalewayPoolQuotaRejections` | warning | `karpenter_scaleway_kapsule_pool_quota_rejections_total` increased in the last 15m (held 5m): Creates are being refused by the quota threshold. |
| `KarpenterScalewayNodeClaimNotReady` | warning | A NodeClaim has had `Ready != True` for more than `thresholds.nodeClaimNotReadySeconds` (`operator_nodeclaim_status_condition_current_status_seconds`). |
| `KarpenterScalewayPricingStale` | warning | `karpenter_scaleway_pricing_last_update_seconds_ago` on the leader above `thresholds.pricingStaleSeconds`. |

Most gauges are only kept up to date by the leader: standby replicas export
`pricing_last_update_seconds_ago = +Inf` and `kapsule_pool_quota_remaining = 0`.
The rules account for this (leader filter / `kapsule_pools_total` presence),
so take the same care in any custom query.

The dashboard (`dashboards/karpenter-scaleway.json`, also importable by hand
in Grafana) shows Karpenter-managed nodes per nodepool and instance type, the
estimated hourly cost (`karpenter_nodepools_cost_total`, EUR/h from Scaleway
list prices, not a billing source), disruption events and decisions,
Create outcomes and latency, Kapsule pool usage and pricing freshness.

## RBAC

The chart creates a `ClusterRole` (nodes, pods, NodeClaim/NodePool/ScalewayNodeClass)
and a namespace-scoped `Role` (leases, secrets, configmaps in the release
namespace). No wildcards — see [templates/rbac.yaml](templates/rbac.yaml).

## CRDs

`crds/karpenter.scaleway.com_scalewaynodeclasses.yaml` ships with the chart
and is installed by Helm 3 on first `helm install`. Helm does **not** upgrade
CRDs on `helm upgrade`. **v0.1.0 → v0.2.0 upgrade**: `spec.zone` (string,
required) is replaced by `spec.zones` (`[]string`, optional). Apply the new
CRD before running `helm upgrade`:

```sh
kubectl apply -f charts/karpenter-scaleway/crds/karpenter.scaleway.com_scalewaynodeclasses.yaml
# Migrate every existing NodeClass:  spec.zone: "X"  →  spec.zones: ["X"]
```
