package kapsule

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/pkg/backend/drift"
	"github.com/no-inspi/karpenter-scaleway/pkg/backend/selection"
	"github.com/no-inspi/karpenter-scaleway/pkg/instancetype"
	"github.com/no-inspi/karpenter-scaleway/pkg/metrics"
	"github.com/no-inspi/karpenter-scaleway/pkg/pricing"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	"github.com/go-logr/logr"
	"github.com/scaleway/scaleway-sdk-go/scw"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

const (
	// PoolIDAnnotationKey records the Scaleway pool UUID on the NodeClaim
	// so Delete never has to scan all pools.
	PoolIDAnnotationKey = "karpenter.scaleway.com/pool-id"

	backendName = "kapsule"

	createTimeout = 4 * time.Minute

	// failedPoolDeleteTimeout bounds how long Create keeps retrying the
	// deletion of a pool whose node never became Ready. Scaleway rejects
	// DeletePool with a transient_state error while the pool is still
	// "scaling" — exactly the state a creation_error pool is in.
	failedPoolDeleteTimeout       = 2 * time.Minute
	failedPoolDeleteRetryInterval = 10 * time.Second
)

// Options bundles dependencies for New.
type Options struct {
	Kapsule    scaleway.KapsuleAPI
	KubeClient client.Client
	Pricing    *pricing.Provider
	Quota      QuotaConfig
	Log        logr.Logger
}

// Kapsule is the backend.Backend implementation for Scaleway Kapsule
// managed-Kubernetes node pools.
type Kapsule struct {
	kapsule    scaleway.KapsuleAPI
	kubeClient client.Client
	pricing    *pricing.Provider
	quota      QuotaConfig
	log        logr.Logger

	unavailable *unavailableOfferings

	// failedPoolDeleteTimeout / failedPoolDeleteRetryInterval are fields so
	// tests can shrink them.
	failedPoolDeleteTimeout       time.Duration
	failedPoolDeleteRetryInterval time.Duration
}

// New validates Options and returns a ready-to-use Kapsule backend. A zero
// QuotaConfig is replaced by the documented defaults (Limit=150,
// Threshold=140) so callers that don't care about quota wiring can leave it
// unset.
func New(opts Options) (*Kapsule, error) {
	switch {
	case opts.Kapsule == nil:
		return nil, fmt.Errorf("kapsule: KapsuleAPI is required")
	case opts.KubeClient == nil:
		return nil, fmt.Errorf("kapsule: KubeClient is required")
	case opts.Pricing == nil:
		return nil, fmt.Errorf("kapsule: Pricing is required")
	}
	q := opts.Quota
	if q.Limit == 0 {
		q.Limit = defaultPoolQuota
	}
	if q.Threshold == 0 {
		q.Threshold = defaultPoolQuotaThreshold
	}
	if q.Threshold >= q.Limit {
		q.Threshold = q.Limit - 1
	}
	return &Kapsule{
		kapsule:    opts.Kapsule,
		kubeClient: opts.KubeClient,
		pricing:    opts.Pricing,
		quota:      q,
		log:        opts.Log,

		unavailable: newUnavailableOfferings(),

		failedPoolDeleteTimeout:       failedPoolDeleteTimeout,
		failedPoolDeleteRetryInterval: failedPoolDeleteRetryInterval,
	}, nil
}

// Name returns the backend identifier.
func (k *Kapsule) Name() string { return backendName }

