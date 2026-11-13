package kapsule

import (
	"context"
	"errors"
	"testing"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/pkg/pricing"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	"github.com/go-logr/logr"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func newPricing() *pricing.Provider {
	return pricing.NewProvider(&pricing.FakeAPI{}, nil)
}

func mkNCForCreate(name, instanceType, zone string) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: v1alpha1.GroupVersion.Group,
				Kind:  "ScalewayNodeClass",
				Name:  "default",
			},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.LabelInstanceTypeStable, Operator: corev1.NodeSelectorOpIn, Values: []string{instanceType}},
				{Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpIn, Values: []string{zone}},
			},
		},
	}
}

func mkNodeClass(zones ...string) *v1alpha1.ScalewayNodeClass {
	return &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       v1alpha1.ScalewayNodeClassSpec{Zones: zones},
	}
}

func newKapsuleWithQuota(t *testing.T, api *fakeKapsule, quota QuotaConfig, objs ...any) *Kapsule {
	t.Helper()
	scheme := newScheme(t)
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, obj := range objs {
		switch o := obj.(type) {
		case *v1alpha1.ScalewayNodeClass:
			builder = builder.WithObjects(o)
		default:
			t.Fatalf("unsupported object %T", obj)
		}
	}
	kc := builder.Build()
	bk, err := New(Options{
		Kapsule:    api,
		KubeClient: kc,
		Pricing:    newPricing(),
		Quota:      quota,
		Log:        logr.Discard(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return bk
}

// counterValue extracts the float value out of a Prometheus counter for a
// deterministic before/after delta in tests. This is the simplest way to
// assert monotonic increments without restarting the global registry.
func counterValue(t *testing.T) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := poolQuotaRejections.Write(m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if m.Counter == nil || m.Counter.Value == nil {
		return 0
	}
	return *m.Counter.Value
}

// gaugeRemainingValue reads the current Kapsule pool quota remaining gauge.
func gaugeRemainingValue(t *testing.T) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := poolQuotaRemaining.Write(m); err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	if m.Gauge == nil || m.Gauge.Value == nil {
		return 0
	}
	return *m.Gauge.Value
}

func TestQuotaConfig_Defaults(t *testing.T) {
	t.Setenv(envPoolQuota, "")
	t.Setenv(envPoolQuotaThreshold, "")
	cfg := LoadQuotaConfig(logr.Discard())
	if cfg.Limit != defaultPoolQuota || cfg.Threshold != defaultPoolQuotaThreshold {
		t.Fatalf("defaults: got %+v", cfg)
	}
}

func TestQuotaConfig_OverridesFromEnv(t *testing.T) {
	t.Setenv(envPoolQuota, "200")
	t.Setenv(envPoolQuotaThreshold, "180")
	cfg := LoadQuotaConfig(logr.Discard())
	if cfg.Limit != 200 || cfg.Threshold != 180 {
		t.Fatalf("env overrides: got %+v", cfg)
	}
}

// Documented clamp behaviour: a misconfigured threshold (>= limit) gets
// clamped rather than rejected so a typo never wedges the controller at
// boot.
func TestQuotaConfig_ThresholdClampedWhenAboveLimit(t *testing.T) {
	t.Setenv(envPoolQuota, "150")
	t.Setenv(envPoolQuotaThreshold, "200")
	cfg := LoadQuotaConfig(logr.Discard())
	if cfg.Threshold != 149 {
		t.Fatalf("expected clamp to 149, got %d", cfg.Threshold)
	}
}

func TestQuotaConfig_NegativeOrJunkIgnored(t *testing.T) {
	t.Setenv(envPoolQuota, "-5")
	t.Setenv(envPoolQuotaThreshold, "abc")
	cfg := LoadQuotaConfig(logr.Discard())
	if cfg.Limit != defaultPoolQuota || cfg.Threshold != defaultPoolQuotaThreshold {
		t.Fatalf("expected defaults on junk, got %+v", cfg)
	}
}

// At threshold (140 by default), Create must reject before reaching CreatePool.
// The rejection bumps the rejections_total counter and surfaces the typed
// ErrQuotaExceeded so callers / dashboards can attribute the cause.
func TestCreate_RejectedAtThreshold(t *testing.T) {
	api := &fakeKapsule{
		countTotal: 140,
		countKarp:  120,
	}
	bk := newKapsuleWithQuota(t, api, QuotaConfig{Limit: 150, Threshold: 140},
		mkNodeClass("fr-par-1"))

	before := counterValue(t)
	_, err := bk.Create(context.Background(), mkNCForCreate("nc-saturated", "GP1-XS", "fr-par-1"))
	if !IsQuotaExceeded(err) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}
	if api.createPoolCalls != 0 {
		t.Fatalf("expected no CreatePool call, got %d", api.createPoolCalls)
	}
	if got := counterValue(t); got != before+1 {
		t.Fatalf("rejections counter: before=%v after=%v", before, got)
	}
	if got := gaugeRemainingValue(t); got != 10 {
		t.Fatalf("remaining gauge: want 10 got %v", got)
	}
}

// One pool above the threshold (already in saturation) — the counter still
// climbs, the rejection still fires, and the remaining gauge goes negative
// to make the over-quota state observable.
func TestCreate_RejectedAboveLimit(t *testing.T) {
	api := &fakeKapsule{
		countTotal: 152,
		countKarp:  140,
	}
	bk := newKapsuleWithQuota(t, api, QuotaConfig{Limit: 150, Threshold: 140},
		mkNodeClass("fr-par-1"))

	_, err := bk.Create(context.Background(), mkNCForCreate("nc-over", "GP1-XS", "fr-par-1"))
	if !IsQuotaExceeded(err) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}
	if got := gaugeRemainingValue(t); got != -2 {
		t.Fatalf("remaining gauge: want -2 got %v", got)
	}
}

