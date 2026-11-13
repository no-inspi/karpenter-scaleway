// Package instancetype owns the Scaleway instance type catalog consumed by
// Karpenter scheduling. The catalog is generated at build time from the
// Scaleway API by cmd/generate-catalog and embedded into the binary as
// catalog.json. Runtime is read-only.
package instancetype

// Catalog is the top-level structure embedded as catalog.json.
type Catalog struct {
	GeneratedAt string      `json:"generatedAt"`
	SDKVersion  string      `json:"sdkVersion"`
	Denylist    Denylist    `json:"denylist"`
	Types       []TypeEntry `json:"types"`
}

// Denylist records the filters applied at generation time. Stored in the
// JSON for traceability — runtime never reads it.
type Denylist struct {
	EndOfService bool     `json:"endOfService"`
	GPU          bool     `json:"gpu"`
	NamePrefixes []string `json:"namePrefixes"`
}

// TypeEntry is one Scaleway instance type with its per-zone offerings.
type TypeEntry struct {
	Name          string     `json:"name"`
	Arch          string     `json:"arch"`          // "amd64" | "arm64"
	CPUMillicores int64      `json:"cpuMillicores"` // Ncpus * 1000
	MemoryBytes   uint64     `json:"memoryBytes"`
	PodsPerNode   int64      `json:"podsPerNode"` // 110 by default
	Offerings     []Offering `json:"offerings"`
}

// Offering is the availability and price of a TypeEntry in a given (zone, capacity).
//
// Available reflects the per-zone stock signal from
// instance.GetServerTypesAvailability at generation time:
//   - "available" / "scarce" → true (creation expected to succeed)
//   - "shortage"             → false (creation will be rejected by the API)
//
// The runtime pricing refresh re-evaluates this every PricingUpdatePeriod
// so the catalog baseline is just the boot-time fallback. Karpenter's
// scheduler filters on this field via OrderByPrice / Compatible — without
// it, a depleted zone is still selectable and CreateServer rejects
// trigger a partial-create cleanup loop.
type Offering struct {
	Zone         string  `json:"zone"`
	CapacityType string  `json:"capacityType"` // "on-demand" only
	PriceEURhr   float64 `json:"priceEURhr"`
	Available    bool    `json:"available"`
}
