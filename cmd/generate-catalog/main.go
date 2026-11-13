// Command generate-catalog produces pkg/instancetype/catalog.json from
// the live Scaleway instance API. Run via `make generate-catalog`. Uses
// the standard scw config (~/.config/scw/config.yaml or env vars). Exits
// non-zero on API error or, with --strict, when previously-known types
// disappear.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	pc "github.com/scaleway/scaleway-sdk-go/api/product_catalog/v2alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"

	"github.com/no-inspi/karpenter-scaleway/pkg/instancetype"
	"github.com/no-inspi/karpenter-scaleway/pkg/pricing"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"
)

const (
	outputPath                 = "pkg/instancetype/catalog.json"
	sdkVersion                 = "v1.0.0-beta.36"
	podsPerNodeDefault   int64 = 110
	capacityTypeOnDemand       = "on-demand"
)

func main() {
	strict := flag.Bool("strict", false, "exit 1 if any previously-known type disappeared")
	flag.Parse()

	opts := []scw.ClientOption{scw.WithEnv()}
	if profile, err := loadProfile(); err == nil && profile != nil {
		opts = append(opts, scw.WithProfile(profile))
	}

	client, err := scw.NewClient(opts...)
	if err != nil {
		log.Fatalf("scw client: %v", err)
	}
	api := instance.NewAPI(client)
	catalog := pc.NewPublicCatalogAPI(client)

	types := map[string]*instancetype.TypeEntry{}
	stats := map[string]int{}
	excludedNames := map[string]struct{}{}
	for _, zone := range scaleway.AllZones {
		resp, err := api.ListServersTypes(&instance.ListServersTypesRequest{Zone: zone}, scw.WithAllPages())
		if err != nil {
			log.Fatalf("list types %s: %v", zone, err)
		}
		// Fetch availability for the same zone so each offering carries a
		// fresh stock signal. Treat lookup miss conservatively as available
		// — the stock map can lag behind ListServersTypes by minutes and
		// we'd rather a rare false-positive than a silent zone blackout.
		availResp, err := api.GetServerTypesAvailability(&instance.GetServerTypesAvailabilityRequest{Zone: zone}, scw.WithAllPages())
		if err != nil {
			log.Fatalf("availability %s: %v", zone, err)
		}
		// Prices come from the public Product Catalog API: the Instance API
		// returns hourly_price=0 for every type since 2026-11.
		prices, err := pricing.HourlyPrices(context.Background(), catalog, zone)
		if err != nil {
			log.Fatalf("product catalog %s: %v", zone, err)
		}
		for name, st := range resp.Servers {
			r := accept(name, st)
			if !r.keep {
				if _, seen := excludedNames[name]; !seen {
					stats["excl_"+r.reason]++
					excludedNames[name] = struct{}{}
				}
				continue
			}
			price, ok := prices[name]
			if !ok {
				stats["excl_no_catalog_price"]++
				continue
			}
			e := types[name]
			if e == nil {
				e = newEntry(name, st)
				types[name] = e
				stats["kept"]++
			}
			e.Offerings = append(e.Offerings, instancetype.Offering{
				Zone:         string(zone),
				CapacityType: capacityTypeOnDemand,
				PriceEURhr:   price,
				Available:    isAvailable(availResp.Servers, name),
			})
		}
	}

	// Defensive: a zero price would make the offering free in the embedded
	// baseline (the runtime fallback when live prices are missing).
	zeroPriced := 0
	for _, e := range types {
		for _, o := range e.Offerings {
			if o.PriceEURhr <= 0 {
				zeroPriced++
			}
		}
	}
	if zeroPriced > 0 {
		log.Fatalf("%d offerings have a zero price; refusing to write a catalog with zero prices (keep the current %s)", zeroPriced, outputPath)
	}

	cat := &instancetype.Catalog{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		SDKVersion:  sdkVersion,
		Denylist: instancetype.Denylist{
			EndOfService: true, GPU: true, NamePrefixes: denylistPrefixes,
		},
		Types: make([]instancetype.TypeEntry, 0, len(types)),
	}
	for _, e := range types {
		cat.Types = append(cat.Types, *e)
	}
	sortDeterministic(cat)

	if old, err := readExisting(outputPath); err == nil {
		d := compareCatalogs(old, cat)
		fmt.Fprintf(os.Stderr, "diff vs existing: +%d -%d\n", len(d.added), len(d.removed))
		if len(d.removed) > 0 {
			fmt.Fprintf(os.Stderr, "WARN: removed types: %v\n", d.removed)
			if *strict {
				log.Fatalf("strict mode: aborting")
			}
		}
		if len(d.added) > 0 {
			fmt.Fprintf(os.Stderr, "INFO: added types: %v\n", d.added)
		}
	}

	if err := writeJSON(outputPath, cat); err != nil {
		log.Fatalf("write %s: %v", outputPath, err)
	}
	fmt.Printf("OK: %d types kept; excluded by reason: %v\n", stats["kept"], excludedSummary(stats))
}

// isAvailable reads the per-zone stock map from
// instance.GetServerTypesAvailability. Only an explicit "shortage" entry
// flips the offering to false; anything else (including a missing entry)
// keeps it available, mirroring the conservative read in pkg/pricing.
func isAvailable(m map[string]*instance.GetServerTypesAvailabilityResponseAvailability, name string) bool {
	a, ok := m[name]
	if !ok || a == nil {
		return true
	}
	return a.Availability != instance.ServerTypesAvailabilityShortage
}

func newEntry(name string, st *instance.ServerType) *instancetype.TypeEntry {
	return &instancetype.TypeEntry{
		Name:          name,
		Arch:          mapArch(st.Arch),
		CPUMillicores: int64(st.Ncpus) * 1000,
		MemoryBytes:   st.RAM,
		PodsPerNode:   podsPerNodeDefault,
	}
}

func readExisting(path string) (*instancetype.Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c instancetype.Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func writeJSON(path string, c *instancetype.Catalog) error {
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0o644)
}

func excludedSummary(s map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range s {
		if k != "kept" {
			out[k] = v
		}
	}
	return out
}

// loadProfile loads the active profile from the scw config file. Resolution
// honours $SCW_PROFILE, the active_profile yaml field, then the root profile
// (delegated to cfg.GetActiveProfile). Returns the underlying load error
// when no config file exists — NewClient still falls back to env vars.
func loadProfile() (*scw.Profile, error) {
	cfg, err := scw.LoadConfig()
	if err != nil {
		return nil, err
	}
	return cfg.GetActiveProfile()
}
