package harness

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodPending creates a Pod with a node selector that no system pool
// satisfies, forcing Karpenter to provision a NodeClaim to satisfy
// scheduling. Returns the created Pod.
//
// The caller MUST register cleanup via t.Cleanup so the Pod is
// removed before terraform destroy. Failure to clean up will not
// prevent cluster teardown (terraform destroy reaps everything),
// but a leaked Pod can confuse Karpenter consolidation heuristics
// during the same scenario:
//
//	pod, err := harness.PodPending(ctx, cluster, "scenario-pod", selector)
//	if err != nil { t.Fatal(err) }
//	t.Cleanup(func() { _ = cluster.KubeClient.Delete(ctx, pod) })
func PodPending(ctx context.Context, c *Cluster, name string, nodeSelector map[string]string) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: c.Namespace,
			Labels:    map[string]string{"karpenter-e2e-run": c.RunID},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeSelector:  nodeSelector,
			Containers: []corev1.Container{{
				Name:  "pause",
				Image: "registry.k8s.io/pause:3.9",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			}},
		},
	}
	if err := c.KubeClient.Create(ctx, pod); err != nil {
		return nil, err
	}
	return pod, nil
}
