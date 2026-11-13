//go:build e2ecloud
// +build e2ecloud

package scenarios

import (
	"context"
	"testing"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/test/e2e/harness"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// setupScenarioBase builds a ScalewayNodeClass and NodePool for a single
// scenario run. It registers a t.Cleanup handler to delete the NodeClass
// after the test; the caller is responsible for the NodePool's t.Cleanup
// so it can register cleanup AFTER mutating Disruption / ExpireAfter and
// calling Create.
//
// The caller is responsible for creating Pods and asserting scenario-
// specific conditions. The NodePool is returned unmodified — callers that
// need Disruption overrides (e.g. Consolidation, Expire) should set
// nodePool.Spec.Disruption BEFORE calling cluster.KubeClient.Create.
//
// Usage:
//
//	cluster, nodeClass, nodePool := setupScenarioBase(t, b, "consolidation")
//	nodePool.Spec.Disruption = karpv1.Disruption{...}
//	if err := cluster.KubeClient.Create(ctx, nodePool); ...
func setupScenarioBase(t *testing.T, b harness.Backend, scenario string) (
	*harness.Cluster,
	*v1alpha1.ScalewayNodeClass,
	*karpv1.NodePool,
) {
	t.Helper()
	cluster := harness.SetupCluster(t, b)
	ctx := context.Background()

	nodeClass := &v1alpha1.ScalewayNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-" + scenario + "-" + cluster.RunID},
		Spec: v1alpha1.ScalewayNodeClassSpec{
			Zones:      []string{cluster.Region + "-1"},
			ImageLabel: "ubuntu_jammy",
		},
	}
	if err := cluster.KubeClient.Create(ctx, nodeClass); err != nil {
		t.Fatalf("create NodeClass: %v", err)
	}
	t.Cleanup(func() { _ = cluster.KubeClient.Delete(context.Background(), nodeClass) })

	nodePool := newNodePool("e2e-"+scenario+"-"+cluster.RunID, nodeClass.Name)

	return cluster, nodeClass, nodePool
}
