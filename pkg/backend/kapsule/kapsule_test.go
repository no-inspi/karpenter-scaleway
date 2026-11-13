package kapsule

import (
	"context"
	"errors"
	"testing"
	"time"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/pkg/backend/drift"
	"github.com/no-inspi/karpenter-scaleway/pkg/pricing"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpapi "sigs.k8s.io/karpenter/pkg/apis"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

type fakeKapsule struct {
	listResp        []*scaleway.KapsulePool
	listErr         error
	listNodes       map[string][]*scaleway.KapsuleNode
	deletePoolCalls []string
	deletePoolErr   error
	// deletePoolErrs, when set, is consumed one entry per DeletePool call
	// (then falls back to deletePoolErr).
	deletePoolErrs []error
	clusterResp    *scaleway.Cluster
	clusterErr     error

	createPoolFn    func(ctx context.Context, params scaleway.CreatePoolParams) (*scaleway.KapsulePool, error)
	createPoolCalls int
	waitForNodeFn   func(ctx context.Context, poolID string, timeout time.Duration) (*scaleway.KapsuleNode, error)

	countTotal  int
	countKarp   int
	countErr    error
	countCalls  int
	countOnce   bool // when true, the next CountPools after the first uses countTotalNext
	countTotal2 int
	countKarp2  int
}

func (f *fakeKapsule) CreatePool(ctx context.Context, params scaleway.CreatePoolParams) (*scaleway.KapsulePool, error) {
	f.createPoolCalls++
	if f.createPoolFn != nil {
		return f.createPoolFn(ctx, params)
	}
	return nil, errors.New("CreatePool not used in these tests")
}
func (f *fakeKapsule) DeletePool(_ context.Context, id string) error {
	f.deletePoolCalls = append(f.deletePoolCalls, id)
	if len(f.deletePoolErrs) > 0 {
		err := f.deletePoolErrs[0]
		f.deletePoolErrs = f.deletePoolErrs[1:]
		return err
	}
	return f.deletePoolErr
}
func (f *fakeKapsule) WaitForNodeReady(ctx context.Context, poolID string, timeout time.Duration) (*scaleway.KapsuleNode, error) {
	if f.waitForNodeFn != nil {
		return f.waitForNodeFn(ctx, poolID, timeout)
	}
	return nil, errors.New("WaitForNodeReady not used")
}
func (f *fakeKapsule) ListPools(_ context.Context) ([]*scaleway.KapsulePool, error) {
	return f.listResp, f.listErr
}
func (f *fakeKapsule) ListNodesForPool(_ context.Context, poolID string) ([]*scaleway.KapsuleNode, error) {
	return f.listNodes[poolID], nil
}
func (f *fakeKapsule) CountPools(_ context.Context) (int, int, error) {
	f.countCalls++
	if f.countErr != nil {
		return 0, 0, f.countErr
	}
	if f.countOnce && f.countCalls > 1 {
		return f.countTotal2, f.countKarp2, nil
	}
	return f.countTotal, f.countKarp, nil
}
func (f *fakeKapsule) GetCluster(_ context.Context) (*scaleway.Cluster, error) {
	return f.clusterResp, f.clusterErr
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1: %v", err)
	}
	karpGV := schema.GroupVersion{Group: karpapi.Group, Version: "v1"}
	metav1.AddToGroupVersion(s, karpGV)
	s.AddKnownTypes(karpGV, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{}, &karpv1.NodePool{}, &karpv1.NodePoolList{})
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("v1alpha1: %v", err)
	}
	return s
}

