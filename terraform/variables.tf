variable "project_id" {
  type        = string
  description = "Scaleway project ID for the spike"
}

variable "organization_id" {
  type        = string
  description = "Scaleway organization ID (root account UUID, distinct from project_id)"
}

variable "region" {
  type        = string
  description = "Scaleway region"
  default     = "fr-par"
}

variable "zone" {
  type        = string
  description = "Scaleway zone — all Instances will be created here"
  default     = "fr-par-1"
}

variable "k8s_version" {
  type        = string
  description = "Exact Kubernetes patch version, must match VERSIONS.md"
  # ex: "1.34.0"
}
