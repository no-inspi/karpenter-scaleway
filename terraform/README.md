# Terraform — Spike provisioning

## Pre-requisites

- Scaleway admin credentials in env: `SCW_ACCESS_KEY`, `SCW_SECRET_KEY`, `SCW_DEFAULT_PROJECT_ID`
- Terraform >= 1.5
- A `var.k8s_version` matching VERSIONS.md (e.g. "1.34.0")

## Apply

```bash
terraform init
terraform apply \
  -var="project_id=$SCW_DEFAULT_PROJECT_ID" \
  -var="k8s_version=1.34.0" \
  -auto-approve

terraform output -raw kubeconfig > ../kubeconfig.yaml
chmod 600 ../kubeconfig.yaml
```

## Destroy

```bash
terraform destroy \
  -var="project_id=$SCW_DEFAULT_PROJECT_ID" \
  -var="k8s_version=1.34.0" \
  -auto-approve
```

## Outputs

- `kubeconfig` — kubeconfig for the Kosmos cluster (sensitive)
- `scw_secret_key` — IAM key used by the controller and injected in cloud-init (sensitive)
- `pool_id` — multi-cloud external pool ID (passed to k8s-agent)
- `cluster_id` — for diagnostics
- `zone` — the zone all Instances are created in (default `fr-par-1`)

State is local (`terraform.tfstate`), gitignored. Spike state is disposable.
