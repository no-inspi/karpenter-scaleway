// Package-level NodeClaim selection helpers, intentionally WITHOUT the
// e2ecloud build tag so they compile — and unit-test — without a real
// cluster. The scenario files (*_test.go) are e2ecloud-tagged and reuse
// these in the full E2E build.
package scenarios

import (
	opstatus "github.com/awslabs/operatorpkg/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// isNodeClaimReady reports whether the NodeClaim's Ready condition is True.
func isNodeClaimReady(nc *karpv1.NodeClaim) bool {
	for _, c := range nc.Status.Conditions {
		if c.Type == opstatus.ConditionReady && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// firstReadyNodeClaim returns a pointer to the first NodeClaim in items whose
// Ready condition is True, or nil if none is ready.
//
// Callers MUST use this instead of items[0]: List() order is not guaranteed,
// and more than one NodeClaim can be present (e.g. a failed launch being
// replaced). Blindly taking items[0] can pick a not-yet-Ready NodeClaim whose
// Status.NodeName is still empty — the root cause of a past kapsule-Cordon
// failure.
func firstReadyNodeClaim(items []karpv1.NodeClaim) *karpv1.NodeClaim {
	for i := range items {
		if isNodeClaimReady(&items[i]) {
			return &items[i]
		}
	}
	return nil
}
