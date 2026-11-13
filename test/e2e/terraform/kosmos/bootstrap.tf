# Bootstrap external node.
#
# A mutualized multicloud control plane refuses a cluster with zero nodes,
# and the Karpenter controller (installed in-cluster by the E2E harness via
# `helm --install --wait`) has nowhere to run until at least one node is
# Ready — a chicken-and-egg problem. This pre-provisions exactly one
# Scaleway Instance that registers itself as an external node of the seed
# pool, satisfying both constraints. Karpenter provisions the actual test
# workers on top of it during the scenario.

locals {
  # k8s-agent expects the bare pool UUID, not the region-qualified id.
  seed_pool_id = element(
    split("/", scaleway_k8s_pool.seed.id),
    length(split("/", scaleway_k8s_pool.seed.id)) - 1,
  )
}

resource "scaleway_instance_server" "bootstrap" {
  name              = "karpenter-e2e-${var.run_id}-bootstrap"
  type              = var.bootstrap_node_type
  image             = "ubuntu_jammy"
  zone              = var.kosmos_pool_zone
  enable_dynamic_ip = true # public IPv4 — k8s-agent registration needs egress

  user_data = {
    cloud-init = templatefile("${path.module}/bootstrap-cloud-init.sh.tftpl", {
      k8s_agent_url = var.k8s_agent_url
      pool_id       = local.seed_pool_id
      pool_region   = var.region
      # Same Kosmos IAM key the chart + Karpenter workers use to register.
      api_key = scaleway_iam_api_key.kosmos_pool.secret_key
    })
  }

  tags = ["karpenter-e2e=true", "run-id=${var.run_id}", "purpose=kosmos-bootstrap"]
}
