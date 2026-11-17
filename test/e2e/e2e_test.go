//go:build e2e
// +build e2e

// Package e2e exercises the NodeClass reconciler against an
// in-process kube-apiserver provided by controller-runtime's envtest, with
// the Scaleway Marketplace HTTP API mocked via httptest.
//
// This is the same pattern the upstream sigs.k8s.io/karpenter project and
// the AWS / AKS providers use for their PR-time integration suites:
// no Docker, no kind, no real cloud, but a real apiserver so we exercise
// the actual CRD validation, status subresource patching, and the
// reconciler's controller-runtime wiring.
//
// Scope and out-of-scope are documented in test/e2e/README.md.
//
// To run:
//
//	./test/e2e/run.sh
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/pkg/backend"
	nodeclasscontroller "github.com/no-inspi/karpenter-scaleway/pkg/controllers/nodeclass"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"
	scwscheme "github.com/no-inspi/karpenter-scaleway/pkg/scheme"

	opstatus "github.com/awslabs/operatorpkg/status"
	"github.com/go-logr/logr"
	"github.com/scaleway/scaleway-sdk-go/scw"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const (
	// pollTimeout is how long the tests wait for the reconciler to author
	// a condition. envtest is fast; a few seconds is plenty.
	pollTimeout  = 10 * time.Second
	pollInterval = 100 * time.Millisecond
)

// testCfg is the in-process apiserver rest.Config shared by every test.
// Set up once in TestMain so envtest startup (a few seconds cold) is
// amortised across the whole suite.
var testCfg *rest.Config

// TestMain owns the envtest lifecycle. envtest.Start downloads nothing on
// its own — KUBEBUILDER_ASSETS must point at the apiserver/etcd binaries
// (run.sh fetches them via setup-envtest).
func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Fprintln(os.Stderr,
			"KUBEBUILDER_ASSETS is empty — run via test/e2e/run.sh "+
				"or export KUBEBUILDER_ASSETS=$(setup-envtest use 1.34.0 -p path).")
		os.Exit(1)
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "charts", "karpenter-scaleway", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest start: %v\n", err)
		os.Exit(1)
	}
	testCfg = cfg

	code := m.Run()

	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "envtest stop: %v\n", err)
	}
	os.Exit(code)
}

// testEnv carries the per-test plumbing each test reuses: a running
// controller-runtime manager, kube clients, the mock Marketplace server,
// and the cancel that tears them down.
//
// Two kube clients on purpose:
//   - kc is the manager's cached client; the reconciler reads through it.
//   - reader is an uncached APIReader; tests poll status through it so
//     `waitForCondition` doesn't depend on the cache being warm and bypass
//     a class of cache-sync deadlocks.
type testEnv struct {
	t       *testing.T
	cancel  context.CancelFunc
	kc      client.Client
	reader  client.Reader
	mock    *marketplaceMock
	mockSrv *httptest.Server
	stopCh  chan error
}

