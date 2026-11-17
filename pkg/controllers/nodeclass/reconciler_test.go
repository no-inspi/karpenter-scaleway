package nodeclass

import (
	"context"
	"errors"
	"testing"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/pkg/backend"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	opstatus "github.com/awslabs/operatorpkg/status"
	"github.com/go-logr/logr"
	"github.com/scaleway/scaleway-sdk-go/scw"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// fakeMarketplace is a hand-rolled scaleway.MarketplaceAPI fake that lets
// tests assert call count and inject distinct responses per call.
type fakeMarketplace struct {
	calls    int
	imageID  string
	errStack []error // popped in order
}

func (f *fakeMarketplace) LookupImage(_ context.Context, _ scw.Zone, _ string) (string, error) {
	f.calls++
	if len(f.errStack) > 0 {
		err := f.errStack[0]
		f.errStack = f.errStack[1:]
		if err != nil {
			return "", err
		}
	}
	return f.imageID, nil
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1: %v", err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("v1alpha1: %v", err)
	}
	return s
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.ScalewayNodeClass{}).
		Build()
}

func newNodeClass(name, zone, label string) *v1alpha1.ScalewayNodeClass {
	return &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.ScalewayNodeClassSpec{
			Zones:      []string{zone},
			ImageLabel: label,
		},
	}
}

func conditionFor(nc *v1alpha1.ScalewayNodeClass, t string) *opstatus.Condition {
	for i := range nc.Status.Conditions {
		if nc.Status.Conditions[i].Type == t {
			return &nc.Status.Conditions[i]
		}
	}
	return nil
}

// Happy path: Ready=True after a Marketplace hit, image ID stored.
func TestReconcile_ImageResolved(t *testing.T) {
	nc := newNodeClass("default", "fr-par-1", "ubuntu_jammy")
	kc := newClient(t, nc)
	mkt := &fakeMarketplace{imageID: "img-uuid"}
	r := New(kc, mkt, backend.KindKosmos, logr.Discard())

	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "default"}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no requeue on success, got %v", res.RequeueAfter)
	}
	got := &v1alpha1.ScalewayNodeClass{}
	if err := kc.Get(context.Background(), types.NamespacedName{Name: "default"}, got); err != nil {
		t.Fatalf("get back: %v", err)
	}
	if got.Status.ResolvedImageID != "img-uuid" {
		t.Errorf("expected ResolvedImageID=img-uuid, got %q", got.Status.ResolvedImageID)
	}
	if got.Status.ValidatedAt == nil {
		t.Error("expected ValidatedAt to be set")
	}
	cond := conditionFor(got, opstatus.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True, got %#v", cond)
	}
}

// ImageNotFound: Ready=False with reason=ImageNotFound and 30s requeue.
func TestReconcile_ImageNotFound(t *testing.T) {
	nc := newNodeClass("typo", "fr-par-1", "ubuntu_jammyy")
	kc := newClient(t, nc)
	mkt := &fakeMarketplace{errStack: []error{scaleway.ErrImageNotFound}}
	r := New(kc, mkt, backend.KindKosmos, logr.Discard())

	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "typo"}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != requeueOnImageNotFound {
		t.Fatalf("expected requeue=%v, got %v", requeueOnImageNotFound, res.RequeueAfter)
	}
	got := &v1alpha1.ScalewayNodeClass{}
	_ = kc.Get(context.Background(), types.NamespacedName{Name: "typo"}, got)
	cond := conditionFor(got, opstatus.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != v1alpha1.ConditionReasonImageNotFound {
		t.Errorf("expected Ready=False/ImageNotFound, got %#v", cond)
	}
}

// Transient Marketplace error: Ready=False/ValidationPending, 10s requeue.
func TestReconcile_TransientMarketplaceError(t *testing.T) {
	nc := newNodeClass("transient", "fr-par-1", "ubuntu_jammy")
	kc := newClient(t, nc)
	mkt := &fakeMarketplace{errStack: []error{errors.New("503 service unavailable")}}
	r := New(kc, mkt, backend.KindKosmos, logr.Discard())

	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "transient"}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != requeueOnTransientError {
		t.Fatalf("expected requeue=%v, got %v", requeueOnTransientError, res.RequeueAfter)
	}
	got := &v1alpha1.ScalewayNodeClass{}
	_ = kc.Get(context.Background(), types.NamespacedName{Name: "transient"}, got)
	cond := conditionFor(got, opstatus.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != v1alpha1.ConditionReasonValidationPending {
		t.Errorf("expected Ready=False/ValidationPending, got %#v", cond)
	}
}

// Cache hit on a second reconcile means the Marketplace is hit only once.
func TestReconcile_CacheHitSecondReconcile(t *testing.T) {
	nc := newNodeClass("cached", "fr-par-1", "ubuntu_jammy")
	kc := newClient(t, nc)
	mkt := &fakeMarketplace{imageID: "img-uuid"}
	r := New(kc, mkt, backend.KindKosmos, logr.Discard())

	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "cached"}}); err != nil {
			t.Fatalf("Reconcile #%d: %v", i, err)
		}
	}
	if mkt.calls != 1 {
		t.Fatalf("expected 1 Marketplace call (cache hit on 2/3), got %d", mkt.calls)
	}
}

