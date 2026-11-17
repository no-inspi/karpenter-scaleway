package kosmos

import (
	"context"
	"fmt"
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
	// InstanceIDAnnotationKey records the Scaleway Instance UUID on the
	// NodeClaim. Karpenter ignores unknown annotations and propagates them
	// across reconciles.
	InstanceIDAnnotationKey = "karpenter.scaleway.com/instance-id"

	backendName = "kosmos"

	createTimeout = 6 * time.Minute
	pollInterval  = 5 * time.Second
)

// Options bundles dependencies for New. All fields are required.
type Options struct {
	// Instance is the Scaleway Instance API client (mockable interface).
	Instance scaleway.InstanceAPI
	// K8s is the Scaleway K8s API client used for healthchecks.
	K8s scaleway.K8sAPI
	// KubeClient is the kube-apiserver client used to fetch NodeClass and
	// poll Nodes during Create.
	KubeClient client.Client
	// ClusterID is the Scaleway-managed Kosmos cluster ID; surfaced via
	// healthchecks.
	ClusterID string
	// Pricing is the live price provider used to hydrate instance type
	// offerings returned by GetInstanceTypes.
	Pricing *pricing.Provider
	// Log is the structured logger; may be a no-op logger.
	Log logr.Logger
}

// Kosmos is the backend.Backend implementation for Scaleway Kosmos
// (multicloud) clusters.
type Kosmos struct {
	instance   scaleway.InstanceAPI
	k8s        scaleway.K8sAPI
	kubeClient client.Client
	clusterID  string
	pricing    *pricing.Provider
	log        logr.Logger
}

// New validates Options and returns a ready-to-use Kosmos backend.
func New(opts Options) (*Kosmos, error) {
	switch {
	case opts.Instance == nil:
		return nil, fmt.Errorf("kosmos: Instance API is required")
	case opts.K8s == nil:
		return nil, fmt.Errorf("kosmos: K8s API is required")
	case opts.KubeClient == nil:
		return nil, fmt.Errorf("kosmos: KubeClient is required")
	case opts.ClusterID == "":
		return nil, fmt.Errorf("kosmos: ClusterID is required")
	case opts.Pricing == nil:
		return nil, fmt.Errorf("kosmos: Pricing is required")
	}
	return &Kosmos{
		instance:   opts.Instance,
		k8s:        opts.K8s,
		kubeClient: opts.KubeClient,
		clusterID:  opts.ClusterID,
		pricing:    opts.Pricing,
		log:        opts.Log,
	}, nil
}

// Name returns the backend identifier — stable string used in metrics labels
// and log prefixes.
func (k *Kosmos) Name() string { return backendName }

// IsDrifted compares the live Scaleway Instance (and the current
// NodeClass spec) against the state captured on the NodeClaim at Create
// time and returns a structured DriftReason when they diverge. Karpenter
// calls this once per minute per NodeClaim, so the implementation issues
// a single Get round-trip and avoids any cache (Karpenter v1.10 already
// QPS-limits the call).
//
// The escape hatch is the SkipAnnotationKey annotation on the NodeClaim
// — operators set it during one-off interventions to keep Karpenter from
// recycling the NodeClaim.
//
// Drift sources covered:
//   - Tags: the karpenter correlation tag was stripped (we can no longer
//     prove the instance belongs to this NodeClaim).
//   - InstanceType: instance.Type != NodeClaim instance-type label.
//   - Zone: instance.Zone != NodeClaim topology label.
//   - ImageLabel: NodeClass.Spec.ImageLabel changed since Create
//     (compared against the snapshot annotation Create writes).
//
// An instance that has disappeared (404) is not drift — Karpenter's GC
// observes the missing instance via List/Get and recycles the NodeClaim
// through the NodeClaimNotFound path.
func (k *Kosmos) IsDrifted(ctx context.Context, nc *karpv1.NodeClaim) (karpcp.DriftReason, error) {
	if nc.Annotations[drift.SkipAnnotationKey] == "true" {
		return "", nil
	}
	instanceID := nc.Annotations[InstanceIDAnnotationKey]
	zone := scw.Zone(nc.Labels[corev1.LabelTopologyZone])
	if instanceID == "" || zone == "" {
		return "", nil
	}
	inst, err := k.instance.Get(ctx, zone, instanceID)
	if err != nil {
		if scaleway.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get instance %s: %w", instanceID, err)
	}
	if inst.NodeClaim != nc.Name {
		return drift.ReasonTags, nil
	}
	if expected := nc.Labels[corev1.LabelInstanceTypeStable]; expected != "" && inst.Type != expected {
		return drift.ReasonInstanceType, nil
	}
	if expected := string(zone); expected != "" && string(inst.Zone) != expected {
		return drift.ReasonZone, nil
	}
	if snapshot := nc.Annotations[drift.ImageLabelSnapshotAnnotationKey]; snapshot != "" {
		nodeClass, err := k.fetchNodeClass(ctx, nc)
		if err != nil {
			// NodeClass deletion is not our drift signal — the
			// NodeClass reconciler / Karpenter handles that path.
			return "", nil
		}
		if nodeClass.Spec.ImageLabel != snapshot {
			return drift.ReasonImageLabel, nil
		}
	}
	return "", nil
}

