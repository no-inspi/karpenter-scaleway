output "cluster_id" {
  description = "Bare UUID of the multicloud (Kosmos) cluster."
  value       = element(split("/", scaleway_k8s_cluster.e2e.id), length(split("/", scaleway_k8s_cluster.e2e.id)) - 1)
}

output "kubeconfig" {
  description = "Raw kubeconfig YAML for the Kosmos cluster."
  value       = scaleway_k8s_cluster.e2e.kubeconfig[0].config_file
  sensitive   = true
}

output "region" {
  description = "Scaleway region the Kosmos cluster was provisioned in."
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

output "kosmos_pool_id" {
  description = "Bare UUID of the seed external pool — wired into chart values.scaleway.kosmos.poolID."
  value       = element(split("/", scaleway_k8s_pool.seed.id), length(split("/", scaleway_k8s_pool.seed.id)) - 1)
}

output "kosmos_pool_api_key" {
  description = "API key the worker downloads + uses to register with the cluster."
  value       = scaleway_iam_api_key.kosmos_pool.secret_key
  sensitive   = true
}

output "iam_application_id" {
  description = "ID of the IAM application — emitted so cleanup paths can delete it."
  value       = scaleway_iam_application.controller.id
}

output "bootstrap_node_id" {
  description = "Zoned id of the bootstrap Instance — emitted for failure-time debugging + cleanup."
  value       = scaleway_instance_server.bootstrap.id
}
