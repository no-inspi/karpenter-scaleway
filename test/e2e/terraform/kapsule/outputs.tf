output "cluster_id" {
  description = "Bare UUID of the Kapsule cluster."
  value       = element(split("/", scaleway_k8s_cluster.e2e.id), length(split("/", scaleway_k8s_cluster.e2e.id)) - 1)
}

output "kubeconfig" {
  description = "Raw kubeconfig YAML for the Kapsule cluster."
  value       = scaleway_k8s_cluster.e2e.kubeconfig[0].config_file
  sensitive   = true
}

output "region" {
  description = "Scaleway region the cluster was provisioned in."
  value       = var.region
}

output "scw_access_key" {
  description = "Access key the Karpenter controller will use."
  value       = scaleway_iam_api_key.controller.access_key
}

output "scw_secret_key" {
  description = "Secret key the Karpenter controller will use."
  value       = scaleway_iam_api_key.controller.secret_key
  sensitive   = true
}

output "iam_application_id" {
  description = "ID of the IAM application — emitted so cleanup paths can delete it."
  value       = scaleway_iam_application.controller.id
}
