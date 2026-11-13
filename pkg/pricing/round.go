package pricing

import "math"

// RoundEURHourly rounds an EUR/h price to 6 decimal places. The Scaleway SDK
// returns HourlyPrice as float32; widening it to float64 produces artefacts
// like 0.09099999815225601. Rounding to 6 decimals (sub-microcent precision)
// drops the noise while keeping more precision than any real price needs.
//
// Used both by the runtime refresh loop and by cmd/generate-catalog so the
// embedded baseline and the live prices share identical representation —
// avoids spurious price-change diffs when the generator is re-run.
func RoundEURHourly(p float32) float64 {
	return math.Round(float64(p)*1e6) / 1e6
}
