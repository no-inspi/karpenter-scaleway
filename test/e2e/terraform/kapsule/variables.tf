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
  description = "Scaleway region for the Kapsule cluster."
}

variable "k8s_version" {
  type        = string
  default     = "1.34.0"
  description = "Kubernetes patch version. Match the value in VERSIONS.md."
}

variable "system_node_type" {
  type        = string
  default     = "DEV1-M"
  description = "Instance type for the system pool that hosts kube-system + the Karpenter controller."
}
