resource "scaleway_vpc_private_network" "e2e" {
  name   = "karpenter-e2e-${var.run_id}-pn"
  region = var.region
  tags   = ["karpenter-e2e=true", "run-id=${var.run_id}"]
}

resource "scaleway_k8s_cluster" "e2e" {
  name                        = "karpenter-e2e-${var.run_id}"
  type                        = "kapsule"
  version                     = var.k8s_version
  cni                         = "cilium"
  region                      = var.region
  private_network_id          = scaleway_vpc_private_network.e2e.id
  delete_additional_resources = true
  description                 = "E2E nightly run ${var.run_id}"

  # Tag is load-bearing for the cleanup-zombies workflow — DO NOT remove.
  tags = ["karpenter-e2e=true", "run-id=${var.run_id}", "backend=kapsule"]
}

resource "scaleway_k8s_pool" "system" {
  cluster_id          = scaleway_k8s_cluster.e2e.id
  name                = "system"
  node_type           = var.system_node_type
  size                = 1
  min_size            = 1
  max_size            = 1
  autoscaling         = false
  region              = var.region
  wait_for_pool_ready = true

  tags = ["karpenter-e2e=true", "run-id=${var.run_id}", "purpose=system"]
}