// IsDrifted compares the live Kapsule pool against the state captured on
// the NodeClaim at Create time and returns a structured DriftReason when
// they diverge. Karpenter calls this once per minute per NodeClaim, so the
// implementation makes a single ListPools round-trip and avoids any cache
// (Karpenter v1.10 already QPS-limits the call).
//
// The escape hatch is the SkipDriftAnnotationKey annotation on the
// NodeClaim — operators set it during one-off interventions to keep
// Karpenter from recycling the NodeClaim.
//
// Drift sources covered:
//   - PoolResize: pool size != 1 (manual scale outside Karpenter).
//   - InstanceType: pool.NodeType != NodeClaim instance-type label.
//   - Zone: pool.Zone != NodeClaim topology label.
//
// A pool that has disappeared is not drift — Karpenter's GC controller
// observes the missing instance via List/Get and recycles the NodeClaim
// through the NodeClaimNotFound path.
func (k *Kapsule) IsDrifted(ctx context.Context, nc *karpv1.NodeClaim) (karpcp.DriftReason, error) {
	if nc.Annotations[drift.SkipAnnotationKey] == "true" {
		return "", nil
	}
	poolID := nc.Annotations[PoolIDAnnotationKey]
	if poolID == "" {
		return "", nil
	}
	pools, err := k.kapsule.ListPools(ctx)
	if err != nil {
		return "", fmt.Errorf("list pools: %w", err)
	}
	var pool *scaleway.KapsulePool
	for _, p := range pools {
		if p.ID == poolID {
			pool = p
			break
		}
	}
	if pool == nil {
		return "", nil
	}
	if expected := nc.Labels[corev1.LabelInstanceTypeStable]; expected != "" && pool.NodeType != expected {
		return drift.ReasonInstanceType, nil
	}
	if expected := nc.Labels[corev1.LabelTopologyZone]; expected != "" && string(pool.Zone) != expected {
		return drift.ReasonZone, nil
	}
	if pool.Size != 1 {
		return drift.ReasonPoolResize, nil
	}
	return "", nil
}

// GetInstanceTypes returns the embedded catalog filtered by the NodePool's
// requirements (and the NodeClass's spec.zones whitelist) with live prices
// hydrated from the pricing provider. Offerings that recently ended in a
// Scaleway creation_error are reported as unavailable (see
// unavailableOfferings) so Karpenter falls back to another type or zone.
func (k *Kapsule) GetInstanceTypes(ctx context.Context, np *karpv1.NodePool) ([]*karpcp.InstanceType, error) {
	nodeClass, err := k.fetchNodeClassFromNodePool(ctx, np)
	if err != nil {
		return nil, err
	}
	types := instancetype.Filter(np, nodeClass.Spec.Zones)
	out := k.pricing.Hydrate(types)
	k.unavailable.Apply(out)
	return out, nil
}

// Healthcheck calls KapsuleAPI.GetCluster — proves Scaleway K8s API auth
// works and that the configured cluster still exists.
func (k *Kapsule) Healthcheck(ctx context.Context) error {
	_, err := k.kapsule.GetCluster(ctx)
	return err
}

// Create provisions a Kapsule node pool of size 1, waits for the matching
// node to reach Scaleway NodeStatusReady, and returns the hydrated NodeClaim.
//
// Kapsule pools manage instance image, kubelet config and TLS bootstrap
// internally — the backend only needs the InstanceType and pool tags.
// Spec fields ImageLabel, RootVolumeSizeGB, RootVolumeType and PublicIP are
// silently ignored here; the NodeClass reconciler surfaces them under
// status.unusedFieldsForBackend so configuration mistakes stay visible.
func (k *Kapsule) Create(ctx context.Context, nc *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	metrics.Mark(k.log, "B", "T0", nc.Name)

	preflightStart := time.Now()
	zone, instanceType, nodeClass, err := k.preflight(ctx, nc)
	if err != nil {
		metrics.MarkRejected(backendName, zone, time.Since(preflightStart))
		return nil, err
	}

	var out *karpv1.NodeClaim
	err = metrics.MeasureCreate(backendName, zone, func() error {
		var inner error
		out, inner = k.createInZone(ctx, nc, zone, instanceType, nodeClass)
		return inner
	})
	return out, err
}

