package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// LabelCSIZone is the zone topology key the Scaleway Block Storage CSI
// driver writes into PersistentVolume node affinity.
const LabelCSIZone = "topology.csi.scaleway.com/zone"

func init() {
	// Karpenter translates pod requirements through NormalizedLabels before
	// matching them against instance types. Without this alias, a pod that
	// mounts a Scaleway Block Storage volume carries a requirement on a key
	// no instance type offers, so Karpenter can neither provision a node for
	// it nor replace (drift, expiry) the node it runs on.
	karpv1.NormalizedLabels[LabelCSIZone] = corev1.LabelTopologyZone
}
