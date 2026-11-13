//go:build e2ecloud
// +build e2ecloud

package scenarios

import (
	"context"
	"testing"
	"time"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/no-inspi/karpenter-scaleway/test/e2e/harness"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestE2E_Expire asserts that Karpenter replaces an expired NodeClaim.
//
// ExpireAfter is set on NodeClaimTemplateSpec (not on Disruption) — this
// is the actual location in karpenter/pkg/apis/v1 v1.10.0.
//
// Flow:
//  1. Build NodePool with Template.Spec.ExpireAfter = 3m.
//  2. Create 1 Pending Pod, wait for the initial NodeClaim to reach Ready=True.
//  3. Capture the original NodeClaim UID.
//  4. Poll ≤ 15 min: wait for a NodeClaim with a DIFFERENT UID that is
//     Ready=True to appear (proving the replacement actually launched).
//
// NOTE: 3m is intentionally short for a test but still non-trivial for a
// provisioning cycle. The 15m poll window covers the worst-case Kosmos
// sequence: 3m expire + 0.5m grace + 1m drain + ~7m provisioning ≈ 11.5m.
func TestE2E_Expire(t *testing.T) {
	for _, b := range harness.SelectedBackends() {
		t.Run(string(b), func(t *testing.T) {
			cluster, _, nodePool := setupScenarioBase(t, b, "expire")
			ctx := context.Background()

			// Set ExpireAfter on NodeClaimTemplateSpec. This is different from
			// ConsolidateAfter (which lives on NodePoolSpec.Disruption): Karpenter
			// puts node-lifetime TTL on the per-node template, not on the pool's
			// disruption policy.
			nodePool.Spec.Template.Spec.ExpireAfter = karpv1.MustParseNillableDuration("3m")

			if err := cluster.KubeClient.Create(ctx, nodePool); err != nil {
				t.Fatalf("create NodePool: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), nodePool) })

			// Create 1 Pending Pod to trigger provisioning.
			selector := map[string]string{"karpenter.sh/nodepool": nodePool.Name}
			pod, err := harness.PodPending(ctx, cluster, nodePool.Name+"-pod-1", selector)
			if err != nil {
				t.Fatalf("create Pod: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), pod) })

			// Step 2: wait for the initial NodeClaim to reach Ready=True.
			waitForOneReadyNodeClaim(t, cluster, nodePool.Name, 10*time.Minute)

			// Step 3: capture the original NodeClaim UID.
			ncList := &karpv1.NodeClaimList{}
			if err := cluster.KubeClient.List(ctx, ncList,
				client.MatchingLabels{"karpenter.sh/nodepool": nodePool.Name}); err != nil {
				t.Fatalf("list NodeClaims: %v", err)
			}
			if len(ncList.Items) == 0 {
				t.Fatalf("expected at least 1 NodeClaim after Ready, found 0")
			}
			originalUID := types.UID("")
			for i := range ncList.Items {
				if isNodeClaimReady(&ncList.Items[i]) {
					originalUID = ncList.Items[i].UID
					break
				}
			}
			if originalUID == "" {
				cluster.DumpDiagnostics(t)
				t.Fatalf("could not capture UID of the Ready NodeClaim")
			}
			t.Logf("original NodeClaim UID: %s", originalUID)

			// Step 4: poll ≤ 15 min for a replacement NodeClaim with a
			// different UID that has reached Ready=True. The worst-case
			// Kosmos sequence is 3m expire + 0.5m grace + 1m drain +
			// ~7m Kosmos provisioning ≈ 11.5m, so 15m gives safe headroom.
			// Requiring Ready=True (not just "different UID") proves the
			// replacement actually launched and is serving workloads.
			deadline := time.Now().Add(15 * time.Minute)
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
					if nc.UID != originalUID && isNodeClaimReady(nc) {
						// A ready NodeClaim with a different UID is present —
						// the original was expired and a healthy replacement launched.
						t.Logf("replacement NodeClaim %s (UID=%s) found after expire", nc.Name, nc.UID)
						return // green: replacement launched and ready
					}
				}
				time.Sleep(15 * time.Second)
			}
			cluster.DumpDiagnostics(t)
			t.Fatalf("no replacement NodeClaim (UID != %s) within 15m for backend %s", originalUID, b)
		})
	}
}
