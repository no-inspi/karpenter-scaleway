// Package drift defines the cross-backend drift contract — DriftReason
// values returned by IsDrifted and the per-NodeClaim opt-out annotation.
//
// Lives in its own package because pkg/backend imports the concrete
// kapsule/kosmos backends through factory.go; defining these symbols in
// pkg/backend would create an import cycle when those backends reference
// them.
package drift

import karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"

// SkipAnnotationKey is the per-NodeClaim escape hatch that disables
// cloudprovider-side drift detection. Operators set it to "true" when they
// have intentionally diverged the underlying Scaleway resource from the
// NodeClass spec (e.g. a manually resized Kapsule pool kept around for
// debugging) and don't want Karpenter to recycle the NodeClaim.
//
// Karpenter's static-fields/requirements drift checks still fire — only
// the backend's own checks are skipped.
const SkipAnnotationKey = "karpenter.scaleway.com/skip-drift"

// ImageLabelSnapshotAnnotationKey records the NodeClass.Spec.ImageLabel
// value at NodeClaim Create time. IsDrifted compares the live NodeClass
// spec against this snapshot to detect a post-creation imageLabel change
// (the canonical "NodeClass changed after creation" drift case).
//
// Kosmos only — the Kapsule backend ignores Spec.ImageLabel (the Scaleway
// K8s control plane chooses the image internally).
const ImageLabelSnapshotAnnotationKey = "karpenter.scaleway.com/imagelabel-snapshot"

// DriftReason values returned by the Scaleway backends. Stable strings:
// Karpenter writes them onto NodeClaim.status.conditions[Drifted].
const (
	// ReasonImageLabel — NodeClass.Spec.ImageLabel changed since the
	// NodeClaim was created (Kosmos only).
	ReasonImageLabel karpcp.DriftReason = "ImageLabelDrift"

	// ReasonInstanceType — the live Scaleway resource's type no longer
	// matches the NodeClaim's instance-type label. Possible only via
	// out-of-band edits (Karpenter doesn't mutate existing instances).
	ReasonInstanceType karpcp.DriftReason = "InstanceTypeDrift"

	// ReasonZone — the live resource's zone no longer matches the
	// NodeClaim's topology label.
	ReasonZone karpcp.DriftReason = "ZoneDrift"

	// ReasonPoolResize — Kapsule only. The pool is no longer of size 1:
	// an operator scaled it manually outside Karpenter.
	ReasonPoolResize karpcp.DriftReason = "PoolResizeDrift"

	// ReasonTags — the karpenter correlation tag (`nodeclaim=<name>`) has
	// been stripped from the live resource. We can no longer prove it
	// belongs to this NodeClaim, so we fail safe and recycle.
	ReasonTags karpcp.DriftReason = "TagsDrift"
)
