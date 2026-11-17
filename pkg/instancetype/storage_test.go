package instancetype

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestApplyRootVolume_DefaultSize(t *testing.T) {
	types := Filter(&karpv1.NodePool{}, nil)
	if len(types) == 0 {
		t.Fatal("empty catalog")
	}
	ApplyRootVolume(types, nil)
	for _, it := range types {
		got := it.Capacity[corev1.ResourceEphemeralStorage]
		if want := resource.MustParse("18G"); got.Cmp(want) != 0 {
			t.Fatalf("%s: ephemeral-storage capacity = %s, want %s", it.Name, got.String(), want.String())
		}
		ev := it.Overhead.EvictionThreshold[corev1.ResourceEphemeralStorage]
		if want := resource.MustParse("1.8G"); ev.Cmp(want) != 0 {
			t.Fatalf("%s: ephemeral-storage eviction = %s, want %s", it.Name, ev.String(), want.String())
		}
		// Memory eviction threshold must survive.
		if _, ok := it.Overhead.EvictionThreshold[corev1.ResourceMemory]; !ok {
			t.Fatalf("%s: memory eviction threshold dropped", it.Name)
		}
	}
}

// Allocatable must stay below what a Kapsule node really reports: a 50 GB
// root volume gives a node with ~42.5 GB allocatable ephemeral-storage.
func TestApplyRootVolume_ExplicitSizeBelowKubeletAllocatable(t *testing.T) {
	types := Filter(&karpv1.NodePool{}, nil)
	size := int32(50)
	ApplyRootVolume(types, &size)
	alloc := types[0].Allocatable()[corev1.ResourceEphemeralStorage]
	if want := resource.MustParse("40.5G"); alloc.Cmp(want) != 0 {
		t.Fatalf("allocatable ephemeral-storage = %s, want %s", alloc.String(), want.String())
	}
	if kubelet := resource.MustParse("42579438727"); alloc.Cmp(kubelet) >= 0 {
		t.Fatalf("allocatable %s not below the kubelet's %s", alloc.String(), kubelet.String())
	}
}

// Filter returns fresh objects: applying a size to one result must not leak
// into the next call.
func TestApplyRootVolume_DoesNotMutateCatalog(t *testing.T) {
	size := int32(200)
	ApplyRootVolume(Filter(&karpv1.NodePool{}, nil), &size)
	for _, it := range Filter(&karpv1.NodePool{}, nil) {
		if _, ok := it.Capacity[corev1.ResourceEphemeralStorage]; ok {
			t.Fatalf("%s: ephemeral-storage leaked into a fresh Filter result", it.Name)
		}
	}
}
