// Package cloudprovider is a thin wrapper that adapts a backend.Backend to
// the sigs.k8s.io/karpenter/pkg/cloudprovider.CloudProvider interface.
//
// Karpenter's core reconcilers consume this wrapper directly; the actual
// Scaleway-specific logic lives in pkg/backend/{kapsule,kosmos}. The
// wrapper exists solely to add the few cloudprovider-specific surfaces
// (Name, GetSupportedNodeClasses, RepairPolicies) Karpenter expects but
// the Backend contract intentionally omits.
package cloudprovider

import (
	"context"

	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"
	"github.com/no-inspi/karpenter-scaleway/pkg/backend"

	"github.com/awslabs/operatorpkg/status"
	"github.com/go-logr/logr"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// ProviderName is the string Karpenter labels NodeClaims with via Name().
// Stable — callers grep on it.
const ProviderName = "scaleway"

// Wrapper is the karpcp.CloudProvider adapter. Constructed with a single
// resolved Backend; auto-detection happens earlier in the binary lifecycle
// (cmd/controller/main.go).
type Wrapper struct {
	bk  backend.Backend
	log logr.Logger
}

// New returns a Wrapper that delegates karpcp.CloudProvider methods to bk.
func New(bk backend.Backend, log logr.Logger) *Wrapper {
	return &Wrapper{bk: bk, log: log}
}

// Name returns the stable cloudprovider identifier.
func (w *Wrapper) Name() string { return ProviderName }

// GetSupportedNodeClasses tells Karpenter which kinds of NodeClass this
// provider understands. The first element is the default.
func (w *Wrapper) GetSupportedNodeClasses() []status.Object {
	return []status.Object{&v1alpha1.ScalewayNodeClass{}}
}

// RepairPolicies returns nil — auto-repair is not in scope.
func (w *Wrapper) RepairPolicies() []karpcp.RepairPolicy {
	return nil
}

// Create delegates provisioning to the backend.
func (w *Wrapper) Create(ctx context.Context, nc *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	return w.bk.Create(ctx, nc)
}

// Delete delegates termination to the backend.
func (w *Wrapper) Delete(ctx context.Context, nc *karpv1.NodeClaim) error {
	return w.bk.Delete(ctx, nc)
}

// Get delegates lookup to the backend.
func (w *Wrapper) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	return w.bk.Get(ctx, providerID)
}

// List delegates listing to the backend.
func (w *Wrapper) List(ctx context.Context) ([]*karpv1.NodeClaim, error) {
	return w.bk.List(ctx)
}

// GetInstanceTypes delegates the catalog query to the backend.
func (w *Wrapper) GetInstanceTypes(ctx context.Context, np *karpv1.NodePool) ([]*karpcp.InstanceType, error) {
	return w.bk.GetInstanceTypes(ctx, np)
}

// IsDrifted delegates drift detection to the backend.
func (w *Wrapper) IsDrifted(ctx context.Context, nc *karpv1.NodeClaim) (karpcp.DriftReason, error) {
	return w.bk.IsDrifted(ctx, nc)
}

// Compile-time assertion: Wrapper satisfies the Karpenter contract. If
// karpcp.CloudProvider drifts (e.g. v1.11), the breakage surfaces here.
var _ karpcp.CloudProvider = (*Wrapper)(nil)