func newKapsule(t *testing.T, api scaleway.KapsuleAPI, kc client.Client) *Kapsule {
	t.Helper()
	bk, err := New(Options{
		Kapsule:    api,
		KubeClient: kc,
		Pricing:    pricing.NewProvider(&pricing.FakeAPI{}, nil),
		Log:        logr.Discard(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return bk
}

// Per Karpenter contract Delete must return NodeClaimNotFoundError after
// the pool is confirmed gone.
func TestDelete_ReturnsNodeClaimNotFoundOnSuccess(t *testing.T) {
	api := &fakeKapsule{}
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	bk := newKapsule(t, api, kc)

	nc := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "nc-1",
			Annotations: map[string]string{PoolIDAnnotationKey: "pool-uuid"},
		},
	}
	err := bk.Delete(context.Background(), nc)
	if !karpcp.IsNodeClaimNotFoundError(err) {
		t.Fatalf("expected NodeClaimNotFoundError, got %v", err)
	}
	if len(api.deletePoolCalls) != 1 || api.deletePoolCalls[0] != "pool-uuid" {
		t.Fatalf("expected one DeletePool(pool-uuid), got %v", api.deletePoolCalls)
	}
}

// Tag-based fallback when the annotation is missing.
func TestDelete_FallsBackToPoolTagLookup(t *testing.T) {
	api := &fakeKapsule{
		listResp: []*scaleway.KapsulePool{
			{ID: "pool-other", NodeClaim: "other-nc"},
			{ID: "pool-target", NodeClaim: "nc-2"},
		},
	}
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	bk := newKapsule(t, api, kc)

	nc := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc-2"}}
	err := bk.Delete(context.Background(), nc)
	if !karpcp.IsNodeClaimNotFoundError(err) {
		t.Fatalf("expected NodeClaimNotFoundError, got %v", err)
	}
	if len(api.deletePoolCalls) != 1 || api.deletePoolCalls[0] != "pool-target" {
		t.Fatalf("expected DeletePool(pool-target), got %v", api.deletePoolCalls)
	}
}

func TestGet_FindsByProviderID(t *testing.T) {
	api := &fakeKapsule{
		listResp: []*scaleway.KapsulePool{
			{ID: "pool-1", NodeClaim: "nc-x", NodeType: "GP1-XS", Zone: "fr-par-1"},
		},
		listNodes: map[string][]*scaleway.KapsuleNode{
			"pool-1": {
				{ID: "n-1", PoolID: "pool-1", ProviderID: "scaleway://instance/fr-par-1/aaaa", Name: "scw-x"},
			},
		},
	}
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	bk := newKapsule(t, api, kc)

	nc, err := bk.Get(context.Background(), "scaleway://instance/fr-par-1/aaaa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if nc.Annotations[PoolIDAnnotationKey] != "pool-1" {
		t.Fatalf("expected pool-1 annotation, got %v", nc.Annotations)
	}
}

func TestHealthcheck_SurfacesError(t *testing.T) {
	bk := newKapsule(t,
		&fakeKapsule{clusterErr: errors.New("502")},
		fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	if err := bk.Healthcheck(context.Background()); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- IsDrifted ---

// mkKapsuleNC builds a NodeClaim populated as Create would — instance-type
// and zone labels carry the resource state at provision time, the pool ID
// annotation pins the lookup. Tests mutate one field at a time.
func mkKapsuleNC(name, poolID, instanceType, zone string) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{PoolIDAnnotationKey: poolID},
			Labels: map[string]string{
				corev1.LabelInstanceTypeStable: instanceType,
				corev1.LabelTopologyZone:       zone,
			},
		},
	}
}

