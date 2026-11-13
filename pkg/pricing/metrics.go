package pricing

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	lastUpdateAge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "karpenter_scaleway_pricing_last_update_seconds_ago",
		Help: "Seconds since the last successful pricing refresh.",
	})
	refreshTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "karpenter_scaleway_pricing_refresh_total",
		Help: "Pricing refresh attempts by outcome.",
	}, []string{"outcome"})
)

func init() {
	metrics.Registry.MustRegister(lastUpdateAge, refreshTotal)
}
