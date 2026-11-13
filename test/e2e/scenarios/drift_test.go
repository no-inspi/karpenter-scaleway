//go:build e2ecloud
// +build e2ecloud

package scenarios

import (
	"context"
	"testing"
	"time"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/test/e2e/harness"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestE2E_Drift covers the IsDrifted flow: an existing NodeClaim
// must be marked Drifted=True after we mutate the referenced
// ScalewayNodeClass.imageLabel.
func TestE2E_Drift(t *testing.T) {
	// Kosmos-only: the Kapsule backend's IsDrifted (pkg/backend/drift/drift.go)
	// ignores Spec.ImageLabel because the Scaleway Kapsule control plane
	// chooses the worker image internally — mutating ImageLabel has no
	// observable drift signal there. Kosmos is the only backend that
	// snapshots ImageLabel on the NodeClaim and re-checks it on reconcile.
	// Kapsule drift coverage will move to a separate scenario that mutates
	// a field IsDrifted actually observes (instance-type, zone, pool size).
	for _, b := range []harness.Backend{harness.BackendKosmos} {
		t.Run(string(b), func(t *testing.T) {
			cluster := harness.SetupCluster(t, b)
			ctx := context.Background()

			nc := &v1alpha1.ScalewayNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-drift-" + cluster.RunID},
				Spec: v1alpha1.ScalewayNodeClassSpec{
					Zones:      []string{cluster.Region + "-1"},
					ImageLabel: "ubuntu_jammy",
				},
			}
			if err := cluster.KubeClient.Create(ctx, nc); err != nil {
				t.Fatalf("create NodeClass: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), nc) })

			pool := newNodePool("e2e-drift-"+cluster.RunID, nc.Name)
			if err := cluster.KubeClient.Create(ctx, pool); err != nil {
				t.Fatalf("create NodePool: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), pool) })

			pod, err := harness.PodPending(ctx, cluster, "e2e-drift-"+cluster.RunID,
				map[string]string{"karpenter.sh/nodepool": pool.Name})
			if err != nil {
				t.Fatalf("PodPending: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), pod) })

			// 1. Wait for the initial NodeClaim Ready=True (re-uses
			//    the scaleup poll pattern).
			waitForOneReadyNodeClaim(t, cluster, pool.Name, 10*time.Minute)

			// 2. Mutate the NodeClass — change the imageLabel to a
			//    different valid label. IsDrifted compares
			//    NodeClass status.resolvedImageID with the NodeClaim's
			//    snapshot, so this must result in Drifted=True.
			//    Fresh Get before Update avoids stale resourceVersion conflicts.
			fresh := &v1alpha1.ScalewayNodeClass{}
			if err := cluster.KubeClient.Get(ctx, types.NamespacedName{Name: nc.Name}, fresh); err != nil {
				t.Fatalf("get NodeClass: %v", err)
			}
			fresh.Spec.ImageLabel = "ubuntu_noble"
			if err := cluster.KubeClient.Update(ctx, fresh); err != nil {
				t.Fatalf("update NodeClass: %v", err)
			}

			// 3. Poll: at least one NodeClaim from this pool reports
			//    Drifted=True within 5 min.
			deadline := time.Now().Add(5 * time.Minute)
			for time.Now().Before(deadline) {
				ncList := &karpv1.NodeClaimList{}
				if err := cluster.KubeClient.List(ctx, ncList,
					client.MatchingLabels{"karpenter.sh/nodepool": pool.Name}); err == nil {
					for i := range ncList.Items {
						for _, cond := range ncList.Items[i].Status.Conditions {
							if cond.Type == karpv1.ConditionTypeDrifted && cond.Status == metav1.ConditionTrue {
								return // green
							}
						}
					}
				}
				time.Sleep(10 * time.Second)
			}
			t.Fatalf("no NodeClaim flipped Drifted=True after imageLabel change for backend %s", b)
		})
	}
}

// waitForOneReadyNodeClaim blocks until a NodeClaim in poolName reaches
// Ready=True and returns it. Callers must use the returned NodeClaim rather
// than re-listing and taking items[0]: List() order is not guaranteed and more
// than one NodeClaim can be present, so items[0] may be a not-yet-Ready claim
// with an empty Status.NodeName.
//
// It takes the whole Cluster (not just its client) so it can dump cluster
// diagnostics inline on timeout — the e2e.yml log-collection step runs after
// terraform-destroy and captures nothing.
func waitForOneReadyNodeClaim(t *testing.T, cluster *harness.Cluster, poolName string, timeout time.Duration) *karpv1.NodeClaim {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ncList := &karpv1.NodeClaimList{}
		if err := cluster.KubeClient.List(context.Background(), ncList,
			client.MatchingLabels{"karpenter.sh/nodepool": poolName}); err == nil {
			if nc := firstReadyNodeClaim(ncList.Items); nc != nil {
				return nc
			}
		}
		time.Sleep(15 * time.Second)
	}
	cluster.DumpDiagnostics(t)
	t.Fatalf("no NodeClaim reached Ready=True within %s", timeout)
	return nil // unreachable: t.Fatalf stops the goroutine
}
