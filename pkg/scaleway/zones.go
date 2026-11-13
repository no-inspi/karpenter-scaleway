package scaleway

import "github.com/scaleway/scaleway-sdk-go/scw"

// AllZones is the canonical list of Scaleway zones the provider targets.
// Used by every package that needs to enumerate zones — `pkg/pricing`
// refresher, `pkg/scaleway/Client.List`, `cmd/generate-catalog`, and the
// NodeClass reconciler default. Ordered so the first element is the
// historical "primary" (`fr-par-1`).
//
// Keep in sync with the kubebuilder Enum on
// `api/v1alpha1/scalewaynodeclass_types.go:Zones` — the CRD enum needs
// literal values and can't import this slice.
var AllZones = []scw.Zone{
	scw.ZoneFrPar1, scw.ZoneFrPar2, scw.ZoneFrPar3,
	scw.ZoneNlAms1, scw.ZoneNlAms2, scw.ZoneNlAms3,
	scw.ZonePlWaw1, scw.ZonePlWaw2, scw.ZonePlWaw3,
}

// AllZoneStrings returns AllZones as a fresh []string. Convenient for
// callers that work in plain strings (e.g. NodeClass.Spec.Zones default,
// catalog filters keyed by zone label values).
func AllZoneStrings() []string {
	out := make([]string, len(AllZones))
	for i, z := range AllZones {
		out[i] = string(z)
	}
	return out
}
