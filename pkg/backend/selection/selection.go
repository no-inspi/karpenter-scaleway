// Package selection picks the concrete (instance type, zone) a backend
// launches for a NodeClaim.
//
// Karpenter hands the cloud provider a NodeClaim whose instance-type and
// zone requirements list every viable candidate. Those values are a set,
// serialized in alphabetical order — not by price — so the provider must
// make the price-aware pick itself (as the AWS provider does via
// CreateFleet lowest-price).
package selection

// PriceFunc returns the EUR/h price of instanceType in zone, and false when
// the pair is not offered or currently unavailable.
type PriceFunc func(zone, instanceType string) (float64, bool)

// Cheapest returns the cheapest priced (instanceType, zone) pair among the
// candidates. Ties keep candidate order (types first, then zones), so the
// result is deterministic. When no pair has a price it falls back to the
// first type and first zone, which is the pre-0.4.4 behaviour. Empty
// candidates yield empty strings.
func Cheapest(types, zones []string, price PriceFunc) (string, string) {
	if len(types) == 0 || len(zones) == 0 {
		return "", ""
	}
	bestType, bestZone := "", ""
	var best float64
	found := false
	for _, t := range types {
		for _, z := range zones {
			p, ok := price(z, t)
			if !ok {
				continue
			}
			if !found || p < best {
				best, bestType, bestZone, found = p, t, z, true
			}
		}
	}
	if !found {
		return types[0], zones[0]
	}
	return bestType, bestZone
}

// AllowedZones keeps the candidate zones that are in the NodeClass
// spec.zones whitelist. An empty whitelist allows every zone. When no
// candidate is allowed the candidates are returned unchanged, so the
// caller's whitelist guard rejects the launch with an explicit error
// instead of launching nowhere.
func AllowedZones(zones, whitelist []string) []string {
	if len(whitelist) == 0 {
		return zones
	}
	allowed := make(map[string]bool, len(whitelist))
	for _, w := range whitelist {
		allowed[w] = true
	}
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		if allowed[z] {
			out = append(out, z)
		}
	}
	if len(out) == 0 {
		return zones
	}
	return out
}
