package instancetype

import (
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// internalEntry is the unexported intermediate produced by the predicate
// pass; the hydration pass turns it back into a cloudprovider.InstanceType.
// Tests in the same package access it; external callers use Filter.
type internalEntry struct {
	Name      string
	Arch      string
	Offerings []Offering
}

// Filter returns the embedded catalog filtered by NodePool requirements
// (zone, arch, instance-type, capacity-type) AND optionally the NodeClass
// spec.zones whitelist. Types with zero remaining offerings are dropped.
func Filter(np *karpv1.NodePool, allowedZones []string) []*cloudprovider.InstanceType {
	c := load()
	pruned := filterCatalog(c, np, allowedZones)
	out := make([]*cloudprovider.InstanceType, 0, len(pruned))
	for _, t := range pruned {
		ce := findFullEntry(c, t.Name)
		if ce == nil {
			continue
		}
		entry := *ce
		entry.Offerings = t.Offerings
		out = append(out, hydrateOne(&entry))
	}
	return out
}

func filterCatalog(c *Catalog, np *karpv1.NodePool, allowedZones []string) []internalEntry {
	zoneAllowed := zoneFilter(np, allowedZones)
	archAllowed := stringInFilter(np, corev1.LabelArchStable)
	typeAllowed := stringInFilter(np, corev1.LabelInstanceTypeStable)
	capAllowed := stringInFilter(np, karpv1.CapacityTypeLabelKey)

	out := make([]internalEntry, 0, len(c.Types))
	for _, t := range c.Types {
		if !archAllowed(t.Arch) || !typeAllowed(t.Name) {
			continue
		}
		var keepOffers []Offering
		for _, o := range t.Offerings {
			if zoneAllowed(o.Zone) && capAllowed(o.CapacityType) {
				keepOffers = append(keepOffers, o)
			}
		}
		if len(keepOffers) == 0 {
			continue
		}
		out = append(out, internalEntry{Name: t.Name, Arch: t.Arch, Offerings: keepOffers})
	}
	return out
}

// zoneFilter intersects (NodePool topology.kubernetes.io/zone In/NotIn) with
// the optional NodeClass whitelist. Both empty = accept all.
func zoneFilter(np *karpv1.NodePool, whitelist []string) func(string) bool {
	npFn := stringInFilter(np, corev1.LabelTopologyZone)
	if len(whitelist) == 0 {
		return npFn
	}
	wlSet := map[string]struct{}{}
	for _, z := range whitelist {
		wlSet[z] = struct{}{}
	}
	return func(z string) bool {
		_, ok := wlSet[z]
		return ok && npFn(z)
	}
}

// stringInFilter returns a predicate matching a value against the
// requirement on the given label key. Absence of requirement = accept all.
// NotIn is supported. Op other than In/NotIn = accept all. If the same key
// appears more than once, only the first requirement is applied — multiple
// same-key requirements are not intersected.
func stringInFilter(np *karpv1.NodePool, key string) func(string) bool {
	if np == nil {
		return func(string) bool { return true }
	}
	for _, r := range np.Spec.Template.Spec.Requirements {
		if r.Key != key {
			continue
		}
		set := map[string]struct{}{}
		for _, v := range r.Values {
			set[v] = struct{}{}
		}
		switch r.Operator {
		case corev1.NodeSelectorOpIn:
			return func(v string) bool { _, ok := set[v]; return ok }
		case corev1.NodeSelectorOpNotIn:
			return func(v string) bool { _, ok := set[v]; return !ok }
		default:
			return func(string) bool { return true }
		}
	}
	return func(string) bool { return true }
}

func findFullEntry(c *Catalog, name string) *TypeEntry {
	for i := range c.Types {
		if c.Types[i].Name == name {
			return &c.Types[i]
		}
	}
	return nil
}

func hydrateOne(t *TypeEntry) *cloudprovider.InstanceType {
	all := hydrate(&Catalog{Types: []TypeEntry{*t}})
	if len(all) == 0 {
		return nil
	}
	return all[0]
}
