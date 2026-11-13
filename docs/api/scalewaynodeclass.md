# `ScalewayNodeClass` (v1alpha1)

`ScalewayNodeClass` describes how Karpenter should provision Scaleway
instances. It is a cluster-scoped CRD shared by both Kapsule and Kosmos
backends — fields irrelevant to the resolved backend are silently ignored
and surfaced under `status.unusedFieldsForBackend`.

## API group

```
group: karpenter.scaleway.com
version: v1alpha1
kind: ScalewayNodeClass
shortNames: scwnc
scope: Cluster
```

## Spec

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `zone` | string | yes | — | Scaleway zone (`fr-par-1`, `nl-ams-2`, …). Validated by an OpenAPI enum. |
| `imageLabel` | string | yes | — | Marketplace label (e.g. `ubuntu_jammy`) or a UUID. Validated at reconcile time via the Scaleway Marketplace API; failure surfaces as `Ready=False`/`ImageNotFound`. |
| `tags` | map[string]string | no | `{}` | Up to 50 entries. Encoded as `key=value` Scaleway tags on the underlying Instance (Kosmos) or Pool (Kapsule). |
| `rootVolumeSizeGB` | *int32 | no | — | 10 ≤ size ≤ 10000. **Kosmos only** — silently ignored on Kapsule. |
| `rootVolumeType` | string | no | — | One of `l_ssd`, `sbs_volume`. **Kosmos only**. `b_ssd` was deprecated by Scaleway in 2026 (Block Storage v1 retired) and is rejected by the API. |
| `publicIP` | *bool | no | — (Scaleway default true) | **Kosmos only**. The k8s-agent registration requires Internet reachability, so leaving this `true` is the safe default. |

## Status

| Field | Type | Notes |
|---|---|---|
| `conditions` | []metav1.Condition | Always contains a `Ready` condition. |
| `unusedFieldsForBackend` | []string | Spec fields the resolved backend ignores (e.g. `rootVolumeSizeGB` on Kapsule). |
| `validatedAt` | metav1.Time | Last successful Marketplace round-trip. |
| `resolvedImageID` | string | Scaleway image UUID resolved from `imageLabel`. |

### Ready conditions

| `status` | `reason` | When |
|---|---|---|
| `True` | `Ready` | Spec is valid; Marketplace returned a UUID. |
| `False` | `ImageNotFound` | `imageLabel` does not resolve in `zone`. |
| `False` | `ValidationPending` | Marketplace API returned 5xx; the controller retries every 10 s. |
| `False` | `InvalidSpec` | Defensive — should be unreachable since OpenAPI rejects malformed specs. |
| `Unknown` | `Reconciling` | Transient state on the very first reconcile. |

## Example

```yaml
apiVersion: karpenter.scaleway.com/v1alpha1
kind: ScalewayNodeClass
metadata:
  name: default
spec:
  zone: fr-par-1
  imageLabel: ubuntu_jammy
  tags:
    team: platform
    env: prod
  rootVolumeSizeGB: 50
  rootVolumeType: sbs_volume
  publicIP: true
```

## Compatibility

- Adding optional fields in v1alpha1 is non-breaking.
- Removing or renaming any of the 6 spec fields requires a v1alpha2 bump.
- The kubebuilder `Required` markers on `zone` and `imageLabel` are part
  of the public surface — relaxing them is breaking.

## Out of scope for the initial v1alpha1

| Future field | Why |
|---|---|
| `zones []string` | Multi-zone scheduling (shipped in v0.2.0 as `spec.zones`). |
| `instanceTypeSelector` | Replace the hardcoded catalog. |
| `kubeletConfig` | Custom kubelet flags. |
| `userData` | Pre/post bootstrap scripts. |
