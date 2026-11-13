//go:build e2ecloud
// +build e2ecloud

package scenarios

import (
	"context"
	"testing"
	"time"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/no-inspi/karpenter-scaleway/test/e2e/harness"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestE2E_Cordon asserts that Karpenter does not schedule new Pods onto a
// cordoned node, but instead provisions a fresh NodeClaim.
//
// Flow:
//  1. Create 1 Pending Pod, wait for a NodeClaim to become Ready=True.
//  2. Look up the backing Node via NodeClaim.Status.NodeName.
//  3. Cordon the Node (Spec.Unschedulable=true).
//  4. Create a second Pending Pod with the same nodepool selector.
//  5. Poll ≤ 10 min: a NEW NodeClaim (different name from the first)
//     exists AND is Ready=True. The cordoned Node must not be reused.
func TestE2E_Cordon(t *testing.T) {
	for _, b := range harness.SelectedBackends() {
		t.Run(string(b), func(t *testing.T) {
			cluster, _, nodePool := setupScenarioBase(t, b, "cordon")
			ctx := context.Background()

			if err := cluster.KubeClient.Create(ctx, nodePool); err != nil {
				t.Fatalf("create NodePool: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), nodePool) })

			// Step 1: create first Pod and wait for NodeClaim Ready=True.
			selector := map[string]string{"karpenter.sh/nodepool": nodePool.Name}
			pod1, err := harness.PodPending(ctx, cluster, nodePool.Name+"-pod-1", selector)
			if err != nil {
				t.Fatalf("create Pod 1: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), pod1) })

			// Step 2: use the Ready NodeClaim returned by the waiter and its
			// backing node. We must NOT re-list and take items[0]: List() order
			// is not guaranteed and multiple NodeClaims can coexist, so items[0]
			// may be a not-yet-Ready claim with an empty NodeName.
			firstNC := waitForOneReadyNodeClaim(t, cluster, nodePool.Name, 10*time.Minute)
			firstNCName := firstNC.Name
			nodeName := firstNC.Status.NodeName
			if nodeName == "" {
				cluster.DumpDiagnostics(t)
				t.Fatalf("Ready NodeClaim %s has empty Status.NodeName; node may not have joined yet", firstNCName)
			}

			// Step 3: cordon the backing Node.
			_, err = cluster.Typed.CoreV1().Nodes().Patch(
				ctx,
				nodeName,
				types.MergePatchType,
				[]byte(`{"spec":{"unschedulable":true}}`),
				metav1.PatchOptions{},
			)
			if err != nil {
				t.Fatalf("cordon node %s: %v", nodeName, err)
			}
			// Uncordon during cleanup to avoid interfering with terraform destroy.
			t.Cleanup(func() {
				_, _ = cluster.Typed.CoreV1().Nodes().Patch(
					context.Background(),
					nodeName,
					types.MergePatchType,
					[]byte(`{"spec":{"unschedulable":false}}`),
					metav1.PatchOptions{},
				)
			})

			// Step 4: create a second Pending Pod.
			pod2, err := harness.PodPending(ctx, cluster, nodePool.Name+"-pod-2", selector)
			if err != nil {
				t.Fatalf("create Pod 2: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), pod2) })

			// Step 5: poll ≤ 10 min for a NEW NodeClaim (different name from the
			// first) that is Ready=True. The cordoned node must not be reused.
			deadline := time.Now().Add(10 * time.Minute)
			for time.Now().Before(deadline) {
				ncList2 := &karpv1.NodeClaimList{}
				if err := cluster.KubeClient.List(ctx, ncList2,
					client.MatchingLabels{"karpenter.sh/nodepool": nodePool.Name}); err != nil {
					t.Logf("list NodeClaims: %v", err)
					time.Sleep(15 * time.Second)
					continue
				}
				for i := range ncList2.Items {
					nc := &ncList2.Items[i]
					if nc.Name != firstNCName && isNodeClaimReady(nc) {
						// Verify the new NodeClaim does NOT land on the cordoned node.
						if nc.Status.NodeName != "" && nc.Status.NodeName == nodeName {
							t.Fatalf("new NodeClaim %s landed on cordoned node %s", nc.Name, nodeName)
						}
						// Verify the cordoned node is actually unschedulable.
						node, err := cluster.Typed.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
						if err != nil {
							t.Logf("get cordoned node %s: %v", nodeName, err)
						} else if !node.Spec.Unschedulable {
							t.Logf("warning: cordoned node %s is no longer unschedulable", nodeName)
						}
						return // green: new NodeClaim provisioned on a different node
					}
				}
				time.Sleep(15 * time.Second)
			}
			cluster.DumpDiagnostics(t)
			t.Fatalf("no new NodeClaim reached Ready=True within 10m for backend %s (cordoned node: %s)", b, nodeName)
		})
	}
}