// Below the threshold, Create proceeds and the gauge tracks the headroom.
// CreatePool is reached and only fails because the fake doesn't implement
// it; we assert on createPoolCalls instead of the final NodeClaim to keep
// the test independent of the WaitForNodeReady scaffolding.
func TestCreate_AllowedBelowThreshold(t *testing.T) {
	api := &fakeKapsule{
		countTotal: 100,
		countKarp:  100,
		createPoolFn: func(_ context.Context, _ scaleway.CreatePoolParams) (*scaleway.KapsulePool, error) {
			return nil, errors.New("createpool: stop here, we only assert preflight")
		},
	}
	bk := newKapsuleWithQuota(t, api, QuotaConfig{Limit: 150, Threshold: 140},
		mkNodeClass("fr-par-1"))

	_, err := bk.Create(context.Background(), mkNCForCreate("nc-ok", "GP1-XS", "fr-par-1"))
	if IsQuotaExceeded(err) {
		t.Fatalf("did not expect quota rejection, got %v", err)
	}
	if api.createPoolCalls != 1 {
		t.Fatalf("expected CreatePool reached, got calls=%d", api.createPoolCalls)
	}
	if got := gaugeRemainingValue(t); got != 50 {
		t.Fatalf("remaining gauge: want 50 got %v", got)
	}
}

// CountPools transient failure must NOT block provisioning — the controller
// degrades to "no observability this cycle" but still proceeds. Otherwise a
// Scaleway 5xx flake on the count path would also wedge Create.
func TestCreate_CountFailureFallsThroughBestEffort(t *testing.T) {
	api := &fakeKapsule{
		countErr: errors.New("503"),
		createPoolFn: func(_ context.Context, _ scaleway.CreatePoolParams) (*scaleway.KapsulePool, error) {
			return nil, errors.New("createpool: stop here")
		},
	}
	bk := newKapsuleWithQuota(t, api, QuotaConfig{Limit: 150, Threshold: 140},
		mkNodeClass("fr-par-1"))

	_, err := bk.Create(context.Background(), mkNCForCreate("nc-flake", "GP1-XS", "fr-par-1"))
	if IsQuotaExceeded(err) {
		t.Fatalf("did not expect quota rejection, got %v", err)
	}
	if api.createPoolCalls != 1 {
		t.Fatalf("expected CreatePool to be reached even on count failure, got %d", api.createPoolCalls)
	}
}

// Defensive: zero-valued Options.Quota must boot with documented defaults.
// The factory wires LoadQuotaConfig but unit-test users may construct via
// New() directly with an empty struct.
func TestNew_ZeroQuotaDefaults(t *testing.T) {
	bk, err := New(Options{
		Kapsule:    &fakeKapsule{},
		KubeClient: fake.NewClientBuilder().WithScheme(newScheme(t)).Build(),
		Pricing:    newPricing(),
		Log:        logr.Discard(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if bk.quota.Limit != defaultPoolQuota || bk.quota.Threshold != defaultPoolQuotaThreshold {
		t.Fatalf("zero options must default, got %+v", bk.quota)
	}
}

// QuotaConfig.check is the inner contract — exercise threshold equality
// (which counts as exceeded so in-flight Create races stay within Limit).
func TestQuotaCheck_BoundaryConditions(t *testing.T) {
	q := QuotaConfig{Limit: 150, Threshold: 140}
	if err := q.check(139); err != nil {
		t.Fatalf("139 must pass: %v", err)
	}
	if err := q.check(140); !IsQuotaExceeded(err) {
		t.Fatalf("140 must fail: %v", err)
	}
}