// Backend Kapsule: kosmos-only fields surface in unusedFieldsForBackend.
// rootVolumeSizeGB is applied to Kapsule pools, so it is not listed.
func TestReconcile_UnusedFieldsForKapsule(t *testing.T) {
	size := int32(50)
	pub := true
	nc := &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "kap"},
		Spec: v1alpha1.ScalewayNodeClassSpec{
			Zones:            []string{"fr-par-1"},
			ImageLabel:       "ubuntu_jammy",
			RootVolumeSizeGB: &size,
			RootVolumeType:   "sbs_volume",
			PublicIP:         &pub,
		},
	}
	kc := newClient(t, nc)
	mkt := &fakeMarketplace{imageID: "img-uuid"}
	r := New(kc, mkt, backend.KindKapsule, logr.Discard())

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "kap"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &v1alpha1.ScalewayNodeClass{}
	_ = kc.Get(context.Background(), types.NamespacedName{Name: "kap"}, got)
	want := []string{"rootVolumeType", "publicIP"}
	if len(got.Status.UnusedFieldsForBackend) != len(want) {
		t.Fatalf("expected %v, got %v", want, got.Status.UnusedFieldsForBackend)
	}
	for i, f := range want {
		if got.Status.UnusedFieldsForBackend[i] != f {
			t.Errorf("unused[%d]: got=%s want=%s", i, got.Status.UnusedFieldsForBackend[i], f)
		}
	}
}

// Backend Kosmos: no fields are unused — slice must be nil/empty.
func TestReconcile_NoUnusedFieldsForKosmos(t *testing.T) {
	size := int32(50)
	nc := &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "kos"},
		Spec: v1alpha1.ScalewayNodeClassSpec{
			Zones:            []string{"fr-par-1"},
			ImageLabel:       "ubuntu_jammy",
			RootVolumeSizeGB: &size,
		},
	}
	kc := newClient(t, nc)
	mkt := &fakeMarketplace{imageID: "img-uuid"}
	r := New(kc, mkt, backend.KindKosmos, logr.Discard())

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "kos"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &v1alpha1.ScalewayNodeClass{}
	_ = kc.Get(context.Background(), types.NamespacedName{Name: "kos"}, got)
	if len(got.Status.UnusedFieldsForBackend) != 0 {
		t.Errorf("expected no unused fields for kosmos, got %v", got.Status.UnusedFieldsForBackend)
	}
}

// ResolvedZones defaults to the full 9-zone list when spec.zones is empty.
func TestReconcile_ResolvedZonesDefaults(t *testing.T) {
	nc := &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default-zones"},
		Spec: v1alpha1.ScalewayNodeClassSpec{
			ImageLabel: "ubuntu_jammy",
		},
	}
	kc := newClient(t, nc)
	mkt := &fakeMarketplace{imageID: "img-uuid"}
	r := New(kc, mkt, backend.KindKosmos, logr.Discard())

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "default-zones"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &v1alpha1.ScalewayNodeClass{}
	_ = kc.Get(context.Background(), types.NamespacedName{Name: "default-zones"}, got)
	if len(got.Status.ResolvedZones) != 9 {
		t.Fatalf("expected 9 resolved zones, got %v", got.Status.ResolvedZones)
	}
	if got.Status.ResolvedZones[0] != "fr-par-1" {
		t.Errorf("expected fr-par-1 first, got %s", got.Status.ResolvedZones[0])
	}
}

// ResolvedZones echoes spec.zones when explicit.
func TestReconcile_ResolvedZonesExplicit(t *testing.T) {
	nc := &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "explicit-zones"},
		Spec: v1alpha1.ScalewayNodeClassSpec{
			Zones:      []string{"fr-par-2", "nl-ams-1"},
			ImageLabel: "ubuntu_jammy",
		},
	}
	kc := newClient(t, nc)
	mkt := &fakeMarketplace{imageID: "img-uuid"}
	r := New(kc, mkt, backend.KindKosmos, logr.Discard())

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "explicit-zones"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &v1alpha1.ScalewayNodeClass{}
	_ = kc.Get(context.Background(), types.NamespacedName{Name: "explicit-zones"}, got)
	if len(got.Status.ResolvedZones) != 2 || got.Status.ResolvedZones[0] != "fr-par-2" || got.Status.ResolvedZones[1] != "nl-ams-1" {
		t.Fatalf("expected [fr-par-2 nl-ams-1], got %v", got.Status.ResolvedZones)
	}
}

// Zone not in catalog: Ready=False/ZoneUnknown. Uses the test-only override
// of knownZonesFromCatalog because the embedded catalog.json is the empty
// placeholder until P11 lands.
func TestReconcile_ZoneUnknown(t *testing.T) {
	prev := knownZonesFromCatalog
	knownZonesFromCatalog = func() map[string]struct{} {
		return map[string]struct{}{"fr-par-1": {}}
	}
	t.Cleanup(func() { knownZonesFromCatalog = prev })

	nc := newNodeClass("bad-zone", "fr-par-2", "ubuntu_jammy")
	kc := newClient(t, nc)
	mkt := &fakeMarketplace{imageID: "img-uuid"}
	r := New(kc, mkt, backend.KindKosmos, logr.Discard())

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "bad-zone"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &v1alpha1.ScalewayNodeClass{}
	_ = kc.Get(context.Background(), types.NamespacedName{Name: "bad-zone"}, got)
	cond := conditionFor(got, opstatus.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != v1alpha1.ConditionReasonZoneUnknown {
		t.Errorf("expected Ready=False/ZoneUnknown, got %#v", cond)
	}
	if mkt.calls != 0 {
		t.Errorf("marketplace must not be called when zone is invalid, got %d calls", mkt.calls)
	}
}

// NotFound: deleted NodeClass results in no error and no requeue.
func TestReconcile_NotFound(t *testing.T) {
	kc := newClient(t)
	r := New(kc, &fakeMarketplace{}, backend.KindKosmos, logr.Discard())
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "ghost"}})
	if err != nil {
		t.Fatalf("expected no error on NotFound, got %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no requeue, got %v", res.RequeueAfter)
	}
}
