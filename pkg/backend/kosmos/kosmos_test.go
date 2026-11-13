package kosmos

import (
	"context"
	"errors"
	"testing"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/pkg/backend/drift"
	"github.com/no-inspi/karpenter-scaleway/pkg/pricing"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	"github.com/go-logr/logr"
	"github.com/scaleway/scaleway-sdk-go/scw"
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

// fakeInstance is a hand-rolled scaleway.InstanceAPI fake covering the
// behaviors the kosmos backend exercises in tests.
type fakeInstance struct {
	deleteCalls  []string
	deleteZones  []scw.Zone
	deleteErr    error
	listResp     []*scaleway.ScalewayInstance
	listErr      error
	getResp      *scaleway.ScalewayInstance
	getErr       error
	createParams *scaleway.CreateInstanceParams
	createResp   *scaleway.ScalewayInstance
	createErr    error
}

func (f *fakeInstance) Create(_ context.Context, p scaleway.CreateInstanceParams) (*scaleway.ScalewayInstance, error) {
	f.createParams = &p
	if f.createResp != nil && f.createResp.Zone == "" {
		// Mirror real client behavior — populate Zone from params.
		f.createResp.Zone = p.Zone
	}
	return f.createResp, f.createErr
}
func (f *fakeInstance) Delete(_ context.Context, zone scw.Zone, id string) error {
	f.deleteZones = append(f.deleteZones, zone)
	f.deleteCalls = append(f.deleteCalls, id)
	return f.deleteErr
}
func (f *fakeInstance) Get(_ context.Context, _ scw.Zone, _ string) (*scaleway.ScalewayInstance, error) {
	return f.getResp, f.getErr
}
func (f *fakeInstance) List(_ context.Context) ([]*scaleway.ScalewayInstance, error) {
	return f.listResp, f.listErr
}

type fakeK8s struct {
	getResp *scaleway.Cluster
	getErr  error
}

func (f *fakeK8s) GetCluster(_ context.Context, _ string) (*scaleway.Cluster, error) {
	return f.getResp, f.getErr
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1 AddToScheme: %v", err)
	}
	// karpv1 doesn't expose AddToScheme — register manually.
	karpGV := schema.GroupVersion{Group: karpapi.Group, Version: "v1"}
	metav1.AddToGroupVersion(s, karpGV)
	s.AddKnownTypes(karpGV, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{}, &karpv1.NodePool{}, &karpv1.NodePoolList{})
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("v1alpha1 AddToScheme: %v", err)
	}
	return s
}

