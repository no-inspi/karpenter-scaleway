package selection

import "testing"

func prices(m map[string]float64) PriceFunc {
	return func(zone, instanceType string) (float64, bool) {
		v, ok := m[zone+"/"+instanceType]
		return v, ok
	}
}

// Karpenter serializes the instance-type requirement as a sorted set, so
// "PRO2-S" comes before "PRO2-XS" and "PRO2-XXS". Taking Values[0] picked
// the most expensive type on a test cluster.
func TestCheapestIgnoresAlphabeticalOrder(t *testing.T) {
	p := prices(map[string]float64{
		"fr-par-1/PRO2-S":   0.219,
		"fr-par-1/PRO2-XS":  0.11,
		"fr-par-1/PRO2-XXS": 0.055,
	})
	typ, zone := Cheapest([]string{"PRO2-S", "PRO2-XS", "PRO2-XXS"}, []string{"fr-par-1"}, p)
	if typ != "PRO2-XXS" || zone != "fr-par-1" {
		t.Fatalf("got %s/%s, want PRO2-XXS/fr-par-1", typ, zone)
	}
}

func TestCheapestPicksZoneToo(t *testing.T) {
	p := prices(map[string]float64{
		"fr-par-1/GP1-XS": 0.10,
		"fr-par-2/GP1-XS": 0.08,
	})
	typ, zone := Cheapest([]string{"GP1-XS"}, []string{"fr-par-1", "fr-par-2"}, p)
	if typ != "GP1-XS" || zone != "fr-par-2" {
		t.Fatalf("got %s/%s, want GP1-XS/fr-par-2", typ, zone)
	}
}

func TestCheapestSkipsUnpricedPairs(t *testing.T) {
	// PRO2-XXS not offered (or masked unavailable) in fr-par-1.
	p := prices(map[string]float64{"fr-par-1/PRO2-S": 0.219})
	typ, zone := Cheapest([]string{"PRO2-S", "PRO2-XXS"}, []string{"fr-par-1"}, p)
	if typ != "PRO2-S" || zone != "fr-par-1" {
		t.Fatalf("got %s/%s, want PRO2-S/fr-par-1", typ, zone)
	}
}

func TestCheapestFallsBackToFirstWhenNothingPriced(t *testing.T) {
	typ, zone := Cheapest([]string{"B", "A"}, []string{"z2", "z1"}, prices(nil))
	if typ != "B" || zone != "z2" {
		t.Fatalf("got %s/%s, want B/z2", typ, zone)
	}
}

func TestCheapestTieIsDeterministic(t *testing.T) {
	p := prices(map[string]float64{"z/A": 1, "z/B": 1})
	for i := 0; i < 20; i++ {
		if typ, _ := Cheapest([]string{"B", "A"}, []string{"z"}, p); typ != "B" {
			t.Fatalf("tie must keep candidate order, got %s", typ)
		}
	}
}

func TestCheapestEmpty(t *testing.T) {
	if typ, zone := Cheapest(nil, []string{"z"}, prices(nil)); typ != "" || zone != "" {
		t.Fatalf("got %q/%q, want empty", typ, zone)
	}
}

func TestAllowedZones(t *testing.T) {
	if got := AllowedZones([]string{"a", "b"}, nil); len(got) != 2 {
		t.Fatalf("empty whitelist must allow all, got %v", got)
	}
	if got := AllowedZones([]string{"a", "b", "c"}, []string{"c", "a"}); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("got %v, want [a c]", got)
	}
	if got := AllowedZones([]string{"x"}, []string{"a"}); len(got) != 1 || got[0] != "x" {
		t.Fatalf("no match must return candidates unchanged, got %v", got)
	}
}
