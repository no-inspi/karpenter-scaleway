// Package kapsule implements backend.Backend on top of Scaleway Kapsule
// managed-Kubernetes node pools.
//
// Mapping: 1 NodeClaim → 1 Kapsule pool of size=1. Scaleway provisions the
// instance and bootstraps kubelet automatically; no cloud-init or k8s-agent
// is required. ProviderID is read directly from the Scaleway K8s API.
package kapsule