// preflight runs the synchronous, no-Scaleway-API checks that gate a
// Create call. Returning a non-nil error here translates to outcome=rejected
// in the provisioning histogram (see metrics.MarkRejected).
//
// The NodeClass is fetched first so its spec.zones can serve as a fallback
// for zoneCandidates when the NodeClaim itself carries no zone requirement.
func (k *Kapsule) preflight(ctx context.Context, nc *karpv1.NodeClaim) (string, string, *v1alpha1.ScalewayNodeClass, error) {
	types, err := instanceTypeCandidates(nc)
	if err != nil {
		return "", "", nil, fmt.Errorf("select instance type: %w", err)
	}
	nodeClass, err := k.fetchNodeClass(ctx, nc)
	if err != nil {
		return "", "", nil, fmt.Errorf("fetch nodeclass: %w", err)
	}
	zones, err := zoneCandidates(nc, nodeClass.Spec.Zones)
	if err != nil {
		return "", "", nil, fmt.Errorf("select zone: %w", err)
	}
	instanceType, zone := selection.Cheapest(types, selection.AllowedZones(zones, nodeClass.Spec.Zones), k.price)
	if err := guardZoneAllowed(zone, nodeClass.Spec.Zones); err != nil {
		return zone, "", nil, err
	}
	if err := k.quotaPreflight(ctx); err != nil {
		return zone, "", nil, err
	}
	return zone, instanceType, nodeClass, nil
}

// quotaPreflight observes the cluster-wide pool count, refreshes the
// observability gauges, and rejects Create when the configured threshold is
// reached. Rejection here is the quota backpressure path.
//
// CountPools failures are downgraded to a debug log: missing observability is
// strictly preferable to blocking provisioning when Scaleway has a transient
// hiccup. Karpenter still gets normal scheduling pressure feedback through
// the actual CreatePool call further down the path.
func (k *Kapsule) quotaPreflight(ctx context.Context) error {
	total, karpManaged, err := k.kapsule.CountPools(ctx)
	if err != nil {
		k.log.V(1).Info("quota observation failed", "err", err)
		return nil
	}
	observeQuota(total, karpManaged, k.quota.Limit)
	if err := k.quota.check(total); err != nil {
		poolQuotaRejections.Inc()
		k.log.Info("kapsule quota threshold reached, rejecting Create",
			"total", total, "karpenter_managed", karpManaged,
			"threshold", k.quota.Threshold, "limit", k.quota.Limit)
		return err
	}
	return nil
}

func (k *Kapsule) createInZone(ctx context.Context, nc *karpv1.NodeClaim, zone string, instanceType string, nodeClass *v1alpha1.ScalewayNodeClass) (*karpv1.NodeClaim, error) {
	metrics.Mark(k.log, "A", "T0", nc.Name)
	pool, err := k.kapsule.CreatePool(ctx, scaleway.CreatePoolParams{
		Zone:         scw.Zone(zone),
		InstanceType: instanceType,
		Name:         nc.Name,
		ExtraTags:    flattenTags(nodeClass.Spec.Tags),
	})
	if err != nil {
		return nil, fmt.Errorf("kapsule create pool: %w", err)
	}

	created := false
	defer func() {
		if !created {
			k.deleteFailedPool(nc.Name, pool.ID)
		}
	}()

	// CreatePool is idempotent: on a name conflict it returns the pool an
	// earlier Create (e.g. a previous leader) made for this NodeClaim, whose
	// type and zone may differ from this call's choice. The NodeClaim is
	// labelled from the pool itself so drift detection does not flag it;
	// a pool whose type or zone is not a candidate is deleted rather than
	// adopted.
	if pool.NodeType != instanceType || string(pool.Zone) != zone {
		k.log.Info("kapsule pool already exists for NodeClaim with a different offering",
			"nodeclaim", nc.Name, "pool", pool.ID,
			"poolInstanceType", pool.NodeType, "poolZone", pool.Zone,
			"requestedInstanceType", instanceType, "requestedZone", zone)
		if err := checkAdoptable(nc, pool, nodeClass.Spec.Zones); err != nil {
			return nil, fmt.Errorf("existing pool %s for NodeClaim %s: %w; deleting it", pool.ID, nc.Name, err)
		}
		instanceType, zone = pool.NodeType, string(pool.Zone)
	}

	node, err := k.kapsule.WaitForNodeReady(ctx, pool.ID, createTimeout)
	if errors.Is(err, scaleway.ErrNodeCreationError) {
		// Scaleway could not provision the instance — assumed to be a
		// capacity shortage, see scaleway.ErrNodeCreationError.
		// The deferred DeletePool cleans the failed pool; masking the
		// offering and returning InsufficientCapacityError makes Karpenter
		// delete the NodeClaim and relaunch on another type or zone instead
		// of retrying the same one.
		k.unavailable.MarkUnavailable(instanceType, zone)
		k.log.Info("kapsule node creation_error, marking offering unavailable",
			"nodeclaim", nc.Name, "pool", pool.ID, "instanceType", instanceType, "zone", zone,
			"ttl", unavailableOfferingTTL)
		return nil, karpcp.NewInsufficientCapacityError(fmt.Errorf("pool %s (%s in %s): %w", pool.ID, instanceType, zone, err))
	}
	if err != nil {
		return nil, fmt.Errorf("wait for node in pool %s: %w", pool.ID, err)
	}
	metrics.Mark(k.log, "A", "T1", nc.Name)

	created = true

	out := nc.DeepCopy()
	if out.Annotations == nil {
		out.Annotations = map[string]string{}
	}
	out.Annotations[PoolIDAnnotationKey] = pool.ID
	out.Status.ProviderID = node.ProviderID
	out.Status.NodeName = node.Name
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	out.Labels[corev1.LabelInstanceTypeStable] = instanceType
	out.Labels[corev1.LabelTopologyZone] = zone
	out.Labels[karpv1.CapacityTypeLabelKey] = karpv1.CapacityTypeOnDemand

	metrics.Mark(k.log, "B", "T1", nc.Name)
	k.log.Info("created NodeClaim",
		"name", nc.Name,
		"pool", pool.ID,
		"providerID", out.Status.ProviderID,
		"node", out.Status.NodeName,
	)
	return out, nil
}

