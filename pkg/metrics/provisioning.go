package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

var provisioningDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "karpenter_scaleway_nodeclaim_provisioning_duration_seconds",
	Help:    "Time from Backend.Create entry to hydrated NodeClaim returned, by backend, zone, outcome (success|fail|ice|rejected).",
	Buckets: []float64{10, 30, 60, 90, 120, 180, 300, 600},
}, []string{"backend", "zone", "outcome"})

func init() {
	metrics.Registry.MustRegister(provisioningDuration)
}

// MeasureCreate runs fn (the Scaleway-side of Backend.Create — pool/instance
// creation, wait-for-Ready) and records its duration with
// outcome=success|fail|ice. outcome=ice isolates Scaleway capacity failures
// (fn returned a cloudprovider.InsufficientCapacityError) from genuine
// failures so dashboards can tell "no stock" from "broken". Pre-flight rejections (NodeClass not found, zone
// outside whitelist, …) MUST go through MarkRejected instead so they don't
// pollute the success/fail distribution used to track NFR latency. The fn
// error is returned unchanged so callers can layer further wrapping
// without losing the original cause.
func MeasureCreate(backend, zone string, fn func() error) error {
	start := time.Now()
	err := fn()
	outcome := "success"
	switch {
	case karpcp.IsInsufficientCapacityError(err):
		outcome = "ice"
	case err != nil:
		outcome = "fail"
	}
	provisioningDuration.WithLabelValues(backend, zone, outcome).Observe(time.Since(start).Seconds())
	return err
}

// MarkRejected records a pre-flight rejection — i.e. a Backend.Create that
// failed before any Scaleway API call (invalid NodeClaim, NodeClass not
// found, zone not whitelisted, …). The histogram is shared with
// MeasureCreate but the outcome=rejected label lets dashboards filter
// these sub-second pre-flight events out of NFR queries.
func MarkRejected(backend, zone string, duration time.Duration) {
	provisioningDuration.WithLabelValues(backend, zone, "rejected").Observe(duration.Seconds())
}
