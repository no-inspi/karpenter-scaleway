package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"
)

// Source labels the origin of the resolved backend Kind. Surfaced as a
// metric label so operators can tell at a glance whether the binary went
// through auto-detection or the kill-switch flag.
type Source string

const (
	SourceAuto   Source = "auto"
	SourceForced Source = "forced"
)

// detectRetryAttempts is the number of GetCluster attempts before failing.
// Boot-time-only retry; once the binary is up the auto-detection result is
// frozen for the lifetime of the process (per design §3.1).
const detectRetryAttempts = 3

// detectRetryBaseDelay is the gap between successive GetCluster attempts.
const detectRetryBaseDelay = 5 * time.Second

// ResolveAuto inspects the Scaleway API at boot to map cluster.Type to a
// backend.Kind. Transient 5xx errors are retried up to detectRetryAttempts
// times; 404 (cluster not found) and unknown cluster types fail fast.
func ResolveAuto(ctx context.Context, k8s scaleway.K8sAPI, clusterID string) (Kind, error) {
	return resolveAutoWithDelay(ctx, k8s, clusterID, detectRetryBaseDelay)
}

// resolveAutoWithDelay is the implementation of ResolveAuto with an
// injectable delay so unit tests can run without sleeping.
func resolveAutoWithDelay(ctx context.Context, k8s scaleway.K8sAPI, clusterID string, delay time.Duration) (Kind, error) {
	if clusterID == "" {
		return "", errors.New("backend.ResolveAuto: clusterID is required")
	}
	var lastErr error
	for attempt := 0; attempt < detectRetryAttempts; attempt++ {
		cluster, err := k8s.GetCluster(ctx, clusterID)
		if err == nil {
			kind, mapErr := mapClusterType(cluster.Type)
			if mapErr != nil {
				return "", fmt.Errorf("auto-detect: %w (consider --force-backend)", mapErr)
			}
			return kind, nil
		}
		lastErr = err
		if attempt < detectRetryAttempts-1 && delay > 0 {
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("auto-detect cancelled: %w", ctx.Err())
			case <-time.After(delay):
			}
		}
	}
	return "", fmt.Errorf("auto-detect: GetCluster(%s) failed after %d attempts: %w",
		clusterID, detectRetryAttempts, lastErr)
}

// ResolveForced returns the Kind matching the --force-backend flag value.
// Empty string is rejected; callers should branch on flag != "" before
// invoking this helper.
func ResolveForced(s string) (Kind, error) {
	if s == "" {
		return "", errors.New("backend.ResolveForced: empty backend value")
	}
	return ParseKind(s)
}

// mapClusterType maps a Scaleway cluster.Type string to a backend.Kind.
// See Scaleway K8s API docs / pkg/scaleway/k8s.go for the canonical values.
func mapClusterType(t string) (Kind, error) {
	switch {
	case t == "kapsule" || strings.HasPrefix(t, "kapsule-dedicated-"):
		return KindKapsule, nil
	case t == "multicloud" || strings.HasPrefix(t, "multicloud-dedicated-"):
		return KindKosmos, nil
	default:
		return "", fmt.Errorf("unsupported cluster.Type %q (expected kapsule* or multicloud*)", t)
	}
}
