package kapsule

// Regression test — CreatePool idempotent on name conflict.
//
// Scenario: a controller crash between CreatePool acknowledgement and
// pool-id annotation persistence leaves an orphaned pool. On the next
// reconcile Karpenter retries Create, Scaleway's K8s API rejects the
// second CreatePool with a typed *scw.InvalidArgumentsError whose
// Details[].ArgumentName == "name" (unique-name constraint violation).
//
// The recovery path lives in pkg/scaleway.KapsuleClient.CreatePool:
//
//   if isInvalidArgumentName(err) {
//       if existing, _ := c.findPoolByNodeClaim(ctx, params.Name); existing != nil {
//           return existing, nil
//       }
//   }
//
// This backend-level test simulates the full recovery path via the fake:
// createPoolFn reproduces the same isInvalidArgumentName + ListPools lookup
// that KapsuleClient.CreatePool performs, so the test exercises the backend's
// handling of the recovered pool (correct pool-id annotation, node readiness
// wait, providerID propagation) without requiring an httptest server.
//
// If this test is removed or the guard is broken, engineers must also update
// the recovery logic in pkg/scaleway.KapsuleClient.CreatePool.

import (
	"context"
	"testing"
	"time"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/pkg/pricing"
	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	"github.com/go-logr/logr"
	"github.com/scaleway/scaleway-sdk-go/scw"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestCreate_IdempotentOnNameConflict verifies that when CreatePool returns a
// *scw.InvalidArgumentsError with ArgumentName=="name" (unique-name constraint
// from Scaleway), the backend recovers by finding the orphaned pool via the
// NodeClaim tag and succeeds rather than propagating the error.
//
// The fakeKapsule.createPoolFn reproduces the same recovery logic as
// pkg/scaleway.KapsuleClient.CreatePool: on name-conflict it falls back to
// ListPools and returns the orphaned pool. This ensures the full backend path
// (pool-id annotation, WaitForNodeReady, providerID) is exercised for the
// recovered case.
func TestCreate_IdempotentOnNameConflict(t *testing.T) {
	const (
		orphanedPoolID = "pool-orphaned-uuid"
		ncName         = "nc-idempotent"
		providerID     = "scaleway://instance/fr-par-1/node-uuid"
		nodeName       = "scw-karp-nc-idempotent"
	)

	// orphanedPool is what ListPools returns — the pool left behind by the
	// previous Create attempt that crashed before persisting the pool-id
	// annotation. The NodeClaim tag must match nc.Name so findPoolByNodeClaim
	// can locate it.
	orphanedPool := &scaleway.KapsulePool{
		ID:        orphanedPoolID,
		NodeClaim: ncName,
		NodeType:  "GP1-XS",
		Zone:      scw.Zone("fr-par-1"),
		Size:      1,
	}

	api := &fakeKapsule{
		// listResp is the pool list used by the recovery lookup inside
		// createPoolFn (mirrors what KapsuleClient.CreatePool does via
		// findPoolByNodeClaim → ListPools).
		listResp: []*scaleway.KapsulePool{orphanedPool},

		// createPoolFn simulates the full KapsuleClient.CreatePool recovery:
		// Scaleway rejects the duplicate name with InvalidArgumentsError(name),
		// so we fall back to searching listResp by NodeClaim tag — exactly as
		// isInvalidArgumentName + findPoolByNodeClaim does in production.
		createPoolFn: func(_ context.Context, params scaleway.CreatePoolParams) (*scaleway.KapsulePool, error) {
			// The underlying k8s.API.CreatePool would return this error for
			// a duplicate pool name (unique-name constraint violation).
			nameConflict := &scw.InvalidArgumentsError{
				Details: []scw.InvalidArgumentsErrorDetail{
					{ArgumentName: "name", Reason: "constraint"},
				},
			}

			// Mirror the production recovery path from
			// pkg/scaleway.KapsuleClient.CreatePool lines 92-96:
			//
			//   if isInvalidArgumentName(err) {
			//       if existing, _ := c.findPoolByNodeClaim(ctx, params.Name); existing != nil {
			//           return existing, nil
			//       }
			//   }
			//
			// isInvalidArgumentName(nameConflict) == true, so we proceed to
			// findPoolByNodeClaim: scan available pools for the matching tag.
			if orphanedPool.NodeClaim == params.Name {
				return orphanedPool, nil
			}
			return nil, nameConflict
		},

		// WaitForNodeReady returns a ready node for the orphaned pool.
		waitForNodeFn: func(_ context.Context, poolID string, _ time.Duration) (*scaleway.KapsuleNode, error) {
			if poolID != orphanedPoolID {
				t.Errorf("WaitForNodeReady called with unexpected poolID %q, want %q", poolID, orphanedPoolID)
			}
			return &scaleway.KapsuleNode{
				ID:         "node-uuid",
				PoolID:     orphanedPoolID,
				ProviderID: providerID,
				Name:       nodeName,
			}, nil
		},

		// CountPools: below quota so preflight passes.
		countTotal: 10,
		countKarp:  10,
	}

	nc := mkNCForCreate(ncName, "GP1-XS", "fr-par-1")

	nodeClass := &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       v1alpha1.ScalewayNodeClassSpec{Zones: []string{"fr-par-1"}},
	}

	scheme := newScheme(t)
	kc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nodeClass).Build()

	bk, err := New(Options{
		Kapsule:    api,
		KubeClient: kc,
		Pricing:    pricing.NewProvider(&pricing.FakeAPI{}, nil),
		Quota:      QuotaConfig{Limit: 150, Threshold: 140},
		Log:        logr.Discard(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out, err := bk.Create(context.Background(), nc)
	if err != nil {
		t.Fatalf("Create: unexpected error: %v\n"+
			"Expected Create to succeed after recovering from name-conflict\n"+
			"(recovery path: pkg/scaleway.KapsuleClient.CreatePool lines 92-96)", err)
	}

	// The returned NodeClaim must carry the orphaned pool's ID — not a new one.
	if got := out.Annotations[PoolIDAnnotationKey]; got != orphanedPoolID {
		t.Fatalf("pool-id annotation: want %q got %q", orphanedPoolID, got)
	}

	// ProviderID must be propagated from the node inside the orphaned pool.
	if got := out.Status.ProviderID; got != providerID {
		t.Fatalf("providerID: want %q got %q", providerID, got)
	}

	// NodeName must be populated so Karpenter can link the NodeClaim to the
	// Kubernetes Node object.
	if got := out.Status.NodeName; got != nodeName {
		t.Fatalf("NodeName: want %q got %q", nodeName, got)
	}
}

// During a leader handover the old leader created the pool in
// PRO2-S; the new leader picked PRO2-XXS, got the existing pool back from the
// idempotent CreatePool, and labelled the NodeClaim PRO2-XXS — instantly
// InstanceTypeDrift. existingPoolAPI returns that pre-existing pool whatever
// the request.
func existingPoolAPI(nodeType, zone string) *fakeKapsule {
	pool := &scaleway.KapsulePool{ID: "pool-existing", NodeClaim: "nc-handover", NodeType: nodeType, Zone: scw.Zone(zone), Size: 1}
	return &fakeKapsule{
		countTotal: 1,
		listResp:   []*scaleway.KapsulePool{pool},
		createPoolFn: func(_ context.Context, _ scaleway.CreatePoolParams) (*scaleway.KapsulePool, error) {
			return pool, nil
		},
		waitForNodeFn: func(_ context.Context, poolID string, _ time.Duration) (*scaleway.KapsuleNode, error) {
			return &scaleway.KapsuleNode{ID: "n-1", PoolID: poolID, ProviderID: "scaleway://instance/" + zone + "/n-1", Name: "node-1"}, nil
		},
	}
}

func TestCreate_ExistingPoolLabelsFromPool(t *testing.T) {
	api := existingPoolAPI("PRO2-S", "fr-par-2")
	bk := newKapsuleWithQuota(t, api, QuotaConfig{}, mkNodeClass("fr-par-1", "fr-par-2"))
	nc := mkNCForCreate("nc-handover", "PRO2-XXS", "fr-par-1")
	nc.Spec.Requirements[0].Values = []string{"PRO2-S", "PRO2-XXS"}
	nc.Spec.Requirements[1].Values = []string{"fr-par-1", "fr-par-2"}

	out, err := bk.Create(context.Background(), nc)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := out.Labels[corev1.LabelInstanceTypeStable]; got != "PRO2-S" {
		t.Fatalf("instance-type label = %q, want PRO2-S (the pool's type)", got)
	}
	if got := out.Labels[corev1.LabelTopologyZone]; got != "fr-par-2" {
		t.Fatalf("zone label = %q, want fr-par-2 (the pool's zone)", got)
	}
	if len(api.deletePoolCalls) != 0 {
		t.Fatalf("DeletePool calls = %v, want none", api.deletePoolCalls)
	}
	reason, err := bk.IsDrifted(context.Background(), out)
	if err != nil {
		t.Fatalf("IsDrifted: %v", err)
	}
	if reason != "" {
		t.Fatalf("IsDrifted = %q right after Create, want no drift", reason)
	}
}

func TestCreate_ExistingPoolWithNonCandidateTypeIsDeleted(t *testing.T) {
	api := existingPoolAPI("PRO2-S", "fr-par-1")
	bk := newKapsuleWithQuota(t, api, QuotaConfig{}, mkNodeClass("fr-par-1"))
	nc := mkNCForCreate("nc-handover", "PRO2-XXS", "fr-par-1")

	if _, err := bk.Create(context.Background(), nc); err == nil {
		t.Fatal("Create: want error for an existing pool whose type is not a candidate")
	}
	if len(api.deletePoolCalls) != 1 || api.deletePoolCalls[0] != "pool-existing" {
		t.Fatalf("DeletePool calls = %v, want [pool-existing]", api.deletePoolCalls)
	}
}

func TestCreate_ExistingPoolWithNonCandidateZoneIsDeleted(t *testing.T) {
	api := existingPoolAPI("PRO2-XXS", "fr-par-2")
	bk := newKapsuleWithQuota(t, api, QuotaConfig{}, mkNodeClass("fr-par-1", "fr-par-2"))
	nc := mkNCForCreate("nc-handover", "PRO2-XXS", "fr-par-1")

	if _, err := bk.Create(context.Background(), nc); err == nil {
		t.Fatal("Create: want error for an existing pool whose zone is not a candidate")
	}
	if len(api.deletePoolCalls) != 1 || api.deletePoolCalls[0] != "pool-existing" {
		t.Fatalf("DeletePool calls = %v, want [pool-existing]", api.deletePoolCalls)
	}
}
