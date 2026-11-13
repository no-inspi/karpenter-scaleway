// Package nodeclass hosts the controller-runtime reconciler for the
// karpenter.scaleway.com/v1alpha1 ScalewayNodeClass resource.
//
// The reconciler validates the spec against the Scaleway Marketplace API
// (round-tripping the imageLabel) and writes the Ready condition on the
// status subresource. It also flags fields that are silently ignored by the
// resolved backend (status.unusedFieldsForBackend).
package nodeclass
