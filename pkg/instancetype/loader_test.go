package instancetype

import (
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func TestLoad_Fixture(t *testing.T) {
	data, err := os.ReadFile("testdata/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseCatalog(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.Types) != 3 {
		t.Fatalf("got %d types, want 3", len(c.Types))
	}
	if c.Types[0].Name != "POP2-2C-8G" {
		t.Fatalf("got name %q, want POP2-2C-8G", c.Types[0].Name)
	}
	if got := c.Types[0].Offerings[0].PriceEURhr; got != 0.04 {
		t.Fatalf("got price %v, want 0.04", got)
	}
}

func TestHydrate_PropagatesPerOfferingAvailable(t *testing.T) {
	// Fixture: POP2-2C-8G has fr-par-1 available + fr-par-2 unavailable.
	// hydrate must mirror that into cloudprovider.Offering.Available rather
	// than hardcoding true (regression guard).
	data, err := os.ReadFile("testdata/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseCatalog(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := hydrate(c)
	var pop *cloudprovider.InstanceType
	for _, it := range out {
		if it.Name == "POP2-2C-8G" {
			pop = it
			break
		}
	}
	if pop == nil {
		t.Fatal("POP2-2C-8G missing from hydrate output")
	}
	got := map[string]bool{}
	for _, o := range pop.Offerings {
		zone := o.Requirements.Get(corev1.LabelTopologyZone).Values()[0]
		got[zone] = o.Available
	}
	if !got["fr-par-1"] {
		t.Errorf("fr-par-1 should be Available=true, got false")
	}
	if got["fr-par-2"] {
		t.Errorf("fr-par-2 should be Available=false, got true")
	}
}