func newKosmos(t *testing.T, instAPI scaleway.InstanceAPI, k8sAPI scaleway.K8sAPI, kc client.Client) *Kosmos {
	t.Helper()
	bk, err := New(Options{
		Instance:   instAPI,
		K8s:        k8sAPI,
		KubeClient: kc,
		ClusterID:  "cluster-uuid",
		Pricing:    pricing.NewProvider(&pricing.FakeAPI{}, nil),
		Log:        logr.Discard(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return bk
}

// Delete must return NodeClaimNotFoundError after a
// successful synchronous terminate. Returning nil leaves Karpenter
// retrying forever and the finalizer pinned.
func TestDelete_ReturnsNodeClaimNotFoundOnSuccess(t *testing.T) {
	inst := &fakeInstance{}
	k8s := &fakeK8s{}
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	bk := newKosmos(t, inst, k8s, kc)

	nc := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "nc-1",
			Annotations: map[string]string{InstanceIDAnnotationKey: "inst-uuid"},
			Labels:      map[string]string{corev1.LabelTopologyZone: "fr-par-1"},
		},
	}
	err := bk.Delete(context.Background(), nc)
	if err == nil {
		t.Fatalf("expected NodeClaimNotFoundError, got nil")
	}
	if !karpcp.IsNodeClaimNotFoundError(err) {
		t.Fatalf("expected NodeClaimNotFoundError, got %T %v", err, err)
	}
	if len(inst.deleteCalls) != 1 || inst.deleteCalls[0] != "inst-uuid" {
		t.Fatalf("expected one Delete(inst-uuid), got %v", inst.deleteCalls)
	}
	if len(inst.deleteZones) != 1 || inst.deleteZones[0] != scw.Zone("fr-par-1") {
		t.Fatalf("expected zone fr-par-1, got %v", inst.deleteZones)
	}
}

// Delete with no annotation and no matching tag must still return
// NodeClaimNotFoundError so Karpenter clears the finalizer.
func TestDelete_NoUnderlyingInstance(t *testing.T) {
	inst := &fakeInstance{listResp: nil}
	k8s := &fakeK8s{}
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	bk := newKosmos(t, inst, k8s, kc)

	nc := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc-2"}}
	err := bk.Delete(context.Background(), nc)
	if !karpcp.IsNodeClaimNotFoundError(err) {
		t.Fatalf("expected NodeClaimNotFoundError, got %v", err)
	}
	if len(inst.deleteCalls) != 0 {
		t.Fatalf("expected no Delete calls, got %v", inst.deleteCalls)
	}
}

// Delete falls back to nodeclaim=<name> tag scan when annotation is missing.
// In that path the zone must be resolved from the listed instance.
func TestDelete_FallsBackToTagLookup(t *testing.T) {
	inst := &fakeInstance{
		listResp: []*scaleway.ScalewayInstance{
			{ID: "inst-other", NodeClaim: "other-nc", Zone: scw.ZoneFrPar2},
			{ID: "inst-target", NodeClaim: "nc-3", Zone: scw.ZoneFrPar1},
		},
	}
	k8s := &fakeK8s{}
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	bk := newKosmos(t, inst, k8s, kc)

	nc := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc-3"}}
	err := bk.Delete(context.Background(), nc)
	if !karpcp.IsNodeClaimNotFoundError(err) {
		t.Fatalf("expected NodeClaimNotFoundError, got %v", err)
	}
	if len(inst.deleteCalls) != 1 || inst.deleteCalls[0] != "inst-target" {
		t.Fatalf("expected Delete(inst-target), got %v", inst.deleteCalls)
	}
	if len(inst.deleteZones) != 1 || inst.deleteZones[0] != scw.ZoneFrPar1 {
		t.Fatalf("expected zone fr-par-1 from listed instance, got %v", inst.deleteZones)
	}
}

// Get must correlate providerID → kube Node → Instance
// via the externalIP, not via the Kosmos providerID UUID.
func TestGet_CorrelatesByPublicIP(t *testing.T) {
	inst := &fakeInstance{
		listResp: []*scaleway.ScalewayInstance{
			{ID: "inst-1", PublicIP: "203.0.113.5", Type: "GP1-XS", NodeClaim: "nc-x"},
			{ID: "inst-2", PublicIP: "203.0.113.6", Type: "GP1-XS", NodeClaim: "nc-y"},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "scw-node-1"},
		Spec:       corev1.NodeSpec{ProviderID: "scaleway://external/unknown/aaaa"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeExternalIP, Address: "203.0.113.6"}},
		},
	}
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(node).Build()
	bk := newKosmos(t, inst, &fakeK8s{}, kc)

	got, err := bk.Get(context.Background(), "scaleway://external/unknown/aaaa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Annotations[InstanceIDAnnotationKey] != "inst-2" {
		t.Fatalf("expected inst-2 (matched on 203.0.113.6), got %v", got.Annotations)
	}
}

