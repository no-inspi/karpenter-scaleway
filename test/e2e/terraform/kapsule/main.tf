terraform {
  required_version = ">= 1.5"
  required_providers {
    scaleway = {
      source  = "scaleway/scaleway"
      version = "~> 2.40"
    }
    time = {
      source  = "hashicorp/time"
      version = "~> 0.11"
    }
  }
  # Local backend on purpose: each CI job exports its own working dir,
  # state is disposable, every run does its own apply+destroy. Bumping
  # to remote backend = follow-up if we observe state-loss on retries.
}

provider "scaleway" {
  region          = var.region
  zone            = "${var.region}-1"
  project_id      = var.project_id
  organization_id = var.organization_id
}
