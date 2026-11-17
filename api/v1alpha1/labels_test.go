package v1alpha1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

// A PV node affinity on the Scaleway CSI zone key must match an instance
// type offering in that zone.
func TestCSIZoneLabelIsNormalized(t *testing.T) {
	pvAffinity := scheduling.NewRequirements(
		scheduling.NewRequirement(LabelCSIZone, corev1.NodeSelectorOpIn, "fr-par-1"),
	)
	if !pvAffinity.Has(corev1.LabelTopologyZone) {
		t.Fatalf("requirement keys = %v, want %s", pvAffinity.Keys().UnsortedList(), corev1.LabelTopologyZone)
	}
	offering := scheduling.NewRequirements(
		scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "fr-par-1"),
	)
	if err := offering.Compatible(pvAffinity); err != nil {
		t.Fatalf("fr-par-1 offering incompatible with CSI zone affinity: %v", err)
	}
	other := scheduling.NewRequirements(
		scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "nl-ams-1"),
	)
	if err := other.Compatible(pvAffinity); err == nil {
		t.Fatal("nl-ams-1 offering must not satisfy a fr-par-1 volume")
	}
}
