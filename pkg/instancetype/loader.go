package instancetype

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

//go:embed catalog.json
var catalogJSON []byte

var (
	loadOnce sync.Once
	loaded   *Catalog
	loadErr  error
)

// load parses the embedded catalog.json once. Panics on parse failure
// (an invalid embedded file is a programmer bug, not a runtime condition).
//
// The panic lives outside the sync.Once.Do closure so subsequent callers
// also fail loudly — sync.Once marks the call done on panic in the closure,
// which would leave `loaded` nil and produce nil-pointer dereferences in
// hydrate / filterCatalog instead of a clear diagnostic.
func load() *Catalog {
	loadOnce.Do(func() {
		loaded, loadErr = parseCatalog(catalogJSON)
	})
	if loadErr != nil {
		panic(fmt.Sprintf("instancetype: parse embedded catalog: %v", loadErr))
	}
	return loaded
}

func parseCatalog(data []byte) (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("unmarshal catalog: %w", err)
	}
	return &c, nil
}

// All returns every TypeEntry hydrated as a Karpenter cloudprovider.InstanceType
// with the embedded baseline price already in each Offering. Callers that
// need live pricing must run pricing.Provider.Hydrate on top.
func All() []*cloudprovider.InstanceType {
	return hydrate(load())
}

// Default node-level reservations applied to every instance type. Karpenter
// computes Allocatable = Capacity - Overhead, so a zero overhead inflates
// the schedulable surface and risks tight-limit workloads being evicted
// when the kubelet + system daemons claim their share.
//
// These are conservative single-source-of-truth values for Kosmos
// cloud-init worker nodes (Kapsule pools come with their own kubelet
// reservations managed by the pool itself, but Karpenter binpacks against
// the values we report here, so the same overhead is applied uniformly —
// having Kapsule under-report its true allocatable is safer than over-
// scheduling). Calibration source: kubelet defaults.
//
// Total ≈ 160m CPU + 484Mi memory. Sits inside the 150–300m / 256–512Mi
// range from the finding. Per-family tuning is future work; one constant
// is good enough until empirical pressure shows otherwise.
var (
	defaultKubeReserved = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("100m"),
		corev1.ResourceMemory: resource.MustParse("256Mi"),
	}
	defaultSystemReserved = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("60m"),
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	}
	defaultEvictionThreshold = corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("100Mi"),
	}
)

func hydrate(c *Catalog) []*cloudprovider.InstanceType {
	out := make([]*cloudprovider.InstanceType, 0, len(c.Types))
	for _, t := range c.Types {
		offers := make(cloudprovider.Offerings, 0, len(t.Offerings))
		for _, o := range t.Offerings {
			offers = append(offers, &cloudprovider.Offering{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, o.CapacityType),
					scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, o.Zone),
				),
				Price:     o.PriceEURhr,
				Available: o.Available,
			})
		}
		out = append(out, &cloudprovider.InstanceType{
			Name: t.Name,
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, t.Name),
				scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, t.Arch),
				scheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, "linux"),
			),
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(t.CPUMillicores, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(int64(t.MemoryBytes), resource.BinarySI),
				corev1.ResourcePods:   *resource.NewQuantity(t.PodsPerNode, resource.DecimalSI),
			},
			Overhead: &cloudprovider.InstanceTypeOverhead{
				KubeReserved:      defaultKubeReserved.DeepCopy(),
				SystemReserved:    defaultSystemReserved.DeepCopy(),
				EvictionThreshold: defaultEvictionThreshold.DeepCopy(),
			},
			Offerings: offers,
		})
	}
	return out
}
