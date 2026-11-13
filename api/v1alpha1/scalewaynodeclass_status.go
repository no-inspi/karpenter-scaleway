package v1alpha1

import (
	"github.com/awslabs/operatorpkg/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Reason values reported on the Ready condition. They are stable strings
// consumers (operators, dashboards, alerts) may match on — do not rename.
const (
	// ConditionReasonReady marks a NodeClass that passed all validations.
	ConditionReasonReady = "Ready"

	// ConditionReasonImageNotFound is set when the Marketplace round-trip
	// returns no image for the configured zone/imageLabel pair.
	ConditionReasonImageNotFound = "ImageNotFound"

	// ConditionReasonValidationPending is set when the Marketplace API is
	// unreachable or returns a transient error; the reconciler will retry.
	ConditionReasonValidationPending = "ValidationPending"

	// ConditionReasonInvalidSpec is a defensive fallback for cases the
	// kubebuilder OpenAPI validation should already block.
	ConditionReasonInvalidSpec = "InvalidSpec"

	// ConditionReasonReconciling is the transient state on the very first
	// reconcile, before any backend round-trip has completed.
	ConditionReasonReconciling = "Reconciling"

	// ConditionReasonZoneUnknown is set when one of spec.zones references a
	// zone with no offering in the embedded catalog.
	ConditionReasonZoneUnknown = "ZoneUnknown"
)

// ScalewayNodeClassStatus mirrors ScalewayNodeClass status subresource shape.
type ScalewayNodeClassStatus struct {
	// Conditions exposes the readiness of the NodeClass. Always contains a
	// Ready condition once the reconciler has run at least once.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []status.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// UnusedFieldsForBackend lists spec fields the resolved backend ignores.
	// Visible in `kubectl describe` to surface configuration mistakes early.
	// +optional
	UnusedFieldsForBackend []string `json:"unusedFieldsForBackend,omitempty"`

	// ValidatedAt is the last time the spec was successfully validated
	// against the Scaleway API. Useful for cache-debug.
	// +optional
	ValidatedAt *metav1.Time `json:"validatedAt,omitempty"`

	// ResolvedImageID is the Scaleway image UUID resolved from ImageLabel.
	// Populated whenever Ready=True.
	// +optional
	ResolvedImageID string `json:"resolvedImageID,omitempty"`

	// ResolvedZones is the effective list of zones eligible for this
	// NodeClass: spec.zones if non-empty, otherwise the 9 default zones.
	// +optional
	ResolvedZones []string `json:"resolvedZones,omitempty"`
}

// StatusConditions exposes the operatorpkg condition set for the karpenter
// status reconciler. Ready is the only documented top-level condition.
func (in *ScalewayNodeClass) StatusConditions() status.ConditionSet {
	return status.NewReadyConditions().For(in)
}

// GetConditions implements operatorpkg/status.Object.
func (in *ScalewayNodeClass) GetConditions() []status.Condition {
	return in.Status.Conditions
}

// SetConditions implements operatorpkg/status.Object.
func (in *ScalewayNodeClass) SetConditions(conditions []status.Condition) {
	in.Status.Conditions = conditions
}
