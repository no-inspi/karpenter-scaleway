variable "run_id" {
  type        = string
  description = "Unique identifier for this CI run; embedded in resource names and tags."
  validation {
    condition     = can(regex("^[a-z0-9-]{4,40}$", var.run_id))
    error_message = "run_id must be 4-40 chars, lowercase alphanumeric and hyphens only."
  }
}

variable "project_id" {
  type        = string
  description = "Scaleway project ID."
}

variable "organization_id" {
  type        = string
  description = "Scaleway organization ID."
}

variable "region" {
  type        = string
  default     = "fr-par"
  description = "Scaleway region for the Kosmos cluster."
}

variable "k8s_version" {
  type        = string
  default     = "1.34.0"
  description = "Kubernetes patch version. Match the value in VERSIONS.md."
}

variable "kosmos_pool_zone" {
  type        = string
  default     = "fr-par-1"
  description = "Zone of the seed external pool (Kosmos clusters require at least one external pool to be 'usable')."
}

variable "bootstrap_node_type" {
  type        = string
  default     = "DEV1-M"
  description = "Instance type for the bootstrap external node that satisfies the mutualized control-plane's >=1 node requirement and hosts kube-system + the Karpenter controller. DEV1-M (4GB) mirrors the Kapsule system pool: DEV1-S (2GB) only fits one of the chart's two controller replicas, so helm --wait never reaches 2/2 ('Insufficient memory')."
}

variable "k8s_agent_url" {
  type        = string
  default     = "https://github.com/scaleway/k8s-agent/releases/download/v0.1.5/k8s-agent_linux_amd64"
  description = "URL of the Scaleway k8s-agent binary the bootstrap node downloads to register itself as an external Kosmos node. Keep in sync with cmd/controller defaultK8sAgentURL."
}