// Get returns NodeClaimNotFoundError when no Instance matches the externalIP.
func TestGet_NoMatchingInstance(t *testing.T) {
	inst := &fakeInstance{listResp: nil}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "scw-node-1"},
		Spec:       corev1.NodeSpec{ProviderID: "scaleway://external/unknown/aaaa"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeExternalIP, Address: "203.0.113.7"}},
		},
	}
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(node).Build()
	bk := newKosmos(t, inst, &fakeK8s{}, kc)

	_, err := bk.Get(context.Background(), "scaleway://external/unknown/aaaa")
	if !karpcp.IsNodeClaimNotFoundError(err) {
		t.Fatalf("expected NodeClaimNotFoundError, got %v", err)
	}
}

// Healthcheck must surface K8s API errors.
func TestHealthcheck(t *testing.T) {
	bk := newKosmos(t, &fakeInstance{}, &fakeK8s{getErr: errors.New("boom")},
		fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	if err := bk.Healthcheck(context.Background()); err == nil {
		t.Fatal("expected error from healthcheck, got nil")
	}
}

// --- IsDrifted ---

// mkKosmosNC mirrors what Create produces: instance-id annotation, zone
// label, instance-type label, plus the imageLabel snapshot. Tests mutate
// one field at a time. The matching NodeClass is built separately so its
// Spec.ImageLabel can drift away from the snapshot.
func mkKosmosNC(t *testing.T, name, instanceID, instanceType, zone, imageLabelSnapshot, nodeClassName string) *karpv1.NodeClaim {
	t.Helper()
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				InstanceIDAnnotationKey:               instanceID,
				drift.ImageLabelSnapshotAnnotationKey: imageLabelSnapshot,
			},
			Labels: map[string]string{
				corev1.LabelInstanceTypeStable: instanceType,
				corev1.LabelTopologyZone:       zone,
			},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{Name: nodeClassName},
		},
	}
}

func mkNodeClass(name, imageLabel string) *v1alpha1.ScalewayNodeClass {
	return &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.ScalewayNodeClassSpec{ImageLabel: imageLabel},
	}
}

// Happy path — instance, NodeClaim and NodeClass agree.
func TestKosmosIsDrifted_NoDrift(t *testing.T) {
	inst := &fakeInstance{
		getResp: &scaleway.ScalewayInstance{
			ID: "inst-1", NodeClaim: "nc-1", Type: "GP1-XS", Zone: scw.ZoneFrPar1,
		},
	}
	nodeClass := mkNodeClass("nc-class", "ubuntu_jammy")
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(nodeClass).Build()
	bk := newKosmos(t, inst, &fakeK8s{}, kc)

	reason, err := bk.IsDrifted(context.Background(),
		mkKosmosNC(t, "nc-1", "inst-1", "GP1-XS", "fr-par-1", "ubuntu_jammy", "nc-class"))
	if err != nil || reason != "" {
		t.Fatalf("expected no drift, got (%q, %v)", reason, err)
	}
}

// 404 → not drift; let Karpenter GC handle the gone instance.
func TestKosmosIsDrifted_InstanceGoneIsNotDrift(t *testing.T) {
	inst := &fakeInstance{getErr: notFoundErr()}
	nodeClass := mkNodeClass("nc-class", "ubuntu_jammy")
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(nodeClass).Build()
	bk := newKosmos(t, inst, &fakeK8s{}, kc)

	reason, err := bk.IsDrifted(context.Background(),
		mkKosmosNC(t, "nc-1", "inst-gone", "GP1-XS", "fr-par-1", "ubuntu_jammy", "nc-class"))
	if err != nil || reason != "" {
		t.Fatalf("expected no drift on 404, got (%q, %v)", reason, err)
	}
}