// deleteFailedPool removes a pool whose node never became Ready. Retryable
// failures (pool still "scaling", 429, 5xx — see scaleway.IsRetryable) are
// retried until failedPoolDeleteTimeout; every failure is logged so a leaked
// pool is visible. It runs on a fresh context: the caller's ctx may already
// be cancelled (that is often why the wait failed).
func (k *Kapsule) deleteFailedPool(ncName, poolID string) {
	ctx, cancel := context.WithTimeout(context.Background(), k.failedPoolDeleteTimeout)
	defer cancel()
	for attempt := 1; ; attempt++ {
		err := k.kapsule.DeletePool(ctx, poolID)
		if err == nil {
			if attempt > 1 {
				k.log.Info("deleted failed kapsule pool", "nodeclaim", ncName, "pool", poolID, "attempts", attempt)
			}
			return
		}
		if !scaleway.IsRetryable(err) {
			k.log.Error(err, "failed to delete failed kapsule pool, giving up (pool may be leaked)",
				"nodeclaim", ncName, "pool", poolID, "attempts", attempt)
			return
		}
		k.log.Info("delete of failed kapsule pool rejected, retrying",
			"nodeclaim", ncName, "pool", poolID, "attempt", attempt, "err", err.Error())
		select {
		case <-ctx.Done():
			k.log.Error(err, "timed out deleting failed kapsule pool (pool may be leaked)",
				"nodeclaim", ncName, "pool", poolID, "attempts", attempt, "timeout", k.failedPoolDeleteTimeout)
			return
		case <-time.After(k.failedPoolDeleteRetryInterval):
		}
	}
}

// Delete removes the Kapsule pool backing the NodeClaim, which also
// terminates the underlying instance. Per the Karpenter contract Delete
// returns NodeClaimNotFoundError once the pool is confirmed gone.
func (k *Kapsule) Delete(ctx context.Context, nc *karpv1.NodeClaim) error {
	poolID := nc.Annotations[PoolIDAnnotationKey]
	if poolID == "" {
		// Fallback: scan pools for the nodeclaim=<name> tag.
		pools, err := k.kapsule.ListPools(ctx)
		if err != nil {
			return fmt.Errorf("list kapsule pools: %w", err)
		}
		for _, p := range pools {
			if p.NodeClaim == nc.Name {
				poolID = p.ID
				break
			}
		}
	}
	if poolID == "" {
		k.log.Info("delete: no pool found", "nodeclaim", nc.Name)
		return karpcp.NewNodeClaimNotFoundError(fmt.Errorf("no pool for NodeClaim %s", nc.Name))
	}
	k.log.Info("delete: removing pool", "nodeclaim", nc.Name, "pool", poolID)
	if err := k.kapsule.DeletePool(ctx, poolID); err != nil {
		return fmt.Errorf("kapsule delete pool %s: %w", poolID, err)
	}
	k.log.Info("delete: pool removed", "nodeclaim", nc.Name, "pool", poolID)
	return karpcp.NewNodeClaimNotFoundError(fmt.Errorf("pool %s deleted", poolID))
}