// Happy path: pool matches NodeClaim → no drift.
func TestKapsuleIsDrifted_NoDrift(t *testing.T) {
	api := &fakeKapsule{
		listResp: []*scaleway.KapsulePool{
			{ID: "pool-1", NodeClaim: "nc-1", NodeType: "GP1-XS", Zone: "fr-par-1", Size: 1},
		},
	}
	bk := newKapsule(t, api, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	reason, err := bk.IsDrifted(context.Background(), mkKapsuleNC("nc-1", "pool-1", "GP1-XS", "fr-par-1"))
	if err != nil || reason != "" {
		t.Fatalf("expected no drift, got (%q, %v)", reason, err)
	}
}

// Pool gone → not drift; Karpenter's GC recycles via NodeClaimNotFound.
func TestKapsuleIsDrifted_PoolMissingIsNotDrift(t *testing.T) {
	api := &fakeKapsule{listResp: []*scaleway.KapsulePool{}}
	bk := newKapsule(t, api, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	reason, err := bk.IsDrifted(context.Background(), mkKapsuleNC("nc-1", "pool-gone", "GP1-XS", "fr-par-1"))
	if err != nil || reason != "" {
		t.Fatalf("expected no drift, got (%q, %v)", reason, err)
	}
}

// Operator manually scaled the pool to size 2 → drift.
func TestKapsuleIsDrifted_PoolResize(t *testing.T) {
	api := &fakeKapsule{
		listResp: []*scaleway.KapsulePool{
			{ID: "pool-1", NodeClaim: "nc-1", NodeType: "GP1-XS", Zone: "fr-par-1", Size: 2},
		},
	}
	bk := newKapsule(t, api, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	reason, err := bk.IsDrifted(context.Background(), mkKapsuleNC("nc-1", "pool-1", "GP1-XS", "fr-par-1"))
	if err != nil {
		t.Fatalf("IsDrifted: %v", err)
	}
	if reason != drift.ReasonPoolResize {
		t.Fatalf("expected PoolResize, got %q", reason)
	}
}

// Operator edited the pool node-type out of band → drift.
func TestKapsuleIsDrifted_InstanceType(t *testing.T) {
	api := &fakeKapsule{
		listResp: []*scaleway.KapsulePool{
			{ID: "pool-1", NodeClaim: "nc-1", NodeType: "GP1-S", Zone: "fr-par-1", Size: 1},
		},
	}
	bk := newKapsule(t, api, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	reason, err := bk.IsDrifted(context.Background(), mkKapsuleNC("nc-1", "pool-1", "GP1-XS", "fr-par-1"))
	if err != nil || reason != drift.ReasonInstanceType {
		t.Fatalf("expected InstanceType drift, got (%q, %v)", reason, err)
	}
}

// Pool re-zoned out of band → drift.
func TestKapsuleIsDrifted_Zone(t *testing.T) {
	api := &fakeKapsule{
		listResp: []*scaleway.KapsulePool{
			{ID: "pool-1", NodeClaim: "nc-1", NodeType: "GP1-XS", Zone: "fr-par-2", Size: 1},
		},
	}
	bk := newKapsule(t, api, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	reason, err := bk.IsDrifted(context.Background(), mkKapsuleNC("nc-1", "pool-1", "GP1-XS", "fr-par-1"))
	if err != nil || reason != drift.ReasonZone {
		t.Fatalf("expected Zone drift, got (%q, %v)", reason, err)
	}
}

// skip-drift annotation suppresses backend-side checks even when a real
// divergence exists. Use case: operator-led debug session.
func TestKapsuleIsDrifted_SkipAnnotation(t *testing.T) {
	api := &fakeKapsule{
		listResp: []*scaleway.KapsulePool{
			{ID: "pool-1", NodeClaim: "nc-1", NodeType: "GP1-XS", Zone: "fr-par-1", Size: 5},
		},
	}
	bk := newKapsule(t, api, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	nc := mkKapsuleNC("nc-1", "pool-1", "GP1-XS", "fr-par-1")
	nc.Annotations[drift.SkipAnnotationKey] = "true"
	reason, err := bk.IsDrifted(context.Background(), nc)
	if err != nil || reason != "" {
		t.Fatalf("expected no drift (skipped), got (%q, %v)", reason, err)
	}
}

// No pool-id annotation: nothing to compare against → not drift.
// (NodeClaim is mid-create; Karpenter will retry next reconcile.)
func TestKapsuleIsDrifted_MissingPoolAnnotation(t *testing.T) {
	bk := newKapsule(t, &fakeKapsule{}, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	nc := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc-1"}}
	reason, err := bk.IsDrifted(context.Background(), nc)
	if err != nil || reason != "" {
		t.Fatalf("expected no drift, got (%q, %v)", reason, err)
	}
}

// API failure: surface the error so Karpenter retries instead of silently
// proclaiming "not drifted".
func TestKapsuleIsDrifted_ListPoolsError(t *testing.T) {
	api := &fakeKapsule{listErr: errors.New("503 service unavailable")}
	bk := newKapsule(t, api, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	_, err := bk.IsDrifted(context.Background(), mkKapsuleNC("nc-1", "pool-1", "GP1-XS", "fr-par-1"))
	if err == nil {
		t.Fatal("expected error from ListPools failure, got nil")
	}
}

// --- zone selection + whitelist guard ---

func mkNCWithZone(zones ...string) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{Spec: karpv1.NodeClaimSpec{
		Requirements: []karpv1.NodeSelectorRequirementWithMinValues{{
			Key:      corev1.LabelTopologyZone,
			Operator: corev1.NodeSelectorOpIn,
			Values:   zones,
		}},
	}}
}

func TestSelectZone_SingleValue(t *testing.T) {
	z, err := zoneCandidates(mkNCWithZone("fr-par-2"), nil)
	if err != nil || len(z) != 1 || z[0] != "fr-par-2" {
		t.Fatalf("got (%q, %v)", z, err)
	}
}

func TestSelectZone_MissingRequirementNoFallback(t *testing.T) {
	_, err := zoneCandidates(&karpv1.NodeClaim{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSelectZone_MultiValueReturnsAll(t *testing.T) {
	z, err := zoneCandidates(mkNCWithZone("fr-par-1", "fr-par-2"), nil)
	if err != nil || len(z) != 2 || z[0] != "fr-par-1" || z[1] != "fr-par-2" {
		t.Fatalf("expected both candidates, got (%v, %v)", z, err)
	}
}

// When the NodeClaim carries no zone requirement (e.g. NodePool
// example from getting-started.md) and the NodeClass declares spec.zones,
// fall back to the first declared zone instead of refusing the launch.
func TestSelectZone_FallbackFromNodeClassZones(t *testing.T) {
	z, err := zoneCandidates(&karpv1.NodeClaim{}, []string{"fr-par-2", "nl-ams-1"})
	if err != nil || len(z) != 2 || z[0] != "fr-par-2" {
		t.Fatalf("expected fallback [fr-par-2 nl-ams-1], got (%v, %v)", z, err)
	}
}

// Requirement on the NodeClaim must always win over the NodeClass fallback,
// otherwise the scheduler's topology spread decisions get silently ignored.
func TestSelectZone_RequirementWinsOverFallback(t *testing.T) {
	z, err := zoneCandidates(mkNCWithZone("fr-par-3"), []string{"nl-ams-1"})
	if err != nil || len(z) != 1 || z[0] != "fr-par-3" {
		t.Fatalf("expected requirement fr-par-3, got (%v, %v)", z, err)
	}
}

func mkNCWithInstanceType(types ...string) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{Spec: karpv1.NodeClaimSpec{
		Requirements: []karpv1.NodeSelectorRequirementWithMinValues{{
			Key:      corev1.LabelInstanceTypeStable,
			Operator: corev1.NodeSelectorOpIn,
			Values:   types,
		}},
	}}
}

func TestSelectInstanceType_SingleValue(t *testing.T) {
	it, err := instanceTypeCandidates(mkNCWithInstanceType("GP1-XS"))
	if err != nil || len(it) != 1 || it[0] != "GP1-XS" {
		t.Fatalf("got (%q, %v)", it, err)
	}
}

func TestSelectInstanceType_MissingRequirement(t *testing.T) {
	_, err := instanceTypeCandidates(&karpv1.NodeClaim{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// Regression: P12 finding-1 — Karpenter v1.10 leaves the instance-type
// requirement as the full candidate set. The cloudprovider must accept the
// multi-value In; the price-aware pick is selection.Cheapest's job (the set
// is alphabetical, not sorted by price).
func TestSelectInstanceType_MultiValueReturnsAll(t *testing.T) {
	it, err := instanceTypeCandidates(mkNCWithInstanceType("GP1-XS", "GP1-S", "PRO2-XS"))
	if err != nil || len(it) != 3 {
		t.Fatalf("expected 3 candidates, got (%v, %v)", it, err)
	}
}

func TestGuardZoneAllowed_EmptyWhitelistAccepts(t *testing.T) {
	if err := guardZoneAllowed("fr-par-2", nil); err != nil {
		t.Fatal(err)
	}
}

func TestGuardZoneAllowed_InWhitelist(t *testing.T) {
	if err := guardZoneAllowed("fr-par-1", []string{"fr-par-1", "fr-par-2"}); err != nil {
		t.Fatal(err)
	}
}

func TestGuardZoneAllowed_NotInWhitelist(t *testing.T) {
	if err := guardZoneAllowed("nl-ams-1", []string{"fr-par-1"}); err == nil {
		t.Fatal("expected error")
	}
}
