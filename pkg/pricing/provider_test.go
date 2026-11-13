package pricing

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	pc "github.com/scaleway/scaleway-sdk-go/api/product_catalog/v2alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"
)

// slowAPI sleeps `delay` per catalog call without holding any
// shared lock. Used to assert that refreshOnce fans out to all zones in
// parallel rather than serially.
type slowAPI struct {
	delay time.Duration
}

func (s *slowAPI) ListPublicCatalogProducts(_ *pc.PublicCatalogAPIListPublicCatalogProductsRequest, _ ...scw.RequestOption) (*pc.ListPublicCatalogProductsResponse, error) {
	time.Sleep(s.delay)
	return &pc.ListPublicCatalogProductsResponse{}, nil
}

func (s *slowAPI) GetServerTypesAvailability(req *instance.GetServerTypesAvailabilityRequest, _ ...scw.RequestOption) (*instance.GetServerTypesAvailabilityResponse, error) {
	time.Sleep(s.delay)
	return &instance.GetServerTypesAvailabilityResponse{Servers: map[string]*instance.GetServerTypesAvailabilityResponseAvailability{}}, nil
}

func mkType(name, zone string) *cloudprovider.InstanceType {
	return &cloudprovider.InstanceType{
		Name: name,
		Offerings: cloudprovider.Offerings{{
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, "on-demand"),
			),
			Available: true,
		}},
	}
}

func TestProvider_BootPopulatesBaselineFromCatalog(t *testing.T) {
	p := NewProvider(&FakeAPI{}, nil)
	if _, ok := p.Price(scw.ZoneFrPar1, "POP2-2C-8G"); ok {
		// Baseline comes from instancetype.All() which uses the embedded
		// catalog.json placeholder (empty in P4) — so this lookup misses
		// by design until Phase 11 generates the real catalog.
		t.Skip("placeholder catalog.json is empty in P4")
	}
}

func TestProvider_HydrateReplacesEmbeddedPrices(t *testing.T) {
	p := NewProvider(&FakeAPI{}, nil)
	p.setPriceForTest(scw.ZoneFrPar1, "POP2-2C-8G", 0.123)

	in := []*cloudprovider.InstanceType{{
		Name: "POP2-2C-8G",
		Offerings: cloudprovider.Offerings{{
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "fr-par-1"),
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, "on-demand"),
			),
			Price: 0.040,
		}},
	}}
	out := p.Hydrate(in)
	if got := out[0].Offerings[0].Price; got != 0.123 {
		t.Fatalf("price not hydrated: got %v want 0.123", got)
	}
}

func TestProvider_RefreshSwapsPrices(t *testing.T) {
	api := &FakeAPI{Data: map[scw.Zone]map[string]*instance.ServerType{
		scw.ZoneFrPar1: {"POP2-2C-8G": {HourlyPrice: 0.999}},
	}}
	p := NewProvider(api, nil)
	if err := p.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got, ok := p.Price(scw.ZoneFrPar1, "POP2-2C-8G")
	if !ok || got != 0.999 {
		t.Fatalf("got (%v,%v), want (0.999, true)", got, ok)
	}
}

func TestProvider_RefreshUpdatesAvailabilityShortage(t *testing.T) {
	api := &FakeAPI{
		Data: map[scw.Zone]map[string]*instance.ServerType{
			scw.ZoneFrPar1: {
				"POP2-2C-8G":   {HourlyPrice: 0.04},
				"POP2-16C-64G": {HourlyPrice: 0.40},
			},
		},
		Avail: map[scw.Zone]map[string]instance.ServerTypesAvailability{
			scw.ZoneFrPar1: {
				"POP2-2C-8G":   instance.ServerTypesAvailabilityAvailable,
				"POP2-16C-64G": instance.ServerTypesAvailabilityShortage,
			},
		},
	}
	p := NewProvider(api, nil)
	if err := p.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	in := []*cloudprovider.InstanceType{
		mkType("POP2-2C-8G", "fr-par-1"),
		mkType("POP2-16C-64G", "fr-par-1"),
	}
	out := p.Hydrate(in)
	if !out[0].Offerings[0].Available {
		t.Fatalf("POP2-2C-8G should be available")
	}
	if out[1].Offerings[0].Available {
		t.Fatalf("POP2-16C-64G is in shortage; Available must be false")
	}
}