// Get finds the NodeClaim for a given providerID by scanning karpenter-managed
// pools and their nodes. O(pools × nodes) — acceptable at current scale.
func (k *Kapsule) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	pools, err := k.kapsule.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pools: %w", err)
	}
	for _, pool := range pools {
		nodes, err := k.kapsule.ListNodesForPool(ctx, pool.ID)
		if err != nil {
			k.log.V(1).Info("list nodes failed (skipping pool)", "pool", pool.ID, "err", err)
			continue
		}
		for _, n := range nodes {
			if n.ProviderID == providerID {
				return kapsuleNodeToNodeClaim(pool, n), nil
			}
		}
	}
	return nil, karpcp.NewNodeClaimNotFoundError(fmt.Errorf("no node with providerID %s", providerID))
}

// List returns one NodeClaim per Karpenter-managed Kapsule pool. Pools that
// are still scaling (no node yet) are returned with empty providerID.
func (k *Kapsule) List(ctx context.Context) ([]*karpv1.NodeClaim, error) {
	pools, err := k.kapsule.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pools: %w", err)
	}
	var out []*karpv1.NodeClaim
	for _, pool := range pools {
		nodes, err := k.kapsule.ListNodesForPool(ctx, pool.ID)
		if err != nil {
			k.log.V(1).Info("list nodes failed (skipping pool)", "pool", pool.ID, "err", err)
			continue
		}
		if len(nodes) == 0 {
			out = append(out, kapsuleNodeToNodeClaim(pool, nil))
			continue
		}
		for _, n := range nodes {
			out = append(out, kapsuleNodeToNodeClaim(pool, n))
		}
	}
	return out, nil
}

// --- internals ---

