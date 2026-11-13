package backend

import (
	"context"
	"fmt"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// Kind enumerates the backend implementations registered in this package.
// Stable string — also surfaced as a metric label.
type Kind string

const (
	KindKapsule Kind = "kapsule"
	KindKosmos  Kind = "kosmos"
)

// String implements fmt.Stringer.
func (k Kind) String() string { return string(k) }

// ParseKind returns the Kind matching s. Anything outside the registered
// values is rejected with an error rather than silently coerced.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case KindKapsule, KindKosmos:
		return Kind(s), nil
	default:
		return "", fmt.Errorf("unknown backend %q (want %q or %q)", s, KindKapsule, KindKosmos)
	}
}

// Backend abstracts a Scaleway-managed cluster type behind a single contract.
// The signatures mirror karpcp.CloudProvider 1:1, plus two ops useful for the
// binary lifecycle (Healthcheck) and observability (Name).
//
// Implementations must be safe for concurrent use.
type Backend interface {
	// Create provisions a new instance for the NodeClaim and returns it
	// hydrated with ProviderID, NodeName and the required scheduling labels.
	// On error the implementation must roll back any partial state.
	Create(ctx context.Context, nc *karpv1.NodeClaim) (*karpv1.NodeClaim, error)

	// Delete terminates the instance backing the NodeClaim. Per the
	// Karpenter contract it must return karpcp.NewNodeClaimNotFoundError
	// once the instance is confirmed gone (sync deletion model).
	Delete(ctx context.Context, nc *karpv1.NodeClaim) error

	// Get retrieves a NodeClaim by providerID. Returns
	// karpcp.NewNodeClaimNotFoundError if the underlying instance is gone.
	Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error)

	// List returns one NodeClaim per backend-managed instance (or pool).
	List(ctx context.Context) ([]*karpv1.NodeClaim, error)

	// GetInstanceTypes returns the catalog filtered by NodePool requirements.
	GetInstanceTypes(ctx context.Context, np *karpv1.NodePool) ([]*karpcp.InstanceType, error)

	// IsDrifted reports why the NodeClaim drifted from its NodeClass, or ""
	// when it has not.
	IsDrifted(ctx context.Context, nc *karpv1.NodeClaim) (karpcp.DriftReason, error)

	// Healthcheck returns nil if the backend can reach the Scaleway API.
	// Surfaced via the readiness probe.
	Healthcheck(ctx context.Context) error

	// Name returns "kapsule" or "kosmos". Stable identifier for logs and
	// metrics labels.
	Name() string
}
