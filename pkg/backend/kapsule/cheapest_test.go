package kapsule

// Regression: on a test cluster a 1-CPU pod got a PRO2-S (8 vCPU,
// 0.219 EUR/h) instead of a PRO2-XXS (2 vCPU, 0.055 EUR/h). Karpenter's
// instance-type requirement is an alphabetical set and Create used to take
// Values[0]. Prices here come from the embedded catalog baseline.

import (
	"context"
	"testing"
	"time"

	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	corev1 "k8s.io/api/core/v1"
)

func cheapestAPI(created *[]string) *fakeKapsule {
	return &fakeKapsule{
		countTotal: 1,
		createPoolFn: func(_ context.Context, p scaleway.CreatePoolParams) (*scaleway.KapsulePool, error) {
			*created = append(*created, p.InstanceType+"/"+string(p.Zone))
			return &scaleway.KapsulePool{ID: "pool-1", NodeClaim: p.Name, NodeType: p.InstanceType, Zone: p.Zone, Size: 1}, nil
		},
		waitForNodeFn: func(_ context.Context, poolID string, _ time.Duration) (*scaleway.KapsuleNode, error) {
			return &scaleway.KapsuleNode{ID: "n-1", PoolID: poolID, ProviderID: "scaleway://instance/fr-par-1/n-1", Name: "node-1"}, nil
		},
	}
}

func TestCreate_PicksCheapestCandidateNotAlphabeticalFirst(t *testing.T) {
	var created []string
	bk := newKapsuleWithQuota(t, cheapestAPI(&created), QuotaConfig{}, mkNodeClass("fr-par-1"))
	nc := mkNCForCreate("nc-cheap", "PRO2-S", "fr-par-1")
	nc.Spec.Requirements[0].Values = []string{"PRO2-S", "PRO2-XS", "PRO2-XXS"}

	out, err := bk.Create(context.Background(), nc)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created) != 1 || created[0] != "PRO2-XXS/fr-par-1" {
		t.Fatalf("CreatePool calls = %v, want [PRO2-XXS/fr-par-1]", created)
	}
	if got := out.Labels[corev1.LabelInstanceTypeStable]; got != "PRO2-XXS" {
		t.Fatalf("instance-type label = %q, want PRO2-XXS", got)
	}
}

func TestCreate_SkipsCheapestWhenMaskedUnavailable(t *testing.T) {
	var created []string
	bk := newKapsuleWithQuota(t, cheapestAPI(&created), QuotaConfig{}, mkNodeClass("fr-par-1"))
	bk.unavailable.MarkUnavailable("PRO2-XXS", "fr-par-1")
	nc := mkNCForCreate("nc-cheap", "PRO2-S", "fr-par-1")
	nc.Spec.Requirements[0].Values = []string{"PRO2-S", "PRO2-XS", "PRO2-XXS"}

	if _, err := bk.Create(context.Background(), nc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created) != 1 || created[0] != "PRO2-XS/fr-par-1" {
		t.Fatalf("CreatePool calls = %v, want [PRO2-XS/fr-par-1]", created)
	}
}
