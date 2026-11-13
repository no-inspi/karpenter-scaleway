// Package scaleway — interfaces.
//
// Backends and the NodeClass reconciler depend on these interfaces, never on
// the concrete *Client / *KapsuleClient / *MarketplaceClient / *K8sClient
// types, so unit tests can supply hand-written fakes without standing up a
// real Scaleway HTTP client.
package scaleway

import (
	"context"
	"time"

	"github.com/scaleway/scaleway-sdk-go/scw"
)

// InstanceAPI is the surface the Kosmos backend exercises against the
// Scaleway Instance API. Per-instance methods take an explicit zone since the
// client is no longer pinned to one.
type InstanceAPI interface {
	Create(ctx context.Context, params CreateInstanceParams) (*ScalewayInstance, error)
	Delete(ctx context.Context, zone scw.Zone, instanceID string) error
	Get(ctx context.Context, zone scw.Zone, instanceID string) (*ScalewayInstance, error)
	List(ctx context.Context) ([]*ScalewayInstance, error)
}

// KapsuleAPI is the surface the Kapsule backend exercises against the
// Scaleway K8s API for managing pools.
type KapsuleAPI interface {
	CreatePool(ctx context.Context, params CreatePoolParams) (*KapsulePool, error)
	DeletePool(ctx context.Context, poolID string) error
	WaitForNodeReady(ctx context.Context, poolID string, timeout time.Duration) (*KapsuleNode, error)
	ListPools(ctx context.Context) ([]*KapsulePool, error)
	ListNodesForPool(ctx context.Context, poolID string) ([]*KapsuleNode, error)
	CountPools(ctx context.Context) (total, karpenterManaged int, err error)
	GetCluster(ctx context.Context) (*Cluster, error)
}

// K8sAPI is the surface boot-time auto-detection consumes — currently a
// single GetCluster call.
type K8sAPI interface {
	GetCluster(ctx context.Context, clusterID string) (*Cluster, error)
}

// MarketplaceAPI is the surface the NodeClass reconciler consumes to
// validate imageLabel values against the Scaleway Marketplace.
type MarketplaceAPI interface {
	LookupImage(ctx context.Context, zone scw.Zone, label string) (string, error)
}

// Compile-time interface satisfaction. Karpenter's CloudProvider contract is
// stable; the package will not compile if a Scaleway client drifts away
// from one of the interfaces it must satisfy.
var (
	_ InstanceAPI    = (*Client)(nil)
	_ KapsuleAPI     = (*KapsuleClient)(nil)
	_ K8sAPI         = (*K8sClient)(nil)
	_ MarketplaceAPI = (*MarketplaceClient)(nil)
)
