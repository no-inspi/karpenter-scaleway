package main

import (
	"sort"

	"github.com/no-inspi/karpenter-scaleway/pkg/instancetype"
)

type catalogDiff struct {
	added   []string
	removed []string
}

func compareCatalogs(old, new *instancetype.Catalog) catalogDiff {
	o := nameSet(old)
	n := nameSet(new)
	var d catalogDiff
	for name := range n {
		if _, ok := o[name]; !ok {
			d.added = append(d.added, name)
		}
	}
	for name := range o {
		if _, ok := n[name]; !ok {
			d.removed = append(d.removed, name)
		}
	}
	sort.Strings(d.added)
	sort.Strings(d.removed)
	return d
}

func nameSet(c *instancetype.Catalog) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range c.Types {
		out[t.Name] = struct{}{}
	}
	return out
}
