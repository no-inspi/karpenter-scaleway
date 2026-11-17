package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScalewayNodeClassSpec defines the configuration for instances provisioned
// by Karpenter on a Scaleway-managed cluster (Kapsule or Kosmos).
//
// All fields with the kosmosOnly comment are silently ignored when the
// resolved backend is Kapsule; the reconciler surfaces them in
// status.unusedFieldsForBackend so configuration mistakes are visible.
type ScalewayNodeClassSpec struct {
	// Zones is the optional whitelist of Scaleway zones where instances from
	// this NodeClass may be provisioned. Empty or omitted means all 9 zones
	// are eligible; the actual zone is chosen by the Karpenter scheduler
	// from instanceTypeOfferings.
	// +kubebuilder:validation:Enum=fr-par-1;fr-par-2;fr-par-3;nl-ams-1;nl-ams-2;nl-ams-3;pl-waw-1;pl-waw-2;pl-waw-3
	// +kubebuilder:validation:MaxItems=9
	// +optional
	Zones []string `json:"zones,omitempty"`

	// ImageLabel is the Scaleway Marketplace image label or UUID.
	// Must reference a cloud-init enabled image. Validated at reconcile time
	// via a Marketplace API round-trip; failures surface on the Ready
	// condition with reason ImageNotFound.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	ImageLabel string `json:"imageLabel"`

	// Tags are propagated to the underlying Scaleway resource (Instance for
	// Kosmos, Pool for Kapsule). At most 50 entries (Scaleway API limit).
	// +kubebuilder:validation:MaxProperties=50
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// RootVolumeSizeGB is the size of the root volume in gigabytes. Applied
	// to Kapsule pools and Kosmos instances. It also sets the
	// ephemeral-storage capacity Karpenter schedules against (90% of the
	// volume); unset means the Kapsule default of 20 GB. Changing it marks
	// existing Kapsule NodeClaims as drifted (RootVolumeSizeDrift).
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=10000
	// +optional
	RootVolumeSizeGB *int32 `json:"rootVolumeSizeGB,omitempty"`

	// RootVolumeType is the Scaleway volume type for the root volume.
	// kosmosOnly: silently ignored on Kapsule pools.
	// +kubebuilder:validation:Enum=l_ssd;sbs_volume
	// +optional
	RootVolumeType string `json:"rootVolumeType,omitempty"`

	// PublicIP controls whether instances get a public IPv4 address.
	// kosmosOnly: silently ignored on Kapsule pools. Required (true) for
	// the k8s-agent registration to download the agent binary from Scaleway.
	// +optional
	PublicIP *bool `json:"publicIP,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=scwnc
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Zones",type=string,JSONPath=`.status.resolvedZones`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.imageLabel`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
//
// ScalewayNodeClass is the Schema for the ScalewayNodeClass API.
type ScalewayNodeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScalewayNodeClassSpec   `json:"spec,omitempty"`
	Status ScalewayNodeClassStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// ScalewayNodeClassList contains a list of ScalewayNodeClass.
type ScalewayNodeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ScalewayNodeClass `json:"items"`
}
