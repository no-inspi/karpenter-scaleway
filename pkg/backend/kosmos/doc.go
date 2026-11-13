// Package kosmos implements backend.Backend on top of Scaleway Kosmos
// (multicloud) clusters, where Karpenter provisions raw Scaleway Instances
// that join the cluster via the Scaleway k8s-agent installer.
//
// The providerID assigned by the Kosmos control plane is opaque
// (scaleway://external/unknown/<kosmos-uuid>) and unrelated to the underlying
// Instance UUID, so this implementation correlates Node ↔ Instance via the
// public IPv4 address. The Instance UUID is persisted on the NodeClaim as
// the annotation karpenter.scaleway.com/instance-id for fast Delete lookups.
package kosmos
