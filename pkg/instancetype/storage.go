package instancetype

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// DefaultRootVolumeSizeGB is the root volume size assumed when the
// NodeClass leaves spec.rootVolumeSizeGB unset. Kapsule creates PRO2/POP2
// nodes with a 20 GB sbs_5k root volume by default (measured: the node
// reports ~18.2 GB of ephemeral-storage capacity).
const DefaultRootVolumeSizeGB int32 = 20

const (
	// usableRootVolumePercent is the share of the root volume reported as
	// ephemeral-storage capacity. The rest goes to partitioning and the
	// filesystem: a 20 GB volume reports ~18.2 GB, a 50 GB one ~47.3 GB.
	// 90% stays below both, so Karpenter never sees more disk than the
	// kubelet does.
	usableRootVolumePercent = 90
	// evictionRootVolumePercent mirrors the kubelet's default hard eviction
	// threshold nodefs.available<10%.
	evictionRootVolumePercent = 10
)

// ApplyRootVolume sets the ephemeral-storage capacity and eviction threshold
// of each instance type from the root volume size the node will get. A nil
// or non-positive size falls back to DefaultRootVolumeSizeGB.
//
// Without an ephemeral-storage capacity Karpenter treats any pod that
// requests ephemeral-storage as unschedulable on every instance type, and it
// cannot bin-pack disk-heavy pods. The types are modified in place: callers
// pass the fresh slice returned by Filter.
func ApplyRootVolume(types []*cloudprovider.InstanceType, sizeGB *int32) {
	size := DefaultRootVolumeSizeGB
	if sizeGB != nil && *sizeGB > 0 {
		size = *sizeGB
	}
	bytes := int64(size) * 1_000_000_000
	capacity := resource.NewQuantity(bytes*usableRootVolumePercent/100, resource.DecimalSI)
	eviction := resource.NewQuantity(capacity.Value()*evictionRootVolumePercent/100, resource.DecimalSI)
	for _, t := range types {
		if t.Capacity == nil {
			t.Capacity = corev1.ResourceList{}
		}
		t.Capacity[corev1.ResourceEphemeralStorage] = capacity.DeepCopy()
		if t.Overhead == nil {
			t.Overhead = &cloudprovider.InstanceTypeOverhead{}
		}
		if t.Overhead.EvictionThreshold == nil {
			t.Overhead.EvictionThreshold = corev1.ResourceList{}
		}
		t.Overhead.EvictionThreshold[corev1.ResourceEphemeralStorage] = eviction.DeepCopy()
	}
}