// Non-404 API error: surface so Karpenter retries instead of falsely
// proclaiming "not drifted".
func TestKosmosIsDrifted_APIErrorPropagates(t *testing.T) {
	inst := &fakeInstance{getErr: errors.New("503 service unavailable")}
	bk := newKosmos(t, inst, &fakeK8s{}, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	_, err := bk.IsDrifted(context.Background(),
		mkKosmosNC(t, "nc-1", "inst-1", "GP1-XS", "fr-par-1", "ubuntu_jammy", "nc-class"))
	if err == nil {
		t.Fatal("expected error from non-404 Get, got nil")
	}
}

// Out-of-band edit changed the Scaleway commercialType.
func TestKosmosIsDrifted_InstanceType(t *testing.T) {
	inst := &fakeInstance{
		getResp: &scaleway.ScalewayInstance{
			ID: "inst-1", NodeClaim: "nc-1", Type: "GP1-S", Zone: scw.ZoneFrPar1,
		},
	}
	nodeClass := mkNodeClass("nc-class", "ubuntu_jammy")
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(nodeClass).Build()
	bk := newKosmos(t, inst, &fakeK8s{}, kc)

	reason, err := bk.IsDrifted(context.Background(),
		mkKosmosNC(t, "nc-1", "inst-1", "GP1-XS", "fr-par-1", "ubuntu_jammy", "nc-class"))
	if err != nil || reason != drift.ReasonInstanceType {
		t.Fatalf("expected InstanceType drift, got (%q, %v)", reason, err)
	}
}

// Instance was migrated to a different zone.
func TestKosmosIsDrifted_Zone(t *testing.T) {
	inst := &fakeInstance{
		getResp: &scaleway.ScalewayInstance{
			ID: "inst-1", NodeClaim: "nc-1", Type: "GP1-XS", Zone: scw.ZoneFrPar2,
		},
	}
	nodeClass := mkNodeClass("nc-class", "ubuntu_jammy")
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(nodeClass).Build()
	bk := newKosmos(t, inst, &fakeK8s{}, kc)

	reason, err := bk.IsDrifted(context.Background(),
		mkKosmosNC(t, "nc-1", "inst-1", "GP1-XS", "fr-par-1", "ubuntu_jammy", "nc-class"))
	if err != nil || reason != drift.ReasonZone {
		t.Fatalf("expected Zone drift, got (%q, %v)", reason, err)
	}
}

// Correlation tag stripped — fail safe and recycle.
func TestKosmosIsDrifted_TagsMissing(t *testing.T) {
	inst := &fakeInstance{
		getResp: &scaleway.ScalewayInstance{
			ID: "inst-1", NodeClaim: "" /* tag was removed */, Type: "GP1-XS", Zone: scw.ZoneFrPar1,
		},
	}
	nodeClass := mkNodeClass("nc-class", "ubuntu_jammy")
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(nodeClass).Build()
	bk := newKosmos(t, inst, &fakeK8s{}, kc)

	reason, err := bk.IsDrifted(context.Background(),
		mkKosmosNC(t, "nc-1", "inst-1", "GP1-XS", "fr-par-1", "ubuntu_jammy", "nc-class"))
	if err != nil || reason != drift.ReasonTags {
		t.Fatalf("expected Tags drift, got (%q, %v)", reason, err)
	}
}

// NodeClass.Spec.ImageLabel changed after Create — drift.
func TestKosmosIsDrifted_ImageLabel(t *testing.T) {
	inst := &fakeInstance{
		getResp: &scaleway.ScalewayInstance{
			ID: "inst-1", NodeClaim: "nc-1", Type: "GP1-XS", Zone: scw.ZoneFrPar1,
		},
	}
	nodeClass := mkNodeClass("nc-class", "ubuntu_noble") // operator bumped distro
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(nodeClass).Build()
	bk := newKosmos(t, inst, &fakeK8s{}, kc)

	reason, err := bk.IsDrifted(context.Background(),
		mkKosmosNC(t, "nc-1", "inst-1", "GP1-XS", "fr-par-1", "ubuntu_jammy", "nc-class"))
	if err != nil || reason != drift.ReasonImageLabel {
		t.Fatalf("expected ImageLabel drift, got (%q, %v)", reason, err)
	}
}

// NodeClass deleted while NodeClaim still alive — IsDrifted returns
// ("", nil) so the NodeClass reconciler / Karpenter own the recovery
// path and we don't double-fire a drift signal.
func TestKosmosIsDrifted_MissingNodeClass(t *testing.T) {
	inst := &fakeInstance{
		getResp: &scaleway.ScalewayInstance{
			ID: "inst-1", NodeClaim: "nc-1", Type: "GP1-XS", Zone: scw.ZoneFrPar1,
		},
	}
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	bk := newKosmos(t, inst, &fakeK8s{}, kc)

	reason, err := bk.IsDrifted(context.Background(),
		mkKosmosNC(t, "nc-1", "inst-1", "GP1-XS", "fr-par-1", "ubuntu_jammy", "nc-class"))
	if err != nil || reason != "" {
		t.Fatalf("expected no drift when NodeClass is gone, got (%q, %v)", reason, err)
	}
}

// skip-drift annotation suppresses backend-side checks even when several
// real divergences exist simultaneously.
func TestKosmosIsDrifted_SkipAnnotation(t *testing.T) {
	inst := &fakeInstance{
		getResp: &scaleway.ScalewayInstance{
			ID: "inst-1", NodeClaim: "nc-1", Type: "GP1-S" /* drifted */, Zone: scw.ZoneFrPar2, /* drifted */
		},
	}
	bk := newKosmos(t, inst, &fakeK8s{}, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	nc := mkKosmosNC(t, "nc-1", "inst-1", "GP1-XS", "fr-par-1", "ubuntu_jammy", "nc-class")
	nc.Annotations[drift.SkipAnnotationKey] = "true"

	reason, err := bk.IsDrifted(context.Background(), nc)
	if err != nil || reason != "" {
		t.Fatalf("expected no drift (skipped), got (%q, %v)", reason, err)
	}
}

// Mid-create NodeClaim (no instance-id annotation yet) → not drift.
func TestKosmosIsDrifted_NoInstanceIDAnnotation(t *testing.T) {
	bk := newKosmos(t, &fakeInstance{}, &fakeK8s{}, fake.NewClientBuilder().WithScheme(newScheme(t)).Build())
	nc := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc-1"}}
	reason, err := bk.IsDrifted(context.Background(), nc)
	if err != nil || reason != "" {
		t.Fatalf("expected no drift, got (%q, %v)", reason, err)
	}
}

// Create writes the imageLabel snapshot annotation so drift checks
// can compare against the NodeClass post-create.
func TestKosmosCreate_PersistsImageLabelSnapshot(t *testing.T) {
	publicIP := "203.0.113.10"
	inst := &fakeInstance{
		createResp: &scaleway.ScalewayInstance{
			ID: "inst-1", PublicIP: publicIP, Zone: scw.ZoneFrPar1, Type: "GP1-XS",
		},
	}
	nodeClass := mkNodeClass("nc-class", "ubuntu_jammy")
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "scw-1"},
		Spec:       corev1.NodeSpec{ProviderID: "scaleway://external/unknown/aaaa"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeExternalIP, Address: publicIP}},
		},
	}
	kc := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(nodeClass, node).Build()
	bk := newKosmos(t, inst, &fakeK8s{}, kc)

	nc := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "nc-1"},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{Name: "nc-class"},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.LabelInstanceTypeStable, Operator: corev1.NodeSelectorOpIn, Values: []string{"GP1-XS"}},
				{Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpIn, Values: []string{"fr-par-1"}},
			},
		},
	}
	out, err := bk.Create(context.Background(), nc)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := out.Annotations[drift.ImageLabelSnapshotAnnotationKey]; got != "ubuntu_jammy" {
		t.Fatalf("expected snapshot ubuntu_jammy, got %q", got)
	}
}

// notFoundErr returns a Scaleway-shaped 404 the kosmos backend's drift
// path is expected to translate into a no-drift result.
func notFoundErr() error {
	return &scw.ResponseError{Status: "404 Not Found", StatusCode: 404}
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
