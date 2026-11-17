package nodeclass

import (
	"context"
	"errors"
	"fmt"
	"time"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/pkg/backend"
	"github.com/no-inspi/karpenter-scaleway/pkg/instancetype"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	"github.com/awslabs/operatorpkg/status"
	"github.com/go-logr/logr"
	"github.com/scaleway/scaleway-sdk-go/scw"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// ImageCacheTTL is the validity of a cached Marketplace lookup. Stable
	// per design — shorter would hammer the API, longer would surface
	// retired labels too late.
	ImageCacheTTL = 5 * time.Minute

	// requeueOnImageNotFound is how long to wait before re-checking the
	// Marketplace after a miss. The user typically fixes the typo within
	// minutes; 30s lets them iterate without burning quota.
	requeueOnImageNotFound = 30 * time.Second

	// requeueOnTransientError is the retry delay when the Marketplace API
	// returns 5xx. Tighter than ImageNotFound — the underlying issue is
	// usually transient and the operator wants Ready=True ASAP.
	requeueOnTransientError = 10 * time.Second
)

// Reconciler reconciles ScalewayNodeClass objects: validates the spec,
// resolves the Marketplace image, and writes the Ready condition on the
// status subresource.
type Reconciler struct {
	kubeClient  client.Client
	marketplace scaleway.MarketplaceAPI
	cache       *imageCache
	backendKind backend.Kind
	log         logr.Logger
}

// New returns a configured reconciler. The cache is shared across
// reconciles so steady-state load on the Marketplace stays low.
func New(kc client.Client, mkt scaleway.MarketplaceAPI, kind backend.Kind, log logr.Logger) *Reconciler {
	return &Reconciler{
		kubeClient:  kc,
		marketplace: mkt,
		cache:       newImageCache(ImageCacheTTL),
		backendKind: kind,
		log:         log,
	}
}

// SetupWithManager wires the reconciler to a controller-runtime manager.
func (r *Reconciler) SetupWithManager(mgr manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(mgr).
		For(&v1alpha1.ScalewayNodeClass{}).
		Named("scalewaynodeclass").
		Complete(r)
}

// Reconcile is the controller-runtime entrypoint. It is safe to call
// concurrently for distinct NodeClass names.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	nc := &v1alpha1.ScalewayNodeClass{}
	if err := r.kubeClient.Get(ctx, req.NamespacedName, nc); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	stored := nc.DeepCopy()

	// 1. Compute fields the resolved backend will silently ignore. This
	//    is informational and never blocks Ready=True.
	nc.Status.UnusedFieldsForBackend = computeUnusedFieldsForBackend(nc.Spec, r.backendKind)

	// 2. Resolve effective zones: explicit whitelist or all 9 zones.
	//    The kubebuilder Enum validation already rejects unknown zone strings
	//    at admission; this is a defensive check against the embedded
	//    instance-type catalog so a zone with no offerings surfaces as
	//    Ready=False/ZoneUnknown instead of silently producing an empty
	//    candidate set during scheduling.
	effectiveZones := nc.Spec.Zones
	if len(effectiveZones) == 0 {
		effectiveZones = defaultAllZones
	}
	known := knownZonesFromCatalog()
	if len(known) == 0 {
		// TODO: remove this branch once catalog.json is always non-empty
		// in production builds. The placeholder catalog contains no offerings; without this skip every
		// NodeClass would Ready=False with ZoneUnknown. Once P11 lands a
		// real catalog, an empty map is a real failure (corrupted embed,
		// parse bug) and must surface — not be silently tolerated.
		r.log.Info("instance catalog empty, skipping per-zone validation",
			"nodeclass", nc.Name)
	} else {
		for _, z := range effectiveZones {
			if _, ok := known[z]; !ok {
				nc.StatusConditions().SetFalse(status.ConditionReady,
					v1alpha1.ConditionReasonZoneUnknown,
					fmt.Sprintf("zone %s has no known offering in the catalog", z))
				if patchErr := r.patchStatus(ctx, nc, stored); patchErr != nil {
					return reconcile.Result{}, patchErr
				}
				return reconcile.Result{}, nil
			}
		}
	}
	nc.Status.ResolvedZones = effectiveZones

	// 3. Resolve the image label via the Marketplace cache.
	requeue, err := r.resolveImage(ctx, nc, effectiveZones)
	if err != nil {
		// Persist whatever status we set even on transient errors so the
		// user sees the Ready=False/ValidationPending while we retry.
		if patchErr := r.patchStatus(ctx, nc, stored); patchErr != nil {
			return reconcile.Result{}, fmt.Errorf("patch status: %w (original: %w)", patchErr, err)
		}
		return reconcile.Result{RequeueAfter: requeue}, nil
	}

	if err := r.patchStatus(ctx, nc, stored); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{RequeueAfter: requeue}, nil
}

