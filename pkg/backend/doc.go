// Package backend defines the Backend interface that abstracts a Scaleway
// managed-cluster type (Kapsule or Kosmos) behind a single contract.
//
// The interface is consumed by pkg/cloudprovider, which is a thin wrapper
// implementing sigs.k8s.io/karpenter/pkg/cloudprovider.CloudProvider by
// delegating to a resolved Backend.
//
// Auto-detection (detect.go) inspects the Scaleway API at boot to pick the
// appropriate implementation. A --force-backend flag overrides detection.
package backend
