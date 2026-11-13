package main

import (
	"reflect"
	"testing"

	"github.com/no-inspi/karpenter-scaleway/pkg/instancetype"
)

func TestSortDeterministic_OrdersTypesAndOfferings(t *testing.T) {
	c := &instancetype.Catalog{Types: []instancetype.TypeEntry{
		{Name: "B", Offerings: []instancetype.Offering{
			{Zone: "fr-par-2", CapacityType: "on-demand"},
			{Zone: "fr-par-1", CapacityType: "on-demand"},
		}},
		{Name: "A", Offerings: []instancetype.Offering{
			{Zone: "nl-ams-1", CapacityType: "on-demand"},
		}},
	}}
	sortDeterministic(c)
	want := []string{"A", "B"}
	got := []string{c.Types[0].Name, c.Types[1].Name}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("types order: got %v, want %v", got, want)
	}
	if c.Types[1].Offerings[0].Zone != "fr-par-1" {
		t.Fatalf("offerings not sorted: %+v", c.Types[1].Offerings)
	}
}
