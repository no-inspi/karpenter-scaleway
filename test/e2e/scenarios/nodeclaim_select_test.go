package scenarios

import (
	"testing"

	opstatus "github.com/awslabs/operatorpkg/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// nc builds a NodeClaim with the given name, Ready condition and NodeName,
// for table-driven selection tests.
func nc(name string, ready bool, nodeName string) karpv1.NodeClaim {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	return karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: karpv1.NodeClaimStatus{
			NodeName: nodeName,
			Conditions: []opstatus.Condition{
				{Type: opstatus.ConditionReady, Status: status},
			},
		},
	}
}

// TestFirstReadyNodeClaim_SkipsUnreadyItemZero is the regression test for the
// kapsule-Cordon failure: the first item in the list is NOT ready and
// has an empty NodeName, while a later item is ready. firstReadyNodeClaim must
// return the ready one, never items[0].
func TestFirstReadyNodeClaim_SkipsUnreadyItemZero(t *testing.T) {
	items := []karpv1.NodeClaim{
		nc("unready-first", false, ""), // the trap: taking items[0] fails
		nc("ready-second", true, "node-abc"),
	}

	got := firstReadyNodeClaim(items)

	if got == nil {
		t.Fatalf("expected the ready NodeClaim, got nil")
	}
	if got.Name != "ready-second" {
		t.Fatalf("expected ready-second, got %s", got.Name)
	}
	if got.Status.NodeName != "node-abc" {
		t.Fatalf("expected NodeName node-abc, got %q", got.Status.NodeName)
	}
}

// TestFirstReadyNodeClaim_NoneReady returns nil when no NodeClaim is ready.
func TestFirstReadyNodeClaim_NoneReady(t *testing.T) {
	items := []karpv1.NodeClaim{
		nc("a", false, ""),
		nc("b", false, ""),
	}

	if got := firstReadyNodeClaim(items); got != nil {
		t.Fatalf("expected nil when none ready, got %s", got.Name)
	}
}

// TestFirstReadyNodeClaim_ReturnsFirstReadyInOrder returns the first ready one
// when several are ready.
func TestFirstReadyNodeClaim_ReturnsFirstReadyInOrder(t *testing.T) {
	items := []karpv1.NodeClaim{
		nc("first-ready", true, "node-1"),
		nc("second-ready", true, "node-2"),
	}

	got := firstReadyNodeClaim(items)
	if got == nil || got.Name != "first-ready" {
		t.Fatalf("expected first-ready, got %v", got)
	}
}