func setup(t *testing.T, kind backend.Kind) *testEnv {
	t.Helper()

	mock := newMarketplaceMock()
	mockSrv := httptest.NewServer(mock)
	t.Cleanup(mockSrv.Close)

	// Access keys must match the SDK's client-side format checks. The
	// secret key must be a UUIDv4. None of these is sent anywhere real.
	scwClient, err := scw.NewClient(
		scw.WithAuth("SCW0000000000000FAKE", "00000000-0000-4000-8000-000000000000"),
		scw.WithDefaultProjectID("00000000-0000-4000-8000-000000000000"),
		scw.WithAPIURL(mockSrv.URL),
	)
	if err != nil {
		t.Fatalf("scw client: %v", err)
	}
	mkt := scaleway.NewMarketplaceClient(scwClient)

	skip := true
	mgr, err := manager.New(testCfg, manager.Options{
		Scheme: scwscheme.New(),
		// LeaderElection deliberately disabled and the http servers bind
		// to "0" so multiple subtests don't fight over ports.
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// Controller-runtime tracks controller names in a process-global
		// registry; without SkipNameValidation, the second test fails on
		// re-registration with the same name.
		Controller: config.Controller{SkipNameValidation: &skip},
	})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}

	rec := nodeclasscontroller.New(mgr.GetClient(), mkt, kind, logr.Discard())
	if err := rec.SetupWithManager(mgr); err != nil {
		t.Fatalf("setup reconciler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stopCh := make(chan error, 1)
	go func() { stopCh <- mgr.Start(ctx) }()

	return &testEnv{
		t:       t,
		cancel:  cancel,
		kc:      mgr.GetClient(),
		reader:  mgr.GetAPIReader(),
		mock:    mock,
		mockSrv: mockSrv,
		stopCh:  stopCh,
	}
}

// cleanupNodeClass removes the named NodeClass on test exit so subsequent
// runs see a clean slate. Delete is idempotent — a previous failed run
// may have left residue.
func (e *testEnv) cleanupNodeClass(name string) {
	e.t.Cleanup(func() {
		nc := &v1alpha1.ScalewayNodeClass{ObjectMeta: metav1.ObjectMeta{Name: name}}
		_ = e.kc.Delete(context.Background(), nc)
	})
}

// applyNodeClass creates the NodeClass, deleting any existing one with the
// same name first. Necessary if a previous failed run left residue.
func (e *testEnv) applyNodeClass(nc *v1alpha1.ScalewayNodeClass) {
	e.t.Helper()
	existing := &v1alpha1.ScalewayNodeClass{}
	if err := e.reader.Get(context.Background(), types.NamespacedName{Name: nc.Name}, existing); err == nil {
		_ = e.kc.Delete(context.Background(), existing)
		deadline := time.Now().Add(5 * time.Second)
		for {
			if err := e.reader.Get(context.Background(), types.NamespacedName{Name: nc.Name}, existing); err != nil {
				break
			}
			if time.Now().After(deadline) {
				e.t.Fatalf("timeout waiting for delete of stale %s", nc.Name)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if err := e.kc.Create(context.Background(), nc); err != nil {
		e.t.Fatalf("create %s: %v", nc.Name, err)
	}
}

// waitForCondition polls until cond evaluates true on the named NodeClass
// or pollTimeout elapses. Uses the uncached APIReader to sidestep
// cache-sync timing entirely.
func (e *testEnv) waitForCondition(name string, cond func(*v1alpha1.ScalewayNodeClass) bool) *v1alpha1.ScalewayNodeClass {
	e.t.Helper()
	deadline := time.Now().Add(pollTimeout)
	for {
		got := &v1alpha1.ScalewayNodeClass{}
		err := e.reader.Get(context.Background(), types.NamespacedName{Name: name}, got)
		if err == nil && cond(got) {
			return got
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("timeout waiting for condition on %s (last err=%v, status=%+v)", name, err, got.Status)
		}
		time.Sleep(pollInterval)
	}
}

func conditionReady(nc *v1alpha1.ScalewayNodeClass) *opstatus.Condition {
	for i := range nc.Status.Conditions {
		if nc.Status.Conditions[i].Type == opstatus.ConditionReady {
			return &nc.Status.Conditions[i]
		}
	}
	return nil
}

// TestE2E_NodeClassReady exercises the happy path: Marketplace returns a
// hit, the reconciler writes Ready=True with the resolved image UUID.
func TestE2E_NodeClassReady(t *testing.T) {
	env := setup(t, backend.KindKosmos)

	env.mock.responses["fr-par-1/ubuntu_jammy"] = mockResponse{
		images: []localImage{
			{ID: "image-uuid-1", Compatible: []string{"GP1-XS"}, Zone: "fr-par-1"},
		},
	}

	nc := &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-ready"},
		Spec: v1alpha1.ScalewayNodeClassSpec{
			Zones:      []string{"fr-par-1"},
			ImageLabel: "ubuntu_jammy",
		},
	}
	env.cleanupNodeClass(nc.Name)
	env.applyNodeClass(nc)

	got := env.waitForCondition(nc.Name, func(nc *v1alpha1.ScalewayNodeClass) bool {
		c := conditionReady(nc)
		return c != nil && c.Status == metav1.ConditionTrue
	})
	if got.Status.ResolvedImageID != "image-uuid-1" {
		t.Errorf("expected ResolvedImageID=image-uuid-1, got %q", got.Status.ResolvedImageID)
	}
	if got.Status.ValidatedAt == nil {
		t.Error("expected ValidatedAt to be set")
	}
}

// TestE2E_ImageNotFound exercises the typo path: Marketplace returns no
// matching image; the reconciler must surface Ready=False/ImageNotFound.
func TestE2E_ImageNotFound(t *testing.T) {
	env := setup(t, backend.KindKosmos)

	// No entry registered in the mock for this label → empty response.
	nc := &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-typo"},
		Spec: v1alpha1.ScalewayNodeClassSpec{
			Zones:      []string{"fr-par-1"},
			ImageLabel: "ubuntu_jammyy", // typo
		},
	}
	env.cleanupNodeClass(nc.Name)
	env.applyNodeClass(nc)

	got := env.waitForCondition(nc.Name, func(nc *v1alpha1.ScalewayNodeClass) bool {
		c := conditionReady(nc)
		return c != nil && c.Status == metav1.ConditionFalse && c.Reason == v1alpha1.ConditionReasonImageNotFound
	})
	if got.Status.ResolvedImageID != "" {
		t.Errorf("expected empty ResolvedImageID on ImageNotFound, got %q", got.Status.ResolvedImageID)
	}
}

// TestE2E_ValidationPending exercises the transient-error path: the
// reconciler should surface Ready=False/ValidationPending.
func TestE2E_ValidationPending(t *testing.T) {
	env := setup(t, backend.KindKosmos)

	env.mock.responses["fr-par-1/ubuntu_jammy"] = mockResponse{httpStatus: http.StatusServiceUnavailable}

	nc := &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-transient"},
		Spec: v1alpha1.ScalewayNodeClassSpec{
			Zones:      []string{"fr-par-1"},
			ImageLabel: "ubuntu_jammy",
		},
	}
	env.cleanupNodeClass(nc.Name)
	env.applyNodeClass(nc)

	env.waitForCondition(nc.Name, func(nc *v1alpha1.ScalewayNodeClass) bool {
		c := conditionReady(nc)
		return c != nil && c.Status == metav1.ConditionFalse && c.Reason == v1alpha1.ConditionReasonValidationPending
	})
}

// TestE2E_KapsuleUnusedFields verifies the kapsule-resolved reconciler
// flags kosmos-only fields under status.unusedFieldsForBackend.
func TestE2E_KapsuleUnusedFields(t *testing.T) {
	env := setup(t, backend.KindKapsule)

	env.mock.responses["fr-par-1/ubuntu_jammy"] = mockResponse{
		images: []localImage{
			{ID: "image-uuid-2", Compatible: []string{"GP1-XS"}, Zone: "fr-par-1"},
		},
	}

	size := int32(50)
	pub := true
	nc := &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-kapsule"},
		Spec: v1alpha1.ScalewayNodeClassSpec{
			Zones:            []string{"fr-par-1"},
			ImageLabel:       "ubuntu_jammy",
			RootVolumeSizeGB: &size,
			RootVolumeType:   "sbs_volume",
			PublicIP:         &pub,
		},
	}
	env.cleanupNodeClass(nc.Name)
	env.applyNodeClass(nc)

	got := env.waitForCondition(nc.Name, func(nc *v1alpha1.ScalewayNodeClass) bool {
		return len(nc.Status.UnusedFieldsForBackend) == 2
	})
	// rootVolumeSizeGB is applied to Kapsule pools, so it is not listed.
	want := map[string]bool{"rootVolumeType": true, "publicIP": true}
	for _, f := range got.Status.UnusedFieldsForBackend {
		if !want[f] {
			t.Errorf("unexpected field in unusedFieldsForBackend: %q", f)
		}
		delete(want, f)
	}
	if len(want) > 0 {
		t.Errorf("missing fields: %v", want)
	}
}

