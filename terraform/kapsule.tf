# Kapsule cluster (type="kapsule", cni="cilium").
#
# Usage: comment out kosmos.tf (or use a separate workspace) before applying.
# The two files share the same resource names (scaleway_k8s_cluster.spike,
# scaleway_k8s_pool.external) so only one can be active at a time.
#
# IAM note: the karpenter_spike application needs KubernetesFullAccess instead
# of KubernetesExternalNodeRegister (Kosmos-specific).  Update iam.tf before
# applying:
#   permission_set_names = ["KubernetesFullAccess"]
#
# Outputs reuse the same names as kosmos.tf (cluster_id, kubeconfig) so the
# measure.sh script and controller env vars don't need updating.

resource "scaleway_vpc_private_network" "spike" {
  name   = "karpenter-spike-pn"
  region = var.region
  tags   = ["spike", "karpenter", "delete-me"]
}

resource "scaleway_k8s_cluster" "spike" {
  name                        = "karpenter-spike-kapsule"
  type                        = "kapsule"
  version                     = var.k8s_version
  cni                         = "cilium"
  region                      = var.region
  private_network_id          = scaleway_vpc_private_network.spike.id
  delete_additional_resources = true
  description                 = "Karpenter Scaleway dev cluster"

  tags = ["spike", "karpenter", "delete-me"]
}

# Minimal system pool: provides a node for kube-system pods (coredns, etc.)
# while Karpenter-managed pools handle workload nodes.
resource "scaleway_k8s_pool" "system" {
  cluster_id          = scaleway_k8s_cluster.spike.id
  name                = "system"
  node_type           = "DEV1-M"
  size                = 1
  min_size            = 1
  max_size            = 1
  autoscaling         = false
  region              = var.region
  wait_for_pool_ready = true

  tags = ["spike", "system"]
}
