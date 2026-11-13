package backend

import (
	"fmt"

	"github.com/no-inspi/karpenter-scaleway/pkg/backend/kapsule"
	"github.com/no-inspi/karpenter-scaleway/pkg/backend/kosmos"
	"github.com/no-inspi/karpenter-scaleway/pkg/pricing"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SharedOptions bundles the dependencies the backend factory needs to
// construct any concrete implementation. The factory only consumes the
// fields relevant to the resolved Kind — providing extras (e.g. an
// InstanceAPI when picking Kapsule) is harmless.
type SharedOptions struct {
	// Instance is the Scaleway Instance API client. Required for Kosmos.
	Instance scaleway.InstanceAPI
	// Kapsule is the Scaleway K8s pool API client. Required for Kapsule.
	Kapsule scaleway.KapsuleAPI
	// K8s is the generic K8s API client. Required for Kosmos healthchecks
	// (Kapsule uses its own KapsuleAPI.GetCluster).
	K8s scaleway.K8sAPI
	// KubeClient is the kube-apiserver client. Required for both backends.
	KubeClient client.Client
	// ClusterID is the Scaleway-managed cluster UUID.
	ClusterID string
	// Pricing is the live per-zone EUR/h price provider. Required for both
	// backends — consumed by GetInstanceTypes to hydrate offering prices.
	Pricing *pricing.Provider
	// Log is the structured logger.
	Log logr.Logger
}

// New dispatches to the registered backend implementation matching kind.
// Returns an error if kind is unknown or the per-impl construction fails.
func New(kind Kind, opts SharedOptions) (Backend, error) {
	switch kind {
	case KindKapsule:
		return kapsule.New(kapsule.Options{
			Kapsule:    opts.Kapsule,
			KubeClient: opts.KubeClient,
			Pricing:    opts.Pricing,
			Quota:      kapsule.LoadQuotaConfig(opts.Log),
			Log:        opts.Log,
		})
	case KindKosmos:
		return kosmos.New(kosmos.Options{
			Instance:   opts.Instance,
			K8s:        opts.K8s,
			KubeClient: opts.KubeClient,
			ClusterID:  opts.ClusterID,
			Pricing:    opts.Pricing,
			Log:        opts.Log,
		})
	default:
		return nil, fmt.Errorf("unknown backend kind %q", kind)
	}
}

// MustNew is the panic-on-error variant of New, useful at boot when an
// unconstructible backend is a programmer bug rather than a runtime one.
func MustNew(kind Kind, opts SharedOptions) Backend {
	bk, err := New(kind, opts)
	if err != nil {
		panic(err)
	}
	return bk
}

// Compile-time interface satisfaction. If either backend drifts away from
// the contract, this file fails to compile.
var (
	_ Backend = (*kapsule.Kapsule)(nil)
	_ Backend = (*kosmos.Kosmos)(nil)
)
