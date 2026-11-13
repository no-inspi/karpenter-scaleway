package pricing

import (
	"context"
	"time"

	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"golang.org/x/sync/errgroup"

	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"
)

// PricingUpdatePeriod is how often the background goroutine polls the
// Scaleway API. Mirrors karpenter-provider-azure's 12h cadence.
const PricingUpdatePeriod = 12 * time.Hour

// metricUpdateInterval is how often the staleness gauge is refreshed.
// Sub-minute so alerts based on lastUpdateAge react promptly when the
// 12h refresh starts to slip.
const metricUpdateInterval = 30 * time.Second

// Run starts the refresh loop and blocks until ctx is cancelled. Should
// be invoked after leader election so a single replica refreshes.
func (p *Provider) Run(ctx context.Context) {
	if err := p.refreshOnce(ctx); err != nil {
		p.log.Error(err, "initial pricing refresh failed; using baseline")
	}
	ticker := time.NewTicker(PricingUpdatePeriod)
	defer ticker.Stop()
	metricTicker := time.NewTicker(metricUpdateInterval)
	defer metricTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-metricTicker.C:
			lastUpdateAge.Set(p.lastUpdateSeconds())
		case <-ticker.C:
			if err := p.refreshOnce(ctx); err != nil {
				p.log.Error(err, "pricing refresh failed; keeping previous prices")
			}
		}
	}
}

// refreshOnce polls every zone in parallel, builds the new price and
// availability maps, and swaps both atomically. On any zone failure we
// conservatively abort the swap and keep previous state.
//
// Concurrency is capped at len(scaleway.AllZones) — one goroutine per zone
// — and ctx is propagated to each Scaleway call via scw.WithContext so a
// SIGTERM mid-refresh aborts in-flight HTTP requests instead of leaking.
//
// Both the catalog prices and GetServerTypesAvailability (stock)
// are issued from the same per-zone goroutine so a transient zone error
// fails the entire refresh — keeping prices and availability consistent
// rather than swapping a half-fresh state where the price says "$0.04"
// but Available is stale from 12h ago.
func (p *Provider) refreshOnce(ctx context.Context) error {
	zones := scaleway.AllZones
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(len(zones))
	priceMaps := make([]map[string]float64, len(zones))
	availMaps := make([]map[string]bool, len(zones))
	for i, zone := range zones {
		g.Go(func() error {
			pm, err := HourlyPrices(gctx, p.api, zone)
			if err != nil {
				return err
			}
			availResp, err := p.api.GetServerTypesAvailability(
				&instance.GetServerTypesAvailabilityRequest{Zone: zone},
				scw.WithContext(gctx),
				scw.WithAllPages(),
			)
			if err != nil {
				return err
			}
			am := make(map[string]bool, len(pm))
			for name := range pm {
				// Default true: a missing entry in the availability response
				// means "no shortage signal" — same conservative read as the
				// generator (see cmd/generate-catalog/main.go isAvailable).
				am[name] = true
			}
			for name, a := range availResp.Servers {
				if a == nil {
					continue
				}
				if a.Availability == instance.ServerTypesAvailabilityShortage {
					am[name] = false
				}
			}
			priceMaps[i] = pm
			availMaps[i] = am
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		refreshTotal.WithLabelValues("fail").Inc()
		return err
	}
	nextPrices := make(map[scw.Zone]map[string]float64, len(zones))
	nextAvail := make(map[scw.Zone]map[string]bool, len(zones))
	missing := 0
	p.mu.RLock()
	for i, zone := range zones {
		// Start from the current prices so types the API did not price
		// keep their last known value, then overlay the live prices.
		merged := make(map[string]float64, len(p.prices[zone])+len(priceMaps[i]))
		for name, v := range p.prices[zone] {
			merged[name] = v
		}
		for name, v := range priceMaps[i] {
			merged[name] = v
		}
		for name := range p.prices[zone] {
			if _, ok := priceMaps[i][name]; !ok {
				missing++
			}
		}
		nextPrices[zone] = merged
		nextAvail[zone] = availMaps[i]
	}
	p.mu.RUnlock()
	if missing > 0 {
		p.log.Info("product catalog returned no hourly price for some known server types; keeping previous prices",
			"server_types", missing)
	}
	p.mu.Lock()
	p.prices = nextPrices
	p.available = nextAvail
	p.lastUpdate = time.Now()
	p.mu.Unlock()
	refreshTotal.WithLabelValues("success").Inc()
	lastUpdateAge.Set(0) // reset on success; Run() advances it between ticks.
	return nil
}