// resolveImage rounds-trips the Marketplace (via the cache) and writes
// the Ready condition. Returns the requeue delay and a non-nil error if
// the controller-runtime should observe the validation failure.
//
// Marketplace images are replicated across the zones of a region, so a
// single lookup against any effective zone resolves the same image UUID.
// We pick the first effective zone for determinism; the cache key remains
// (zone, label) so per-zone lookups still cache correctly across
// reconciles even if the effective zone list shifts between reconciles.
func (r *Reconciler) resolveImage(ctx context.Context, nc *v1alpha1.ScalewayNodeClass, effectiveZones []string) (time.Duration, error) {
	zone := effectiveZones[0]
	label := nc.Spec.ImageLabel

	if imageID, ok := r.cache.get(zone, label); ok {
		nc.Status.ResolvedImageID = imageID
		nc.Status.ValidatedAt = &metav1.Time{Time: time.Now().UTC()}
		nc.StatusConditions().SetTrue(status.ConditionReady)
		return 0, nil
	}

	imageID, err := r.marketplace.LookupImage(ctx, scw.Zone(zone), label)
	if err == nil {
		r.cache.put(zone, label, imageID)
		nc.Status.ResolvedImageID = imageID
		nc.Status.ValidatedAt = &metav1.Time{Time: time.Now().UTC()}
		nc.StatusConditions().SetTrue(status.ConditionReady)
		return 0, nil
	}

	// Failure paths drop any cached entry so a recovered Marketplace can
	// re-populate immediately without waiting for TTL expiry.
	r.cache.invalidate(zone, label)
	if errors.Is(err, scaleway.ErrImageNotFound) {
		nc.StatusConditions().SetFalse(status.ConditionReady,
			v1alpha1.ConditionReasonImageNotFound,
			fmt.Sprintf("image label %q not found in zone %q", label, zone))
		r.log.Info("image not found", "zone", zone, "label", label)
		return requeueOnImageNotFound, err
	}

	nc.StatusConditions().SetFalse(status.ConditionReady,
		v1alpha1.ConditionReasonValidationPending,
		fmt.Sprintf("marketplace API unavailable: %v", err))
	r.log.Error(err, "marketplace lookup failed (transient)", "zone", zone, "label", label)
	return requeueOnTransientError, err
}

// patchStatus writes the status subresource. Using
// client.Status() avoids overwriting the spec under concurrent
// `kubectl apply` calls.
func (r *Reconciler) patchStatus(ctx context.Context, nc, stored *v1alpha1.ScalewayNodeClass) error {
	return r.kubeClient.Status().Patch(ctx, nc, client.MergeFrom(stored))
}

// computeUnusedFieldsForBackend lists the spec fields the resolved backend
// will silently ignore. Surfaced via status so misconfigurations are
// visible in `kubectl describe`.
func computeUnusedFieldsForBackend(spec v1alpha1.ScalewayNodeClassSpec, kind backend.Kind) []string {
	if kind != backend.KindKapsule {
		return nil
	}
	var unused []string
	if spec.RootVolumeType != "" {
		unused = append(unused, "rootVolumeType")
	}
	if spec.PublicIP != nil {
		unused = append(unused, "publicIP")
	}
	return unused
}

// defaultAllZones is the canonical full Scaleway zone list as plain strings,
// used when spec.zones is omitted. Sourced from scaleway.AllZones so a zone
// addition in the SDK (or a retirement) flows in naturally.
var defaultAllZones = scaleway.AllZoneStrings()

// knownZonesFromCatalog walks the embedded instance-type catalog and
// returns the set of zones that have at least one offering. Used to
// reject NodeClasses that target a zone with no instance types.
//
// Returns an empty map when the catalog is empty (e.g. the placeholder
// catalog before P11). The reconciler treats an empty result as
// "validation skipped" so admission is gated only by the kubebuilder
// Enum until live data is embedded.
//
// Exposed as a var so tests can substitute a known set without having
// to mutate the package-level embedded catalog.
var knownZonesFromCatalog = func() map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range instancetype.All() {
		for _, o := range t.Offerings {
			if req := o.Requirements.Get(corev1.LabelTopologyZone); req != nil {
				for _, v := range req.Values() {
					out[v] = struct{}{}
				}
			}
		}
	}
	return out
}