func (k *Kapsule) fetchNodeClass(ctx context.Context, nc *karpv1.NodeClaim) (*v1alpha1.ScalewayNodeClass, error) {
	if nc.Spec.NodeClassRef == nil || nc.Spec.NodeClassRef.Name == "" {
		return nil, fmt.Errorf("NodeClaim %s has no NodeClassRef", nc.Name)
	}
	nodeClass := &v1alpha1.ScalewayNodeClass{}
	if err := k.kubeClient.Get(ctx, types.NamespacedName{Name: nc.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return nil, err
	}
	return nodeClass, nil
}

// instanceTypeCandidates returns every instance type the NodeClaim allows.
// Karpenter serializes the requirement as a set, i.e. in alphabetical order
// (PRO2-S before PRO2-XXS), not by price: the price-aware pick happens in
// selection.Cheapest.
func instanceTypeCandidates(nc *karpv1.NodeClaim) ([]string, error) {
	for _, r := range nc.Spec.Requirements {
		if r.Key != corev1.LabelInstanceTypeStable {
			continue
		}
		if r.Operator != corev1.NodeSelectorOpIn || len(r.Values) == 0 {
			return nil, fmt.Errorf("instance-type requirement must be In with at least 1 value, got op=%s n=%d", r.Operator, len(r.Values))
		}
		return r.Values, nil
	}
	return nil, fmt.Errorf("NodeClaim %s has no instance-type requirement", nc.Name)
}

// zoneCandidates returns the zones the NodeClaim allows, in requirement
// order. When the NodeClaim carries no zone requirement (e.g. a NodePool
// without an explicit topology.kubernetes.io/zone constraint) it falls back
// to the NodeClass spec.zones whitelist. The final pick, by price,
// happens in selection.Cheapest.
func zoneCandidates(nc *karpv1.NodeClaim, fallbackZones []string) ([]string, error) {
	for _, r := range nc.Spec.Requirements {
		if r.Key != corev1.LabelTopologyZone {
			continue
		}
		if r.Operator != corev1.NodeSelectorOpIn || len(r.Values) == 0 {
			return nil, fmt.Errorf("zone requirement must be In with at least 1 value, got op=%s n=%d", r.Operator, len(r.Values))
		}
		return r.Values, nil
	}
	if len(fallbackZones) > 0 {
		return fallbackZones, nil
	}
	return nil, fmt.Errorf("NodeClaim %s has no topology.kubernetes.io/zone requirement and the referenced NodeClass declares no spec.zones fallback", nc.Name)
}

// price feeds selection.Cheapest: the live (or catalog baseline) EUR/h of
// the offering, excluding offerings masked after a recent creation_error.
func (k *Kapsule) price(zone, instanceType string) (float64, bool) {
	if k.unavailable.IsUnavailable(instanceType, zone) {
		return 0, false
	}
	return k.pricing.Price(scw.Zone(zone), instanceType)
}

// checkAdoptable reports whether a pool that already exists for the NodeClaim
// (returned by the idempotent CreatePool) satisfies the NodeClaim: its type
// must be an instance-type candidate and its zone a zone candidate allowed by
// the NodeClass spec.zones whitelist.
func checkAdoptable(nc *karpv1.NodeClaim, pool *scaleway.KapsulePool, whitelist []string) error {
	types, err := instanceTypeCandidates(nc)
	if err != nil {
		return err
	}
	if !slices.Contains(types, pool.NodeType) {
		return fmt.Errorf("instance type %s not among candidates %v", pool.NodeType, types)
	}
	zones, err := zoneCandidates(nc, whitelist)
	if err != nil {
		return err
	}
	if !slices.Contains(zones, string(pool.Zone)) {
		return fmt.Errorf("zone %s not among candidates %v", pool.Zone, zones)
	}
	return guardZoneAllowed(string(pool.Zone), whitelist)
}

// guardZoneAllowed returns nil when the zone is in the whitelist (or the
// whitelist is empty/unset, i.e. unconstrained).
func guardZoneAllowed(zone string, whitelist []string) error {
	if len(whitelist) == 0 {
		return nil
	}
	for _, w := range whitelist {
		if w == zone {
			return nil
		}
	}
	return fmt.Errorf("zone %s not in NodeClass spec.zones whitelist %v", zone, whitelist)
}

// fetchNodeClassFromNodePool resolves the NodeClass for a NodePool. Unlike
// fetchNodeClass (NodeClaim-side), missing NodeClassRef is tolerated and
// returns an empty NodeClass (= no zone whitelist). GetInstanceTypes is
// called speculatively by the Karpenter scheduler — including on partially
// configured NodePools during reconciliation — and must degrade to "all
// zones eligible" rather than error. Real misconfigurations are caught by
// Karpenter's NodePool admission, not here.
func (k *Kapsule) fetchNodeClassFromNodePool(ctx context.Context, np *karpv1.NodePool) (*v1alpha1.ScalewayNodeClass, error) {
	if np == nil || np.Spec.Template.Spec.NodeClassRef == nil {
		return &v1alpha1.ScalewayNodeClass{}, nil
	}
	nodeClass := &v1alpha1.ScalewayNodeClass{}
	if err := k.kubeClient.Get(ctx, types.NamespacedName{Name: np.Spec.Template.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return nil, err
	}
	return nodeClass, nil
}

func kapsuleNodeToNodeClaim(pool *scaleway.KapsulePool, node *scaleway.KapsuleNode) *karpv1.NodeClaim {
	nc := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: pool.NodeClaim,
			Annotations: map[string]string{
				PoolIDAnnotationKey: pool.ID,
			},
			Labels: map[string]string{
				corev1.LabelInstanceTypeStable: pool.NodeType,
				corev1.LabelTopologyZone:       string(pool.Zone),
				karpv1.CapacityTypeLabelKey:    karpv1.CapacityTypeOnDemand,
			},
		},
	}
	if node != nil {
		nc.Status.ProviderID = node.ProviderID
		nc.Status.NodeName = node.Name
	}
	return nc
}

func flattenTags(tags map[string]string) []string {
	if len(tags) == 0 {
		return nil
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(tags))
	for _, key := range keys {
		out = append(out, key+"="+tags[key])
	}
	return out
}
