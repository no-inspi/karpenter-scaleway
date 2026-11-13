# expires_at must be ≥ now; we anchor it to apply-time +6h to give the
# nightly job comfortable headroom (a secret token without
# expires_at is rejected at create time).
resource "time_offset" "key_expiry" {
  offset_hours = 6
}

resource "scaleway_iam_application" "controller" {
  name        = "karpenter-e2e-${var.run_id}-controller"
  description = "E2E controller credentials for run ${var.run_id}"
}

resource "scaleway_iam_policy" "controller" {
  name           = "karpenter-e2e-${var.run_id}-controller"
  application_id = scaleway_iam_application.controller.id

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

resource "scaleway_iam_api_key" "controller" {
  application_id     = scaleway_iam_application.controller.id
  description        = "E2E controller key for run ${var.run_id}"
  default_project_id = var.project_id
  expires_at         = time_offset.key_expiry.rfc3339
}