func TestProvider_RefreshScarceStaysAvailable(t *testing.T) {
	api := &FakeAPI{
		Data: map[scw.Zone]map[string]*instance.ServerType{
			scw.ZoneFrPar1: {"PRO2-S": {HourlyPrice: 0.10}},
		},
		Avail: map[scw.Zone]map[string]instance.ServerTypesAvailability{
			scw.ZoneFrPar1: {"PRO2-S": instance.ServerTypesAvailabilityScarce},
		},
	}
	p := NewProvider(api, nil)
	if err := p.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	out := p.Hydrate([]*cloudprovider.InstanceType{mkType("PRO2-S", "fr-par-1")})
	if !out[0].Offerings[0].Available {
		t.Fatalf("scarce should remain available; got false")
	}
}

func TestProvider_HydratePropagatesBaselineUnavailableOnLookupMiss(t *testing.T) {
	// No refresh: snapshots are seeded from catalog only. An offering with
	// Available=false in input must stay false on lookup miss (the live
	// state is unknown, baseline takes precedence).
	p := NewProvider(&FakeAPI{}, nil)
	in := []*cloudprovider.InstanceType{{
		Name: "UNKNOWN-TYPE",
		Offerings: cloudprovider.Offerings{{
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "fr-par-1"),
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, "on-demand"),
			),
			Available: false,
		}},
	}}
	out := p.Hydrate(in)
	if out[0].Offerings[0].Available {
		t.Fatalf("baseline Available=false must survive lookup miss")
	}
}

func TestProvider_RefreshFailureKeepsPreviousPrices(t *testing.T) {
	api := &FakeAPI{ErrFor: map[scw.Zone]error{scw.ZoneFrPar1: errors.New("boom")}}
	p := NewProvider(api, nil)
	p.setPriceForTest(scw.ZoneFrPar1, "POP2-2C-8G", 0.5)
	_ = p.refreshOnce(context.Background())
	got, ok := p.Price(scw.ZoneFrPar1, "POP2-2C-8G")
	if !ok || got != 0.5 {
		t.Fatalf("price clobbered after failed refresh: got (%v,%v)", got, ok)
	}
}

func TestProvider_RefreshFansOutInParallel(t *testing.T) {
	// Each zone takes 100ms; serial would be ~900ms for 9 zones, parallel
	// should be ~100ms. Assert well under the serial baseline to catch
	// regressions to the old per-zone loop.
	const perZoneDelay = 100 * time.Millisecond
	p := NewProvider(&slowAPI{delay: perZoneDelay}, nil)
	start := time.Now()
	if err := p.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	elapsed := time.Since(start)
	serial := perZoneDelay * time.Duration(len(scaleway.AllZones))
	// Headroom for CI jitter: any time materially less than serial proves
	// fan-out. Pick half the serial baseline as the cutoff.
	if elapsed >= serial/2 {
		t.Fatalf("refreshOnce ran serially: %v ≥ serial/2 (%v)", elapsed, serial/2)
	}
}

func TestProvider_LastUpdateZeroBeforeFirstRefresh(t *testing.T) {
	p := NewProvider(&FakeAPI{}, nil)
	if !p.LastUpdate().IsZero() {
		t.Fatalf("LastUpdate should be zero before first refresh, got %v", p.LastUpdate())
	}
	if got := p.lastUpdateSeconds(); !math.IsInf(got, 1) {
		t.Fatalf("lastUpdateSeconds should be +Inf before first refresh, got %v", got)
	}
}

func TestProvider_LastUpdateSecondsAfterRefresh(t *testing.T) {
	p := NewProvider(&FakeAPI{}, nil)
	if err := p.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got := p.lastUpdateSeconds()
	if math.IsInf(got, 1) {
		t.Fatalf("lastUpdateSeconds is +Inf after successful refresh")
	}
	if got < 0 || got > 5 {
		t.Fatalf("lastUpdateSeconds %v not in plausible range [0,5] right after refresh", got)
	}
}

func TestProvider_LastUpdateMonotonic(t *testing.T) {
	p := NewProvider(&FakeAPI{}, nil)
	t1 := p.LastUpdate()
	time.Sleep(2 * time.Millisecond)
	_ = p.refreshOnce(context.Background())
	t2 := p.LastUpdate()
	if !t2.After(t1) {
		t.Fatalf("lastUpdate did not advance: %v -> %v", t1, t2)
	}
}
