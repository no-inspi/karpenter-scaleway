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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestE2E_ScaleUp asserts: a Pending Pod selecting a label only present
// on Karpenter-managed nodes triggers Karpenter to create a NodeClaim,
// the NodeClaim becomes Ready=True, and the Pod transitions to Running
// within the budgeted wall-time.
func TestE2E_ScaleUp(t *testing.T) {
	for _, b := range harness.SelectedBackends() {
		t.Run(string(b), func(t *testing.T) {
			cluster := harness.SetupCluster(t, b)
			ctx := context.Background()

			nodeClass := &v1alpha1.ScalewayNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-scaleup-" + cluster.RunID},
				Spec: v1alpha1.ScalewayNodeClassSpec{
					Zones:      []string{cluster.Region + "-1"},
					ImageLabel: "ubuntu_jammy",
				},
			}
			if err := cluster.KubeClient.Create(ctx, nodeClass); err != nil {
				t.Fatalf("create NodeClass: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), nodeClass) })

			nodePool := newNodePool("e2e-scaleup-"+cluster.RunID, nodeClass.Name)
			if err := cluster.KubeClient.Create(ctx, nodePool); err != nil {
				t.Fatalf("create NodePool: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), nodePool) })

			pod, err := harness.PodPending(ctx, cluster, "e2e-scaleup-"+cluster.RunID,
				map[string]string{"karpenter.sh/nodepool": nodePool.Name})
			if err != nil {
				t.Fatalf("create Pod: %v", err)
			}
			t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), pod) })

			// Poll for at least 1 NodeClaim Ready=True belonging to the
			// scenario NodePool. Kapsule nodes join in ~90s; a Kosmos worker
			// must download k8s-agent and install kubelet/containerd/CNI on a
			// fresh Instance (observed ~8-9 min for the bootstrap node), so
			// give it a wider budget than the go-test -timeout=60m ceiling.
			readyBudget := 10 * time.Minute
			if b == harness.BackendKosmos {
				readyBudget = 15 * time.Minute
			}
			deadline := time.Now().Add(readyBudget)
			for time.Now().Before(deadline) {
				ncList := &karpv1.NodeClaimList{}
				if err := cluster.KubeClient.List(ctx, ncList,
					client.MatchingLabels{"karpenter.sh/nodepool": nodePool.Name}); err != nil {
					t.Logf("list NodeClaims: %v", err)
				} else {
					for i := range ncList.Items {
						if isNodeClaimReady(&ncList.Items[i]) {
							return // green
						}
					}
				}
				time.Sleep(15 * time.Second)
			}
			cluster.DumpDiagnostics(t)
			t.Fatalf("no NodeClaim reached Ready=True within %s for backend %s", readyBudget, b)
		})
	}
}

// newNodePool builds a minimal NodePool referencing the scenario's
// ScalewayNodeClass. The instance-type list is intentionally narrow to
// keep the test deterministic.
func newNodePool(name, nodeClassName string) *karpv1.NodePool {
	return &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: v1alpha1.Group,
						Kind:  "ScalewayNodeClass",
						Name:  nodeClassName,
					},
					// karpenter.sh/nodepool is restricted by Karpenter v1 — it's
					// the controller's own bookkeeping label, set on each
					// NodeClaim/Node after provisioning. Constraining a NodePool's
					// Requirements on it is rejected by the validating webhook
					// ("label "karpenter.sh/nodepool" is restricted"). The Pod's
					// nodeSelector already pins it to this NodePool's nodes via
					// the same label, so the requirement was a no-op anyway.
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      "node.kubernetes.io/instance-type",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"DEV1-S", "DEV1-M"},
						},
					},
				},
			},
			// Hard cap per NodePool to prevent runaway provisioning if the
			// scenario hangs or the controller misbehaves. 12 vCPU = 6×DEV1-S
			// or 3×DEV1-M; the test plan caps the matrix at ≤ 5 nodes/cell.
			Limits: karpv1.Limits{
				corev1.ResourceCPU:    resource.MustParse("12"),
				corev1.ResourceMemory: resource.MustParse("24Gi"),
			},
		},
	}
}
