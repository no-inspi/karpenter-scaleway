// Package scheme provides a centralized runtime.Scheme builder that
// registers every API type the controller binary needs to (de)serialize.
//
// Using a dedicated scheme avoids relying on the global k8s.io/client-go
// scheme: each component receives the same explicitly-built Scheme, which
// makes the dependency graph auditable and unit tests deterministic.
package scheme

import (
	v1alpha1 "github.com/no-inspi/karpenter-scaleway/api/v1alpha1"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	karpapi "sigs.k8s.io/karpenter/pkg/apis"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

var karpenterGV = schema.GroupVersion{Group: karpapi.Group, Version: "v1"}

// New returns a Scheme populated with every type the controller manipulates.
// Panics if a registration fails — those errors are programmer bugs, not
// runtime conditions.
func New() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(corev1.AddToScheme(s))
	utilruntime.Must(coordinationv1.AddToScheme(s))
	addKarpenter(s)
	utilruntime.Must(v1alpha1.AddToScheme(s))
	return s
}

func addKarpenter(s *runtime.Scheme) {
	metav1.AddToGroupVersion(s, karpenterGV)
	s.AddKnownTypes(karpenterGV,
		&karpv1.NodePool{},
		&karpv1.NodePoolList{},
		&karpv1.NodeClaim{},
		&karpv1.NodeClaimList{},
	)
}
