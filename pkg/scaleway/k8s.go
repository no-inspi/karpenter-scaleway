package scaleway

import (
	"context"
	"fmt"

	k8s "github.com/scaleway/scaleway-sdk-go/api/k8s/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// K8sClient wraps the Scaleway K8s API for operations that don't require a
// pinned cluster (auto-detection at boot, generic GetCluster lookups). It
// is intentionally separate from KapsuleClient, which is itself bound to a
// specific cluster ID + region + zone.
type K8sClient struct {
	api    *k8s.API
	region scw.Region
}

// Cluster is the trimmed view of a Scaleway-managed Kubernetes cluster
// the rest of the codebase needs. We avoid exposing the upstream SDK type
// so the surface stays mockable.
type Cluster struct {
	ID   string
	Type string
	Name string
}

func NewK8sClient(scwClient *scw.Client, region scw.Region) *K8sClient {
	return &K8sClient{api: k8s.NewAPI(scwClient), region: region}
}

// GetCluster returns metadata about a Scaleway-managed cluster.
// 404 surfaces as a typed error so callers can distinguish from transient
// 5xx responses.
func (c *K8sClient) GetCluster(ctx context.Context, clusterID string) (*Cluster, error) {
	resp, err := c.api.GetCluster(&k8s.GetClusterRequest{
		Region:    c.region,
		ClusterID: clusterID,
	}, scw.WithContext(ctx))
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("cluster %s not found in region %s: %w", clusterID, c.region, err)
		}
		return nil, fmt.Errorf("get cluster %s: %w", clusterID, err)
	}
	return &Cluster{ID: resp.ID, Type: resp.Type, Name: resp.Name}, nil
}