// --- mock Scaleway Marketplace --------------------------------------------

type localImage struct {
	ID         string
	Compatible []string
	Zone       string
}

type mockResponse struct {
	images     []localImage
	httpStatus int
}

// marketplaceMock answers Scaleway SDK calls for /marketplace/v2/local-images
// using a per-(zone, label) registry. Anything unregistered returns an
// empty list (which the reconciler interprets as ImageNotFound).
type marketplaceMock struct {
	responses map[string]mockResponse
}

func newMarketplaceMock() *marketplaceMock {
	return &marketplaceMock{responses: map[string]mockResponse{}}
}

func (m *marketplaceMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/marketplace/v2/local-images") {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	zone := q.Get("zone")
	label := q.Get("image_label")
	key := zone + "/" + label
	resp, ok := m.responses[key]
	if !ok {
		writeImages(w, nil)
		return
	}
	if resp.httpStatus != 0 {
		http.Error(w, fmt.Sprintf("mock %d", resp.httpStatus), resp.httpStatus)
		return
	}
	writeImages(w, resp.images)
}

func writeImages(w http.ResponseWriter, imgs []localImage) {
	out := struct {
		LocalImages []map[string]any `json:"local_images"`
		TotalCount  uint32           `json:"total_count"`
	}{}
	for _, i := range imgs {
		out.LocalImages = append(out.LocalImages, map[string]any{
			"id":                          i.ID,
			"compatible_commercial_types": i.Compatible,
			"arch":                        "x86_64",
			"zone":                        i.Zone,
			"label":                       "ubuntu_jammy",
			"type":                        "instance_local",
		})
	}
	out.TotalCount = uint32(len(out.LocalImages))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		panic(errors.New("encode mock response: " + err.Error()))
	}
}
