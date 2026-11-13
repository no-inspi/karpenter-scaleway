output "kubeconfig" {
  value     = scaleway_k8s_cluster.spike.kubeconfig[0].config_file
  sensitive = true
}

output "scw_access_key" {
  value = scaleway_iam_api_key.karpenter_spike.access_key
}

output "scw_secret_key" {
  value     = scaleway_iam_api_key.karpenter_spike.secret_key
  sensitive = true
}

# Active when kosmos.tf.disabled is enabled (renamed to kosmos.tf).
# output "pool_id" {
#   description = "Bare UUID of the external pool (region prefix stripped from TF id format)"
#   value       = element(split("/", scaleway_k8s_pool.external.id), length(split("/", scaleway_k8s_pool.external.id)) - 1)
# }

output "cluster_id" {
  description = "Bare UUID of the cluster (region prefix stripped from TF id format)"
  value       = element(split("/", scaleway_k8s_cluster.spike.id), length(split("/", scaleway_k8s_cluster.spike.id)) - 1)
}

output "zone" {
  value = var.zone
}
