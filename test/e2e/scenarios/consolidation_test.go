//go:build e2ecloud
// +build e2ecloud

package scenarios

import (
	"context"
	"testing"
	"time"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	corev1 "k8s.io/api/core/v1"

	"github.com/no-inspi/karpenter-scaleway/test/e2e/harness"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestE2E_Consolidation asserts that Karpenter removes idle NodeClaims
// after all Pods they hosted are deleted, using the
// WhenEmptyOrUnderutilized consolidation policy.
//
// Flow:
//  1. Create 3 Pending Pods to force Karpenter to provision at least 1 NodeClaim.
//  2. Wait for at least 1 NodeClaim to reach Ready=True (≤ 10 min).
//  3. Delete all 3 Pods, leaving the node(s) empty.
//  4. Poll ≤ 5 min: assert that all NodeClaims for this NodePool are gone
//     (Karpenter consolidates and deletes the empty node(s)).
func TestE2E_Consolidation(t *testing.T) {
	for _, b := range harness.SelectedBackends() {
		t.Run(string(b), func(t *testing.T) {
			cluster, _, nodePool := setupScenarioBase(t, b, "consolidation")
			ctx := context.Background()

			// Override Disruption: enable WhenEmptyOrUnderutilized with a
			// short 30s consolidation window so the test does not run for long.
			nodePool.Spec.Disruption = karpv1.Disruption{
				ConsolidationPolicy: karpv1.ConsolidationPolicyWhenEmptyOrUnderutilized,
				ConsolidateAfter:    karpv1.MustParseNillableDuration("30s"),
			}
			if err := cluster.KubeClient.Create(ctx, nodePool); err != nil {
				t.Fatalf("create NodePool: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), nodePool) })

			// Create 3 Pending Pods to ensure Karpenter provisions at least
			// one NodeClaim. All share the same nodepool label.
			selector := map[string]string{"karpenter.sh/nodepool": nodePool.Name}
			podNames := []string{
				nodePool.Name + "-pod-1",
				nodePool.Name + "-pod-2",
				nodePool.Name + "-pod-3",
			}
			for _, name := range podNames {
				pod, err := harness.PodPending(ctx, cluster, name, selector)
				if err != nil {
					t.Fatalf("create Pod %s: %v", name, err)
				}
				func(p *corev1.Pod) {
					t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), p) })
				}(pod)
			}

			// Step 2: wait for at least 1 NodeClaim Ready=True.
			waitForOneReadyNodeClaim(t, cluster, nodePool.Name, 10*time.Minute)

			// Step 3: delete all pods, leaving the node(s) idle.
			for _, name := range podNames {
				pod := &corev1.Pod{}
				pod.Name = name
				pod.Namespace = cluster.Namespace
				if err := cluster.KubeClient.Delete(context.Background(), pod); err != nil {
					t.Logf("delete pod %s: %v", name, err)
				}
			}

			// Step 4: poll ≤ 8 min for all NodeClaims belonging to this
			// NodePool to be removed by Karpenter consolidation. We accept
			// either an empty list or a list where every remaining NodeClaim
			// is already terminating (DeletionTimestamp != nil): Karpenter
			// holds a finalizer during drain → detach → instance delete,
			// which can take 1-3 min on Scaleway, so a strict len==0 check
			// would race with the GC and time out prematurely.
			deadline := time.Now().Add(8 * time.Minute)
			for time.Now().Before(deadline) {
				ncList := &karpv1.NodeClaimList{}
				if err := cluster.KubeClient.List(ctx, ncList,
					client.MatchingLabels{"karpenter.sh/nodepool": nodePool.Name}); err != nil {
					t.Logf("list NodeClaims: %v", err)
				} else if allTerminatingOrEmpty(ncList.Items) {
					return // green
				}
				time.Sleep(15 * time.Second)
			}
			t.Fatalf("NodeClaims not consolidated within 8m for backend %s", b)
		})
	}
}

// allTerminatingOrEmpty returns true when every NodeClaim left in the
// list is already terminating (DeletionTimestamp set) or the list is
// empty. Karpenter holds a finalizer during drain → detach → instance
// delete, which can take 1-3 min on Scaleway, so a strict "list empty"
// check would race with the GC.
func allTerminatingOrEmpty(items []karpv1.NodeClaim) bool {
	for i := range items {
		if items[i].DeletionTimestamp == nil {
			return false
		}
	}
	return true
}
