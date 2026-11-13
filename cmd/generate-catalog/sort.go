package main

import (
	"sort"

	"github.com/no-inspi/karpenter-scaleway/pkg/instancetype"
)

// sortDeterministic sorts types by Name asc and each type's offerings by
// (Zone, CapacityType) asc, in place. Required so json.Marshal output is
// stable across regenerations and produces clean git diffs.
func sortDeterministic(c *instancetype.Catalog) {
	sort.SliceStable(c.Types, func(i, j int) bool {
		return c.Types[i].Name < c.Types[j].Name
	})
	for i := range c.Types {
		offers := c.Types[i].Offerings
		sort.SliceStable(offers, func(a, b int) bool {
			if offers[a].Zone != offers[b].Zone {
				return offers[a].Zone < offers[b].Zone
			}
			return offers[a].CapacityType < offers[b].CapacityType
		})
	}
}
