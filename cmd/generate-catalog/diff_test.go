package main

import (
	"testing"

	"github.com/no-inspi/karpenter-scaleway/pkg/instancetype"
)

func mkCat(names ...string) *instancetype.Catalog {
	c := &instancetype.Catalog{}
	for _, n := range names {
		c.Types = append(c.Types, instancetype.TypeEntry{Name: n})
	}
	return c
}

func TestCompareCatalogs_AddedRemoved(t *testing.T) {
	old := mkCat("A", "B", "C")
	new := mkCat("A", "C", "D")
	d := compareCatalogs(old, new)
	if len(d.added) != 1 || d.added[0] != "D" {
		t.Fatalf("added: %v", d.added)
	}
	if len(d.removed) != 1 || d.removed[0] != "B" {
		t.Fatalf("removed: %v", d.removed)
	}
}

func TestCompareCatalogs_NoChange(t *testing.T) {
	d := compareCatalogs(mkCat("A", "B"), mkCat("A", "B"))
	if len(d.added) != 0 || len(d.removed) != 0 {
		t.Fatalf("expected empty diff, got %+v", d)
	}
}
