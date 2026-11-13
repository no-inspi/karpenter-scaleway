// Package pricing maintains the live per-zone, per-instance-type EUR/h
// prices consumed by Karpenter consolidation. The package mirrors
// karpenter-provider-azure pkg/providers/pricing: embedded baseline at
// boot, background refresh every 12h, last-known-good fallback on
// failure.
package pricing

import (
	"math"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	corev1 "k8s.io/api/core/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/no-inspi/karpenter-scaleway/pkg/instancetype"
)

// API is what the Provider depends on: stock availability from the Instance
// API and prices from the public Product Catalog API (the Instance API
// returns hourly_price=0 since 2026-11). NewScalewayAPI builds the real one;
// FakeAPI is the test double.
type API interface {
	GetServerTypesAvailability(*instance.GetServerTypesAvailabilityRequest, ...scw.RequestOption) (*instance.GetServerTypesAvailabilityResponse, error)
	CatalogAPI
}

// Provider keeps an in-memory map of EUR/h prices and a parallel map of
// per-zone stock availability, refreshed periodically. All methods are
// safe for concurrent use.
//
// prices and available are kept as separate maps rather than fused into a
// single struct value: refreshOnce builds them in parallel per zone and
// swaps both under the same lock, so a torn read across price+availability
// for the same offering is impossible without paying the cost of a struct
// allocation per (zone, type) pair.
type Provider struct {
	api API
	log logr.Logger

	mu         sync.RWMutex
	prices     map[scw.Zone]map[string]float64
	available  map[scw.Zone]map[string]bool
	lastUpdate time.Time
}

// NewProvider constructs a Provider seeded from instancetype.All() (the
// embedded baseline). Pass nil log for default discard.
//
// lastUpdate is zero until the first successful refresh: callers must
// treat the baseline catalog as "live data of unknown age", and the
// staleness gauge reports +Inf so alerts based on age don't go silent
// during the boot window.
func NewProvider(api API, log *logr.Logger) *Provider {
	l := logr.Discard()
	if log != nil {
		l = *log
	}
	p := &Provider{
		api:       api,
		log:       l,
		prices:    map[scw.Zone]map[string]float64{},
		available: map[scw.Zone]map[string]bool{},
	}
	p.seedFromCatalog()
	lastUpdateAge.Set(math.Inf(1))
	return p
}

func (p *Provider) seedFromCatalog() {
	for _, t := range instancetype.All() {
		for _, o := range t.Offerings {
			zone := zoneFromOffering(o)
			if zone == "" {
				continue
			}
			if p.prices[zone] == nil {
				p.prices[zone] = map[string]float64{}
			}
			if p.available[zone] == nil {
				p.available[zone] = map[string]bool{}
			}
			p.prices[zone][t.Name] = o.Price
			p.available[zone][t.Name] = o.Available
		}
	}
}

// zoneFromOffering reads topology.kubernetes.io/zone from the offering
// requirements and returns the first value as scw.Zone. Empty if absent.
func zoneFromOffering(o *karpcp.Offering) scw.Zone {
	if r := o.Requirements.Get(corev1.LabelTopologyZone); r != nil {
		for _, v := range r.Values() {
			return scw.Zone(v)
		}
	}
	return ""
}

// Price returns the current EUR/h for (zone, serverType). false if unknown.
func (p *Provider) Price(zone scw.Zone, serverType string) (float64, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if zm, ok := p.prices[zone]; ok {
		v, ok := zm[serverType]
		return v, ok
	}
	return 0, false
}

// LastUpdate reports the time of the most recent successful refresh.
// Returns the zero time if no refresh has succeeded yet.
func (p *Provider) LastUpdate() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastUpdate
}

// lastUpdateSeconds returns seconds since the last successful refresh,
// or +Inf if no refresh has succeeded yet. Used to drive the staleness
// gauge so "no refresh ever" reads as infinitely stale instead of
// "refreshed at boot, age 0s" — the latter would silence age-based
// alerts during the boot window.
func (p *Provider) lastUpdateSeconds() float64 {
	t := p.LastUpdate()
	if t.IsZero() {
		return math.Inf(1)
	}
	return time.Since(t).Seconds()
}

// Hydrate returns a deep copy of types with each Offering.Price and
// Offering.Available replaced by the live values. Lookup miss preserves
// the input value (baseline catalog).
//
// All offerings see the same generation of the price+availability maps:
// both snapshots are captured under a single RLock so a refresh swap
// mid-iteration cannot produce a torn read across offerings.
func (p *Provider) Hydrate(types []*karpcp.InstanceType) []*karpcp.InstanceType {
	p.mu.RLock()
	priceSnap := p.prices
	availSnap := p.available
	p.mu.RUnlock()
	out := make([]*karpcp.InstanceType, 0, len(types))
	for _, t := range types {
		offerings := make(karpcp.Offerings, 0, len(t.Offerings))
		for _, o := range t.Offerings {
			zone := zoneFromOffering(o)
			price := o.Price
			if zm, ok := priceSnap[zone]; ok {
				if v, ok := zm[t.Name]; ok {
					price = v
				}
			}
			avail := o.Available
			if zm, ok := availSnap[zone]; ok {
				if v, ok := zm[t.Name]; ok {
					avail = v
				}
			}
			offerings = append(offerings, &karpcp.Offering{
				Requirements: o.Requirements,
				Price:        price,
				Available:    avail,
			})
		}
		// Use DeepCopy to avoid copying the embedded sync.Once in InstanceType.
		copyT := t.DeepCopy()
		copyT.Offerings = offerings
		out = append(out, copyT)
	}
	return out
}

// setPriceForTest is unexported; white-box tests in package pricing use
// it to pre-populate state without going through refreshOnce.
func (p *Provider) setPriceForTest(zone scw.Zone, name string, price float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prices[zone] == nil {
		p.prices[zone] = map[string]float64{}
	}
	p.prices[zone][name] = price
}

// Compile-time checks: the production adapter and the fake satisfy API.
var (
	_ API = (*scalewayAPI)(nil)
	_ API = (*FakeAPI)(nil)
)
