package kapsule

import (
	"context"
	"testing"

	"github.com/no-inspi/karpenter-scaleway/pkg/backend/drift"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func nodeClassWithDisk(sizeGB int32) any {
	nc := mkNodeClass("fr-par-1")
	nc.Spec.RootVolumeSizeGB = &sizeGB
	return nc
}

// Create forwards spec.rootVolumeSizeGB to the Kapsule pool.
func TestCreate_ForwardsRootVolumeSize(t *testing.T) {
	var gotSize *int32
	api := cheapestAPI(new([]string))
	inner := api.createPoolFn
	api.createPoolFn = func(ctx context.Context, p scaleway.CreatePoolParams) (*scaleway.KapsulePool, error) {
		gotSize = p.RootVolumeSizeGB
		return inner(ctx, p)
	}
	bk := newKapsuleWithQuota(t, api, QuotaConfig{}, nodeClassWithDisk(80))

	if _, err := bk.Create(context.Background(), mkNCForCreate("nc-disk", "PRO2-XXS", "fr-par-1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotSize == nil || *gotSize != 80 {
		t.Fatalf("CreatePool RootVolumeSizeGB = %v, want 80", gotSize)
	}
}

// GetInstanceTypes advertises ephemeral-storage sized from the NodeClass,
// with the 20 GB Kapsule default when unset.
func TestGetInstanceTypes_EphemeralStorage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		nodeClass any
		want      string
	}{
		{name: "default", nodeClass: mkNodeClass("fr-par-1"), want: "18G"},
		{name: "explicit", nodeClass: nodeClassWithDisk(80), want: "72G"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bk := newKapsuleWithQuota(t, &fakeKapsule{}, QuotaConfig{}, tc.nodeClass)
			np := &karpv1.NodePool{}
			np.Spec.Template.Spec.NodeClassRef = &karpv1.NodeClassReference{Name: "default"}
			its, err := bk.GetInstanceTypes(context.Background(), np)
			if err != nil {
				t.Fatalf("GetInstanceTypes: %v", err)
			}
			if len(its) == 0 {
				t.Fatal("no instance types")
			}
			for _, it := range its {
				got := it.Capacity[corev1.ResourceEphemeralStorage]
				if got.Cmp(resource.MustParse(tc.want)) != 0 {
					t.Fatalf("%s: ephemeral-storage = %s, want %s", it.Name, got.String(), tc.want)
				}
			}
		})
	}
}

func TestKapsuleIsDrifted_RootVolumeSize(t *testing.T) {
	for _, tc := range []struct {
		name      string
		poolGB    int64
		nodeClass any
		want      string
	}{
		{name: "spec differs from pool", poolGB: 20, nodeClass: nodeClassWithDisk(80), want: string(drift.ReasonRootVolumeSize)},
		{name: "spec matches pool", poolGB: 80, nodeClass: nodeClassWithDisk(80), want: ""},
		{name: "spec unset", poolGB: 20, nodeClass: mkNodeClass("fr-par-1"), want: ""},
		{name: "pool size unknown", poolGB: 0, nodeClass: nodeClassWithDisk(80), want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeKapsule{listResp: []*scaleway.KapsulePool{
				{ID: "pool-1", NodeClaim: "nc-1", NodeType: "PRO2-XXS", Zone: "fr-par-1", Size: 1, RootVolumeSizeGB: tc.poolGB},
			}}
			bk := newKapsuleWithQuota(t, api, QuotaConfig{}, tc.nodeClass)
			nc := mkNCForCreate("nc-1", "PRO2-XXS", "fr-par-1")
			nc.Annotations = map[string]string{PoolIDAnnotationKey: "pool-1"}
			nc.Labels = map[string]string{
				corev1.LabelInstanceTypeStable: "PRO2-XXS",
				corev1.LabelTopologyZone:       "fr-par-1",
			}
			reason, err := bk.IsDrifted(context.Background(), nc)
			if err != nil {
				t.Fatalf("IsDrifted: %v", err)
			}
			if string(reason) != tc.want {
				t.Fatalf("reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

// A NodeClass that was deleted is not root volume drift.
func TestKapsuleIsDrifted_RootVolumeNodeClassGone(t *testing.T) {
	api := &fakeKapsule{listResp: []*scaleway.KapsulePool{
		{ID: "pool-1", NodeClaim: "nc-1", NodeType: "PRO2-XXS", Zone: "fr-par-1", Size: 1, RootVolumeSizeGB: 20},
	}}
	bk := newKapsuleWithQuota(t, api, QuotaConfig{})
	nc := mkNCForCreate("nc-1", "PRO2-XXS", "fr-par-1")
	nc.Annotations = map[string]string{PoolIDAnnotationKey: "pool-1"}
	reason, err := bk.IsDrifted(context.Background(), nc)
	if err != nil || reason != "" {
		t.Fatalf("expected no drift, got (%q, %v)", reason, err)
	}
}
