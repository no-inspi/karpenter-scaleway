package instancetype

import (
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func loadFixture(t *testing.T) *Catalog {
	t.Helper()
	data, err := os.ReadFile("testdata/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newNP(reqs []karpv1.NodeSelectorRequirementWithMinValues) *karpv1.NodePool {
	return &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				Spec: karpv1.NodeClaimTemplateSpec{Requirements: reqs},
			},
		},
	}
}

func TestFilter_NoConstraints_KeepsAll(t *testing.T) {
	c := loadFixture(t)
	got := filterCatalog(c, newNP(nil), nil)
	if len(got) != 3 {
		t.Fatalf("got %d types, want 3", len(got))
	}
}

func TestFilter_ZoneIn_FrPar1_KeepsOnlyFrPar1Offerings(t *testing.T) {
	c := loadFixture(t)
	np := newNP([]karpv1.NodeSelectorRequirementWithMinValues{{
		Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpIn, Values: []string{"fr-par-1"},
	}})
	got := filterCatalog(c, np, nil)
	if len(got) != 3 {
		t.Fatalf("expected all 3 types still match (each has fr-par-1), got %d", len(got))
	}
	for _, e := range got {
		if len(e.Offerings) != 1 || e.Offerings[0].Zone != "fr-par-1" {
			t.Fatalf("type %s should have 1 fr-par-1 offering only, got %+v", e.Name, e.Offerings)
		}
	}
}

func TestFilter_ZoneIn_FrPar2_DropsTypeWithoutThatZone(t *testing.T) {
	c := loadFixture(t)
	np := newNP([]karpv1.NodeSelectorRequirementWithMinValues{{
		Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpIn, Values: []string{"fr-par-2"},
	}})
	got := filterCatalog(c, np, nil)
	if len(got) != 1 || got[0].Name != "POP2-2C-8G" {
		t.Fatalf("expected only POP2-2C-8G, got %+v", names(got))
	}
}

func TestFilter_ArchArm64_KeepsOnlyARM(t *testing.T) {
	c := loadFixture(t)
	np := newNP([]karpv1.NodeSelectorRequirementWithMinValues{{
		Key: corev1.LabelArchStable, Operator: corev1.NodeSelectorOpIn, Values: []string{"arm64"},
	}})
	got := filterCatalog(c, np, nil)
	if len(got) != 1 || got[0].Name != "COPARM1-2C-8G" {
		t.Fatalf("expected only COPARM1-2C-8G, got %+v", names(got))
	}
}

func TestFilter_AllowedZonesWhitelist_AppliesIntersection(t *testing.T) {
	c := loadFixture(t)
	got := filterCatalog(c, newNP(nil), []string{"fr-par-2"})
	if len(got) != 1 || got[0].Name != "POP2-2C-8G" {
		t.Fatalf("expected only POP2-2C-8G in fr-par-2, got %+v", names(got))
	}
}

func names(es []internalEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name
	}
	return out
}
