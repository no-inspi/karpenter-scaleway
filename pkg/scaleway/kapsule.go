// Package scaleway — KapsuleClient wraps the Scaleway K8s API to manage
// Kapsule node pools on behalf of Karpenter.
//
// Strategy: 1 NodeClaim → 1 Kapsule pool of size=1.
// Scaleway handles instance provisioning and kubelet bootstrapping; no
// cloud-init / k8s-agent required.  ProviderID is read directly from the
// Scaleway K8s Node API (scaleway://instances/<zone>/<uuid>) — no IP
// correlation needed.
package scaleway

import (
	"context"
	"errors"
	"fmt"
	"time"

	k8s "github.com/scaleway/scaleway-sdk-go/api/k8s/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// KapsuleClient manages Kapsule node pools for Karpenter.
type KapsuleClient struct {
	api       *k8s.API
	region    scw.Region
	clusterID string
	// nodePollInterval overrides defaultNodePollInterval (tests only).
	nodePollInterval time.Duration
}

// KapsulePool is a thin view of a karpenter-managed Kapsule pool.
type KapsulePool struct {
	ID        string
	NodeClaim string // decoded from "nodeclaim=<name>" tag
	NodeType  string
	Zone      scw.Zone
	// Size is the pool's current node count. Karpenter creates pools of
	// size=1; drift detection treats anything else as ManualResize.
	Size uint32
	// RootVolumeSizeGB is the pool's root volume size in GB (10^9 bytes),
	// 0 when Scaleway did not report one.
	RootVolumeSizeGB int64
}

// KapsuleNode is a thin view of a node inside a KapsulePool.
type KapsuleNode struct {
	ID         string
	PoolID     string
	ProviderID string // e.g. scaleway://instances/fr-par-1/<uuid>
	Name       string // kubernetes node name
	Status     k8s.NodeStatus
}

func NewKapsuleClient(scwClient *scw.Client, region scw.Region, clusterID string) *KapsuleClient {
	return &KapsuleClient{
		api:       k8s.NewAPI(scwClient),
		region:    region,
		clusterID: clusterID,
	}
}

// CreatePoolParams carries the fields a caller needs to provision a Kapsule
// pool of size 1. Required: Zone, InstanceType, Name. ExtraTags are merged
// with the well-known karpenter tags and propagated to the pool.
// RootVolumeSizeGB, when non-nil, sets the node's root volume size; nil keeps
// the Scaleway default for the node type.
type CreatePoolParams struct {
	Zone             scw.Zone
	InstanceType     string
	Name             string
	ExtraTags        []string
	RootVolumeSizeGB *int32
}

// CreatePool creates a Kapsule node pool of size 1 tagged to the given NodeClaim.
// Idempotent: if a pool already exists for this NodeClaim (e.g. controller
// restarted after CreatePool but before the pool-id annotation was persisted),
// the existing pool is returned instead of erroring on the unique-name constraint.
func (c *KapsuleClient) CreatePool(ctx context.Context, params CreatePoolParams) (*KapsulePool, error) {
	if params.Zone == "" {
		return nil, errors.New("kapsule: CreatePoolParams.Zone is required")
	}
	if params.InstanceType == "" || params.Name == "" {
		return nil, fmt.Errorf("kapsule: InstanceType and Name are required")
	}
	tags := append([]string{TagKarpenterManaged, nodeClaimTagPrefix + params.Name}, params.ExtraTags...)
	var rootVolumeSize *scw.Size
	if params.RootVolumeSizeGB != nil {
		size := scw.Size(*params.RootVolumeSizeGB) * scw.GB
		rootVolumeSize = &size
	}
	pool, err := c.api.CreatePool(&k8s.CreatePoolRequest{
		Region:      c.region,
		ClusterID:   c.clusterID,
		Name:        kapsulePoolName(params.Name),
		NodeType:    params.InstanceType,
		Size:        1,
		Autoscaling: false,
		Zone:        params.Zone,
		Tags:        tags,
		// RootVolumeType stays empty: Scaleway picks the node type's
		// default (sbs_5k on PRO2/POP2).
		RootVolumeSize: rootVolumeSize,
		// Karpenter v1 expects nodes to register with
		// karpenter.sh/unregistered:NoExecute and removes it once it has
		// applied the NodePool labels and taints. It has to be a startup
		// taint: Kapsule re-applies reconciled taints (Taints, or pool tags
		// "taint=..."), which would put it back after Karpenter removes it.
		StartupTaints: []*k8s.CoreV1Taint{{
			Key:    karpv1.UnregisteredNoExecuteTaint.Key,
			Effect: k8s.CoreV1TaintEffect(karpv1.UnregisteredNoExecuteTaint.Effect),
		}},
	}, scw.WithContext(ctx))
	if err == nil {
		return toKapsulePool(pool), nil
	}
	// On unique-name conflict, look up the orphaned pool by NodeClaim tag.
	if isInvalidArgumentName(err) {
		if existing, lookupErr := c.findPoolByNodeClaim(ctx, params.Name); lookupErr == nil && existing != nil {
			return existing, nil
		}
	}
	return nil, fmt.Errorf("create pool: %w", err)
}

// GetCluster returns metadata about the cluster this client targets — used
// by the Kapsule backend's Healthcheck.
func (c *KapsuleClient) GetCluster(ctx context.Context) (*Cluster, error) {
	resp, err := c.api.GetCluster(&k8s.GetClusterRequest{
		Region:    c.region,
		ClusterID: c.clusterID,
	}, scw.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("get cluster %s: %w", c.clusterID, err)
	}
	return &Cluster{ID: resp.ID, Type: resp.Type, Name: resp.Name}, nil
}

func (c *KapsuleClient) findPoolByNodeClaim(ctx context.Context, ncName string) (*KapsulePool, error) {
	pools, err := c.ListPools(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range pools {
		if p.NodeClaim == ncName {
			return p, nil
		}
	}
	return nil, nil
}

func isInvalidArgumentName(err error) bool {
	var iaErr *scw.InvalidArgumentsError
	if !errors.As(err, &iaErr) {
		return false
	}
	for _, d := range iaErr.Details {
		if d.ArgumentName == "name" {
			return true
		}
	}
	return false
}

// ErrNodeCreationError is returned (wrapped) by WaitForNodeReady when the
// pool's node reaches the terminal Scaleway status "creation_error". Callers
// match it with errors.Is to fail fast instead of burning the full timeout.
//
// Scaleway does not say why a node failed. We *assume* creation_error means
// a capacity shortage for the requested type in that zone (by far the most
// common cause), but it can also come from the image, IP
// allocation or a project quota. Treating those as capacity errors is still
// safe: the offering is only masked for a few minutes.
var ErrNodeCreationError = errors.New("kapsule node reached status creation_error")

// defaultNodePollInterval is how often WaitForNodeReady polls the pool's
// nodes. Matches the SDK's default WaitFor* retry interval.
const defaultNodePollInterval = 5 * time.Second

// WaitForNodeReady waits until the pool's single node reaches NodeStatusReady.
//
// It polls ListNodes for the pool rather than chaining the SDK's WaitForPool
// and WaitForNode: when Scaleway fails to provision the instance the node
// lands in "creation_error" while the pool can stay "scaling", so
// WaitForPool would block until the timeout. Polling the node directly lets
// us return ErrNodeCreationError as soon as Scaleway gives up.
//
// Readiness is judged on the node only; the pool status is no longer
// checked. That is acceptable because Karpenter pools have exactly one node,
// so a Ready node means the pool has finished scaling for our purposes.
//
// Retryable ListNodes failures (429, 5xx, network — see IsRetryable) are
// logged and polling continues until the timeout; any other error aborts.
func (c *KapsuleClient) WaitForNodeReady(ctx context.Context, poolID string, timeout time.Duration) (*KapsuleNode, error) {
	interval := c.nodePollInterval
	if interval <= 0 {
		interval = defaultNodePollInterval
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastStatus := "no node yet"
	for {
		nodes, err := c.ListNodesForPool(ctx, poolID)
		if err != nil && ctx.Err() == nil {
			if !IsRetryable(err) {
				return nil, err
			}
			log.FromContext(ctx).Info("transient error polling kapsule pool nodes, retrying", "pool", poolID, "err", err)
			lastStatus = "list nodes error: " + err.Error()
		}
		for _, n := range nodes {
			switch n.Status {
			case k8s.NodeStatusCreationError:
				return nil, fmt.Errorf("node %s in pool %s: %w", n.ID, poolID, ErrNodeCreationError)
			case k8s.NodeStatusReady:
				return n, nil
			}
			lastStatus = fmt.Sprintf("node %s status %s", n.ID, n.Status)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for node in pool %s (last: %s): %w", poolID, lastStatus, ctx.Err())
		case <-ticker.C:
		}
	}
}

// DeletePool deletes the Kapsule pool, which terminates the underlying instance.
// Idempotent: returns nil on 404 or if the pool is already in the "deleting"
// transient state (Karpenter retries Delete until it sees NodeClaimNotFound,
// so a second call lands while Scaleway is still tearing the pool down).
func (c *KapsuleClient) DeletePool(ctx context.Context, poolID string) error {
	_, err := c.api.DeletePool(&k8s.DeletePoolRequest{
		Region: c.region,
		PoolID: poolID,
	}, scw.WithContext(ctx))
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return nil
	}
	var transient *scw.TransientStateError
	if errors.As(err, &transient) && transient.CurrentState == "deleting" {
		return nil
	}
	return fmt.Errorf("delete pool %s: %w", poolID, err)
}

// ListPools returns all karpenter-managed Kapsule pools in the cluster.
func (c *KapsuleClient) ListPools(ctx context.Context) ([]*KapsulePool, error) {
	resp, err := c.api.ListPools(&k8s.ListPoolsRequest{
		Region:    c.region,
		ClusterID: c.clusterID,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, fmt.Errorf("list pools: %w", err)
	}
	var out []*KapsulePool
	for _, p := range resp.Pools {
		kp := toKapsulePool(p)
		if kp.NodeClaim != "" {
			out = append(out, kp)
		}
	}
	return out, nil
}

// CountPools returns the cluster-wide pool count and the subset that carries
// the karpenter-managed tag. The total is what consumes the Scaleway 150
// pools/cluster quota (pools created out-of-band via console or Terraform are
// counted in too); the karpenter-managed subset lets metrics show the share
// owned by this controller.
func (c *KapsuleClient) CountPools(ctx context.Context) (int, int, error) {
	resp, err := c.api.ListPools(&k8s.ListPoolsRequest{
		Region:    c.region,
		ClusterID: c.clusterID,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return 0, 0, fmt.Errorf("count pools: %w", err)
	}
	karp := 0
	for _, p := range resp.Pools {
		for _, t := range p.Tags {
			if t == TagKarpenterManaged {
				karp++
				break
			}
		}
	}
	return len(resp.Pools), karp, nil
}

// ListNodesForPool returns all nodes within a given pool.
func (c *KapsuleClient) ListNodesForPool(ctx context.Context, poolID string) ([]*KapsuleNode, error) {
	resp, err := c.api.ListNodes(&k8s.ListNodesRequest{
		Region:    c.region,
		ClusterID: c.clusterID,
		PoolID:    &poolID,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, fmt.Errorf("list nodes for pool %s: %w", poolID, err)
	}
	out := make([]*KapsuleNode, 0, len(resp.Nodes))
	for _, n := range resp.Nodes {
		out = append(out, toKapsuleNode(n))
	}
	return out, nil
}

// kapsulePoolName derives a valid Kapsule pool name from a NodeClaim name.
// Pool names are limited to 50 chars and must match [a-z0-9-].
func kapsulePoolName(ncName string) string {
	name := "karp-" + ncName
	if len(name) > 50 {
		name = name[:50]
	}
	return name
}

func toKapsulePool(p *k8s.Pool) *KapsulePool {
	kp := &KapsulePool{
		ID:       p.ID,
		NodeType: NormalizeNodeType(p.NodeType),
		Zone:     p.Zone,
		Size:     p.Size,
	}
	if p.RootVolumeSize != nil {
		kp.RootVolumeSizeGB = int64(*p.RootVolumeSize / scw.GB)
	}
	for _, t := range p.Tags {
		if len(t) > len(nodeClaimTagPrefix) && t[:len(nodeClaimTagPrefix)] == nodeClaimTagPrefix {
			kp.NodeClaim = t[len(nodeClaimTagPrefix):]
		}
	}
	return kp
}

func toKapsuleNode(n *k8s.Node) *KapsuleNode {
	return &KapsuleNode{
		ID:         n.ID,
		PoolID:     n.PoolID,
		ProviderID: n.ProviderID,
		Name:       n.Name,
		Status:     n.Status,
	}
}
