package harness

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// discoveryProbe is the subset of `client-go/discovery.DiscoveryInterface`
// used by PollDiscovery — extracted so tests can supply a stub instead of
// a real apiserver.
type discoveryProbe interface {
	ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error)
}

// PollDiscovery blocks until ServerGroupsAndResources returns nil error, or
// the context fires. This replicates the call helm performs at the start of
// `upgrade --install`; helm fails when even one aggregated APIService is
// unhealthy (Kapsule's metrics-server addon registers
// `metrics.k8s.io/v1beta1` before its pod is Ready, surfacing as "client
// rate limiter Wait returned an error: context deadline exceeded"). Once
// this poll succeeds, helm install will succeed too.
//
// every is the inter-attempt delay; production callers should pass 5s. The
// caller is expected to provide a context with a timeout (~5 min for fresh
// Kapsule clusters; metrics-server typically becomes Ready within 60-90 s).
func PollDiscovery(ctx context.Context, d discoveryProbe, every time.Duration) error {
	if d == nil {
		return errors.New("discovery client required")
	}
	if every <= 0 {
		every = 5 * time.Second
	}
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("discovery never settled: %w (last: %v)", ctx.Err(), lastErr)
			}
			return ctx.Err()
		default:
		}
		_, _, err := d.ServerGroupsAndResources()
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("discovery never settled: %w (last: %v)", ctx.Err(), lastErr)
		case <-time.After(every):
		}
	}
}

// WaitForAPIDiscovery is the production entry point used by SetupCluster.
// It builds a discovery client from kubeconfigPath and calls PollDiscovery
// with a 5 s inter-attempt delay. Caller controls the overall deadline via
// ctx.
func WaitForAPIDiscovery(ctx context.Context, kubeconfigPath string) error {
	if kubeconfigPath == "" {
		return errors.New("kubeconfig path required")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("build config from %s: %w", kubeconfigPath, err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kube clientset: %w", err)
	}
	return PollDiscovery(ctx, cs.Discovery(), 5*time.Second)
}
