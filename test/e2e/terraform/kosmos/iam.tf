resource "time_offset" "key_expiry" {
  offset_hours = 6
}

# Controller-side credentials (Karpenter pod).
resource "scaleway_iam_application" "controller" {
  name        = "karpenter-e2e-${var.run_id}-controller"
  description = "E2E controller credentials for run ${var.run_id}"
}

resource "scaleway_iam_policy" "controller" {
  name           = "karpenter-e2e-${var.run_id}-controller"
  application_id = scaleway_iam_application.controller.id

  rule {
    project_ids = [var.project_id]
    # KubernetesFullAccess is over-privileged for the Kosmos worker join
    # path (KubernetesExternalNodeRegister is enough), but the controller
    # also calls GetCluster at boot for backend auto-detection. Whether
    # KubernetesExternalNodeRegister includes that read is undocumented;
    # adding KubernetesFullAccess removes the risk for v0.1.0. Audit and
    # tighten in a follow-up alongside the VPCFullAccess audit.
    permission_set_names = [
      "KubernetesFullAccess",
      "KubernetesExternalNodeRegister",
      "InstancesFullAccess",
      "IPAMFullAccess",
      "VPCFullAccess",
    ]
  }
}

resource "scaleway_iam_api_key" "controller" {
  application_id     = scaleway_iam_application.controller.id
  description        = "E2E controller key for run ${var.run_id}"
  default_project_id = var.project_id
  expires_at         = time_offset.key_expiry.rfc3339
}

# Pool-side credentials (workers join the cluster). Same application is
# fine — minimum-privilege split is post-v0.1.0.
resource "scaleway_iam_api_key" "kosmos_pool" {
  application_id     = scaleway_iam_application.controller.id
  description        = "E2E Kosmos pool token for run ${var.run_id}"
  default_project_id = var.project_id
  expires_at         = time_offset.key_expiry.rfc3339
}
