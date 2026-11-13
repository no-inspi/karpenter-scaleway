resource "scaleway_iam_application" "karpenter_spike" {
  name        = "karpenter-spike-controller"
  description = "Karpenter Scaleway dev cluster"
}

resource "scaleway_iam_policy" "node_register" {
  name           = "karpenter-spike-node-register"
  application_id = scaleway_iam_application.karpenter_spike.id

  rule {
    project_ids = [var.project_id]
    permission_set_names = [
      "KubernetesFullAccess",
      "InstancesFullAccess",
      "IPAMFullAccess",
      "VPCFullAccess",
    ]
  }
}

resource "scaleway_iam_api_key" "karpenter_spike" {
  application_id     = scaleway_iam_application.karpenter_spike.id
  description        = "Karpenter Scaleway dev cluster"
  default_project_id = var.project_id
  expires_at         = "2026-12-31T23:59:59Z"
}
