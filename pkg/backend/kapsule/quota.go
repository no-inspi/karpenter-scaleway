package kapsule

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Scaleway hard limit on Kapsule pools per cluster, documented in their
// product limits page. Treated as a default that operators can override when
// Scaleway grants a higher quota for a specific cluster.
const (
	defaultPoolQuota          = 150
	defaultPoolQuotaThreshold = 140

	envPoolQuota          = "KAPSULE_POOL_QUOTA"
	envPoolQuotaThreshold = "KAPSULE_POOL_QUOTA_THRESHOLD"
)

// ErrQuotaExceeded surfaces from preflight when the cluster pool count meets
// or exceeds the configured backpressure threshold. It's returned via the
// rejected outcome path (MarkRejected) so it never pollutes the success/fail
// latency distribution. Karpenter retries Create on a debounced cadence, so
// the NodeClaim eventually provisions once a pool slot frees up.
type ErrQuotaExceeded struct {
	Total     int
	Threshold int
	Limit     int
}

func (e *ErrQuotaExceeded) Error() string {
	return fmt.Sprintf("kapsule pool quota threshold reached: total=%d threshold=%d limit=%d", e.Total, e.Threshold, e.Limit)
}

// QuotaConfig carries the cluster-level Kapsule pool budget. Limit is the
// Scaleway hard cap (default 150); Threshold is the soft cap above which
// Create is rejected to leave room for in-flight provisioning races and
// out-of-band ops pools.
type QuotaConfig struct {
	Limit     int
	Threshold int
}

// LoadQuotaConfig reads KAPSULE_POOL_QUOTA / KAPSULE_POOL_QUOTA_THRESHOLD,
// falls back to documented defaults, and clamps Threshold to Limit-1 if the
// operator misconfigures it. The clamp is logged as a warning rather than
// rejected so a typo never wedges the controller at boot.
func LoadQuotaConfig(log logr.Logger) QuotaConfig {
	cfg := QuotaConfig{Limit: defaultPoolQuota, Threshold: defaultPoolQuotaThreshold}
	if v, ok := readPositiveInt(envPoolQuota); ok {
		cfg.Limit = v
	}
	if v, ok := readPositiveInt(envPoolQuotaThreshold); ok {
		cfg.Threshold = v
	}
	if cfg.Threshold >= cfg.Limit {
		clamped := cfg.Limit - 1
		if clamped < 1 {
			clamped = 1
		}
		log.Info("KAPSULE_POOL_QUOTA_THRESHOLD >= KAPSULE_POOL_QUOTA, clamping",
			"requested", cfg.Threshold, "limit", cfg.Limit, "clamped", clamped)
		cfg.Threshold = clamped
	}
	return cfg
}

func readPositiveInt(env string) (int, bool) {
	raw := os.Getenv(env)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// check returns ErrQuotaExceeded when total >= Threshold. Equality is
// intentional: at threshold we already want to push back so the in-flight
// CreatePool races don't carry us above Limit.
func (q QuotaConfig) check(total int) error {
	if total >= q.Threshold {
		return &ErrQuotaExceeded{Total: total, Threshold: q.Threshold, Limit: q.Limit}
	}
	return nil
}

// IsQuotaExceeded is the sentinel-style helper preflight callers use to
// classify the rejection reason without poking at the typed error directly.
func IsQuotaExceeded(err error) bool {
	var qe *ErrQuotaExceeded
	return errors.As(err, &qe)
}

// --- metrics ---

var (
	poolsTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "karpenter_scaleway_kapsule_pools_total",
		Help: "Number of Kapsule pools observed in the cluster, by origin (karpenter|other). Sum across labels equals the cluster-wide count consumed against the Scaleway 150 pools/cluster quota.",
	}, []string{"origin"})

	poolQuotaRemaining = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "karpenter_scaleway_kapsule_pool_quota_remaining",
		Help: "Kapsule pool quota headroom (limit - observed total). Negative values signal that the cluster already exceeds the configured limit.",
	})

	poolQuotaRejections = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "karpenter_scaleway_kapsule_pool_quota_rejections_total",
		Help: "NodeClaim Create attempts rejected by the Kapsule pool quota threshold.",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(poolsTotal, poolQuotaRemaining, poolQuotaRejections)
}

// observeQuota updates the cluster-wide pool gauges. Called whenever the
// backend has a fresh CountPools result, regardless of the Create outcome.
func observeQuota(total, karpenterManaged, limit int) {
	other := total - karpenterManaged
	if other < 0 {
		other = 0
	}
	poolsTotal.WithLabelValues("karpenter").Set(float64(karpenterManaged))
	poolsTotal.WithLabelValues("other").Set(float64(other))
	poolQuotaRemaining.Set(float64(limit - total))
}
