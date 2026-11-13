terraform {
  required_version = ">= 1.5"
  required_providers {
    scaleway = {
      source  = "scaleway/scaleway"
      version = "~> 2.40"
    }
  }
  # Local backend — spike state is disposable.
}

provider "scaleway" {
  zone            = var.zone
  region          = var.region
  project_id      = var.project_id
  organization_id = var.organization_id
}
