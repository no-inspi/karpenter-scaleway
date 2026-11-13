resource "scaleway_k8s_cluster" "e2e" {
  name                        = "karpenter-e2e-${var.run_id}"
  type                        = "multicloud"
  version                     = var.k8s_version
  cni                         = "kilo"
  region                      = var.region
  delete_additional_resources = true
  description                 = "E2E nightly Kosmos run ${var.run_id}"

  tags = ["karpenter-e2e=true", "run-id=${var.run_id}", "backend=kosmos"]
}

# Seed external pool — the single registration target for every Kosmos
# node. The Karpenter controller boots Scaleway Instances that join as
# *external* nodes of this pool via the kosmos API key + this pool's id
# (read by the scenarios through kosmos_pool_id → chart scaleway.kosmos.poolID).
# The bootstrap node in bootstrap.tf registers into it too.
#
# size = 1 (NOT 0): this cluster uses the free *mutualized* control plane,
# which the Scaleway API refuses to create with zero nodes —
# "a mutualized cluster cannot have less than 1 node". size=1 declares the
# one bootstrap node we pre-provision; Karpenter then registers additional
# externals into the same pool at runtime (external pools don't cap actual
# registrations by the desired size). wait_for_pool_ready = false so
# `terraform apply` does not block on the bootstrap node's async
# registration (which depends on this pool's id — see bootstrap.tf).
resource "scaleway_k8s_pool" "seed" {
  cluster_id          = scaleway_k8s_cluster.e2e.id
  name                = "seed"
  node_type           = "external"
  size                = 1
  region              = var.region
  zone                = var.kosmos_pool_zone
  wait_for_pool_ready = false

  tags = ["karpenter-e2e=true", "run-id=${var.run_id}", "purpose=kosmos-seed"]
}