// GetInstanceTypes returns the embedded catalog filtered by the NodePool's
// requirements (and the NodeClass's spec.zones whitelist) with live prices
// hydrated from the pricing provider.
func (k *Kosmos) GetInstanceTypes(ctx context.Context, np *karpv1.NodePool) ([]*karpcp.InstanceType, error) {
	nodeClass, err := k.fetchNodeClassFromNodePool(ctx, np)
	if err != nil {
		return nil, err
	}
	types := instancetype.Filter(np, nodeClass.Spec.Zones)
	// The Instance API default root volume size depends on the image, so
	// ephemeral-storage is only advertised when the NodeClass sets it.
	if nodeClass.Spec.RootVolumeSizeGB != nil {
		instancetype.ApplyRootVolume(types, nodeClass.Spec.RootVolumeSizeGB)
	}
	return k.pricing.Hydrate(types), nil
}

// Healthcheck calls K8s.GetCluster(clusterID); a successful round-trip
// proves both API auth and that the configured cluster still exists.
func (k *Kosmos) Healthcheck(ctx context.Context) error {
	_, err := k.k8s.GetCluster(ctx, k.clusterID)
	return err
}

// Create launches a Scaleway Instance for the NodeClaim, waits for the
// matching Kosmos Node to reach Ready, and returns the hydrated NodeClaim.
//
// Correlation Instance ↔ Node is done via the public IPv4: the Kosmos providerID (scaleway://external/unknown/<kosmos-uuid>)
// is opaque and unrelated to the Scaleway Instance UUID, so we cannot use
// it for lookups. The Instance UUID is persisted on the NodeClaim as the
// annotation karpenter.scaleway.com/instance-id for the Delete path.
func (k *Kosmos) Create(ctx context.Context, nc *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	metrics.Mark(k.log, "B", "T0", nc.Name)

	preflightStart := time.Now()
	zone, commercialType, nodeClass, err := k.preflight(ctx, nc)
	if err != nil {
		metrics.MarkRejected(backendName, zone, time.Since(preflightStart))
		return nil, err
	}

	var out *karpv1.NodeClaim
	err = metrics.MeasureCreate(backendName, zone, func() error {
		var inner error
		out, inner = k.createInZone(ctx, nc, zone, commercialType, nodeClass)
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
func (k *Kosmos) preflight(ctx context.Context, nc *karpv1.NodeClaim) (string, string, *v1alpha1.ScalewayNodeClass, error) {
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
	commercialType, zone := selection.Cheapest(types, selection.AllowedZones(zones, nodeClass.Spec.Zones), k.price)
	if err := guardZoneAllowed(zone, nodeClass.Spec.Zones); err != nil {
		return zone, "", nil, err
	}
	return zone, commercialType, nodeClass, nil
}

func (k *Kosmos) createInZone(ctx context.Context, nc *karpv1.NodeClaim, zone string, commercialType string, nodeClass *v1alpha1.ScalewayNodeClass) (*karpv1.NodeClaim, error) {
	metrics.Mark(k.log, "A", "T0", nc.Name)
	inst, err := k.instance.Create(ctx, scaleway.CreateInstanceParams{
		Zone:             scw.Zone(zone),
		InstanceType:     commercialType,
		Name:             nc.Name,
		ImageLabel:       nodeClass.Spec.ImageLabel,
		Tags:             flattenTags(nodeClass.Spec.Tags),
		RootVolumeSizeGB: nodeClass.Spec.RootVolumeSizeGB,
		RootVolumeType:   nodeClass.Spec.RootVolumeType,
		PublicIP:         nodeClass.Spec.PublicIP,
	})
	if err != nil {
		return nil, fmt.Errorf("scaleway create: %w", err)
	}

	created := false
	defer func() {
		if !created {
			// Survives ctx cancellation; failure surfaces as orphan
			// instances, not silent leaks.
			_ = k.instance.Delete(context.Background(), scw.Zone(zone), inst.ID)
		}
	}()

	if inst.PublicIP == "" {
		return nil, fmt.Errorf("instance %s has no public IP — cannot correlate with kube Node", inst.ID)
	}

	node, err := k.waitForNodeReadyByPublicIP(ctx, inst.PublicIP)
	if err != nil {
		return nil, fmt.Errorf("wait for node ready: %w", err)
	}
	metrics.Mark(k.log, "A", "T1", nc.Name)

	created = true

	out := nc.DeepCopy()
	if out.Annotations == nil {
		out.Annotations = map[string]string{}
	}
	out.Annotations[InstanceIDAnnotationKey] = inst.ID
	// Snapshot the resolved imageLabel so drift detection can spot
	// a NodeClass.Spec.ImageLabel edit. We persist what was actually
	// requested at Create (which may equal nodeClass.Spec.ImageLabel or
	// the empty default — both round-trip to the same comparison).
	out.Annotations[drift.ImageLabelSnapshotAnnotationKey] = nodeClass.Spec.ImageLabel
	out.Status.ProviderID = node.Spec.ProviderID
	out.Status.NodeName = node.Name
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	out.Labels[corev1.LabelInstanceTypeStable] = commercialType
	out.Labels[corev1.LabelTopologyZone] = string(inst.Zone)
	out.Labels[karpv1.CapacityTypeLabelKey] = karpv1.CapacityTypeOnDemand

	metrics.Mark(k.log, "B", "T1", nc.Name)
	k.log.Info("created NodeClaim",
		"name", nc.Name,
		"instance", inst.ID,
		"providerID", out.Status.ProviderID,
		"node", out.Status.NodeName,
	)
	return out, nil
}

// Delete terminates the Scaleway Instance backing this NodeClaim. Per the
// Karpenter contract Delete must return
// NodeClaimNotFoundError once the Instance is confirmed gone — returning
// nil leaves the finalizer pinned and Karpenter retrying forever.
func (k *Kosmos) Delete(ctx context.Context, nc *karpv1.NodeClaim) error {
	id := nc.Annotations[InstanceIDAnnotationKey]
	zone := scw.Zone(nc.Labels[corev1.LabelTopologyZone])
	source := "annotation"
	// If the annotation path is missing the instance ID OR the zone
	// label (race / externally created NodeClaim), fall through to the
	// tag-lookup path which resolves both from the live instance list.
	// We list (not Get) because Get also requires a zone — so without
	// the zone label there is no faster path than scanning all 9 zones.
	if id == "" || zone == "" {
		insts, err := k.instance.List(ctx)
		if err != nil {
			return fmt.Errorf("list scaleway instances: %w", err)
		}
		for _, i := range insts {
			if i.NodeClaim == nc.Name {
				id = i.ID
				zone = i.Zone
				source = "tag-lookup"
				break
			}
		}
	}
	if id == "" {
		k.log.Info("delete: no underlying instance", "nodeclaim", nc.Name)
		return karpcp.NewNodeClaimNotFoundError(fmt.Errorf("no instance for NodeClaim %s", nc.Name))
	}
	if zone == "" {
		return fmt.Errorf("delete: cannot resolve zone for instance %s (nodeclaim %s)", id, nc.Name)
	}
	k.log.Info("delete: terminating instance", "nodeclaim", nc.Name, "instance", id, "zone", string(zone), "source", source)
	if err := k.instance.Delete(ctx, zone, id); err != nil {
		k.log.Error(err, "delete: scaleway terminate failed", "nodeclaim", nc.Name, "instance", id)
		return fmt.Errorf("scaleway delete %s: %w", id, err)
	}
	k.log.Info("delete: terminated", "nodeclaim", nc.Name, "instance", id)
	return karpcp.NewNodeClaimNotFoundError(fmt.Errorf("instance %s terminated", id))
}

// Get retrieves a NodeClaim view by providerID. Correlates providerID →
// kube Node → Instance via the externalIP.
func (k *Kosmos) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	node, err := k.findNodeByProviderID(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, karpcp.NewNodeClaimNotFoundError(fmt.Errorf("no node with providerID %s", providerID))
	}
	externalIP := nodeExternalIP(node)
	if externalIP == "" {
		return nil, karpcp.NewNodeClaimNotFoundError(fmt.Errorf("node %s has no external IP", node.Name))
	}
	insts, err := k.instance.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	for _, inst := range insts {
		if inst.PublicIP == externalIP {
			return scalewayInstanceToNodeClaim(inst, node), nil
		}
	}
	return nil, karpcp.NewNodeClaimNotFoundError(fmt.Errorf("no instance with publicIP %s", externalIP))
}

// List returns one NodeClaim per Karpenter-managed Scaleway Instance.
// Instances that have not yet registered a kube Node are returned with
// an empty providerID so Karpenter treats them as still-provisioning.
func (k *Kosmos) List(ctx context.Context) ([]*karpv1.NodeClaim, error) {
	insts, err := k.instance.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	var nodes corev1.NodeList
	if err := k.kubeClient.List(ctx, &nodes); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	nodeByExternalIP := map[string]*corev1.Node{}
	for i := range nodes.Items {
		if ip := nodeExternalIP(&nodes.Items[i]); ip != "" {
			nodeByExternalIP[ip] = &nodes.Items[i]
		}
	}
	out := make([]*karpv1.NodeClaim, 0, len(insts))
	for _, inst := range insts {
		out = append(out, scalewayInstanceToNodeClaim(inst, nodeByExternalIP[inst.PublicIP]))
	}
	return out, nil
}

// --- internals ---

func (k *Kosmos) fetchNodeClass(ctx context.Context, nc *karpv1.NodeClaim) (*v1alpha1.ScalewayNodeClass, error) {
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

// price feeds selection.Cheapest with the live (or catalog baseline) EUR/h
// of the offering.
func (k *Kosmos) price(zone, commercialType string) (float64, bool) {
	return k.pricing.Price(scw.Zone(zone), commercialType)
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
func (k *Kosmos) fetchNodeClassFromNodePool(ctx context.Context, np *karpv1.NodePool) (*v1alpha1.ScalewayNodeClass, error) {
	if np == nil || np.Spec.Template.Spec.NodeClassRef == nil {
		return &v1alpha1.ScalewayNodeClass{}, nil
	}
	nodeClass := &v1alpha1.ScalewayNodeClass{}
	if err := k.kubeClient.Get(ctx, types.NamespacedName{Name: np.Spec.Template.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return nil, err
	}
	return nodeClass, nil
}

func (k *Kosmos) waitForNodeReadyByPublicIP(ctx context.Context, publicIP string) (*corev1.Node, error) {
	deadline := time.Now().Add(createTimeout)
	t := time.NewTicker(pollInterval)
	defer t.Stop()

	for {
		var nodes corev1.NodeList
		if err := k.kubeClient.List(ctx, &nodes); err != nil {
			k.log.V(1).Info("list nodes error (transient)", "err", err)
		} else {
			for i := range nodes.Items {
				node := &nodes.Items[i]
				if nodeExternalIP(node) != publicIP {
					continue
				}
				if isNodeReady(node) {
					return node, nil
				}
				k.log.V(1).Info("found node, waiting Ready", "name", node.Name, "ip", publicIP)
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout %s waiting for node Ready (publicIP=%s)", createTimeout, publicIP)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func (k *Kosmos) findNodeByProviderID(ctx context.Context, providerID string) (*corev1.Node, error) {
	var nodes corev1.NodeList
	if err := k.kubeClient.List(ctx, &nodes); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	for i := range nodes.Items {
		if nodes.Items[i].Spec.ProviderID == providerID {
			return &nodes.Items[i], nil
		}
	}
	return nil, nil
}

func nodeExternalIP(n *corev1.Node) string {
	for _, addr := range n.Status.Addresses {
		if addr.Type == corev1.NodeExternalIP {
			return addr.Address
		}
	}
	return ""
}

func isNodeReady(n *corev1.Node) bool {
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func scalewayInstanceToNodeClaim(inst *scaleway.ScalewayInstance, node *corev1.Node) *karpv1.NodeClaim {
	name := inst.NodeClaim
	if name == "" {
		// Fallback for adopted Instances that lack the nodeclaim= tag.
		name = inst.Name
	}
	nc := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				InstanceIDAnnotationKey: inst.ID,
			},
			Labels: map[string]string{
				corev1.LabelInstanceTypeStable: inst.Type,
				corev1.LabelTopologyZone:       string(inst.Zone),
				karpv1.CapacityTypeLabelKey:    karpv1.CapacityTypeOnDemand,
			},
		},
	}
	if node != nil {
		nc.Status.ProviderID = node.Spec.ProviderID
		nc.Status.NodeName = node.Name
	}
	return nc
}

// flattenTags renders a NodeClass tags map as a deterministic list of
// "key=value" strings — Scaleway tags are flat so the map shape from the
// CRD must be encoded.
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
