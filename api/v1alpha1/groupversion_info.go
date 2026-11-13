// +groupName=karpenter.scaleway.com
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
)

const Group = "karpenter.scaleway.com"

// GroupVersion is the group/version used to register the v1alpha1 types.
var GroupVersion = schema.GroupVersion{Group: Group, Version: "v1alpha1"}

// AddToScheme registers the v1alpha1 types with the supplied runtime.Scheme.
// Used by pkg/scheme to build the centralized scheme passed to the manager.
func AddToScheme(s *runtime.Scheme) error {
	metav1.AddToGroupVersion(s, GroupVersion)
	s.AddKnownTypes(GroupVersion,
		&ScalewayNodeClass{},
		&ScalewayNodeClassList{},
	)
	return nil
}

// init registers the types on the global client-go scheme so binaries that
// rely on a side-effect import (`_ "…/api/v1alpha1"`) automatically pick
// them up — including the karpenter operator framework, which builds the
// manager scheme from the global one.
func init() {
	if err := AddToScheme(scheme.Scheme); err != nil {
		panic(err)
	}
}
