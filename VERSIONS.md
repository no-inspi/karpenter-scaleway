# Pinned versions

| Component | Version | Rationale |
|---|---|---|
| Go | 1.26.2 | installed on dev machine, exceeds plan minimum 1.23 |
| sigs.k8s.io/karpenter | v1.10.0 | latest stable v1.10.x line at spike start (verify before `go get` and update if a newer patch v1.10.x exists) |
| sigs.k8s.io/controller-runtime | v0.22.4 | matches karpenter v1.10.0 go.mod requirement (v0.23.x breaks on client-go interface change) |
| k8s.io/api, apimachinery, client-go | v0.35.0 | matches karpenter v1.10.0 go.mod requirement (v0.36 breaks controller-runtime cache types) |
| github.com/scaleway/scaleway-sdk-go | v1.0.0-beta.36 | resolved by `go get` at module bootstrap |
| scaleway/k8s-agent | v0.1.5 | latest release at spike start (2026-04-02); binary `k8s-agent_linux_amd64` from GitHub release assets |
| Kubernetes (Kosmos) | 1.34.0 | supported by Scaleway Kosmos and compatible with k8s-agent |
| Kubernetes (Kapsule) | 1.34.0 | Per `test/e2e/terraform/kapsule/variables.tf` default; CI overrides via -var if needed. |
| Terraform | 1.5.7 | installed on dev machine; main.tf will require ">= 1.5" (no 1.6-specific features used) |
| scaleway/scaleway provider | ~> 2.40 | latest 2.x, supports Kosmos cluster type and external pool |

This file is the source of truth for versions. Update it whenever a version is locked or upgraded during implementation.

## Releases

### v0.1.0 — 2026-05-03 (*Foundations*)

- CRD `karpenter.scaleway.com/v1alpha1.ScalewayNodeClass` frozen — 6 spec
  fields (zone, imageLabel, tags, rootVolumeSizeGB, rootVolumeType,
  publicIP) + status conditions, `unusedFieldsForBackend`, `validatedAt`,
  `resolvedImageID`.
- `pkg/backend.Backend` interface (8 methods) + Kapsule and Kosmos
  implementations, factory dispatch by `Kind`.
- Auto-detection via `k8s.GetCluster` with `SCW_FORCE_BACKEND` kill-switch.
- NodeClass reconciler with 5-minute Marketplace cache.
- Single Helm chart `charts/karpenter-scaleway` works for both backends.
- Single binary `cmd/controller`; `cmd/kapsule-controller` removed.
- Hardcoded instance type catalog (parity with the spike).