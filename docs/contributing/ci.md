# CI and local checks

> Goal: reproduce the GitHub Actions CI locally before pushing a pull request.

## TL;DR

```bash
make ci
```

Runs `lint-ci`, `unit` and `envtest`, in that order. Takes about 3-5 min.

## Targets

| Target | Matching CI job | Description |
|---|---|---|
| `make lint-ci` | `lint` | golangci-lint v1.64.8, 5 min timeout. |
| `make unit` | `unit` | `go test ./... -race -count=1`, no build tag. |
| `make envtest` | `envtest` | `go test -tags=e2e ./test/e2e/... -race`. Starts an in-process API server through `test/e2e/setup-envtest.sh`. |
| `make build` | `build` | `go build ./...` (cross-compiled for linux/{amd64,arm64} in CI). |
| `make ci` | (local only) | Runs lint-ci + unit + envtest. |

## Requirements

- Go 1.26+.
- For `envtest`: the `kube-apiserver` and `etcd` binaries. `make envtest`
  installs them through `test/e2e/setup-envtest.sh` (k8s 1.34.0 by default,
  override with `ENVTEST_K8S_VERSION`).

## Workflows

### `.github/workflows/ci.yml`

Runs on pull requests to `master` and on pushes to `master`. **Needs no
secrets**: lint, unit and envtest only use mocks.

Jobs run in parallel:

- `lint` (`make lint-ci`)
- `unit` (`make unit`)
- `envtest` (`make envtest`)
- `build` (matrix linux/{amd64,arm64})

`concurrency: cancel-in-progress`: a new push to a pull request cancels the
previous run.

### `.github/workflows/pr-issue-gate.yml`

Closes pull requests from external contributors that do not link an issue
labelled `accepted`. See [CONTRIBUTING.md](../../CONTRIBUTING.md).

### `.github/workflows/release.yml`

Runs on `v*` tags. Builds and pushes the controller image and the Helm chart
(OCI) to the Scaleway Container Registry, then creates a draft GitHub
Release with the binaries and checksums. See [RELEASE.md](../../RELEASE.md).

Required secrets:

- `SCW_CR_TOKEN` — IAM application key with `ContainerRegistryFullAccess`
  on the registry namespace. Preferred.
- `SCW_SECRET_KEY` — fallback when `SCW_CR_TOKEN` is not set.

### `.github/workflows/e2e.yml` (manual only)

Real-cluster E2E tests, triggered with `workflow_dispatch`. The nightly
cron and the tag trigger are disabled.

- **Matrix**: `backend × scenario` = `{kapsule, kosmos} × {ScaleUp, Drift,
  Consolidation, Cordon, Expire}`, minus `kapsule × Drift` (Kapsule's
  `IsDrifted` ignores `ImageLabel`). 9 cells.
- **Each cell**: 60 min job timeout, sets `E2E_BACKEND`, runs
  `make e2e-cloud BACKEND=<x> SCENARIO=<y>`. One cluster per cell.
- **`fail-fast: false`**: a Terraform flake in one cell does not cancel the
  others.
- **`cancel-in-progress: false`**: cancelling during `apply` would leave a
  billed cluster behind until cleanup-zombies runs, so runs are queued.
- **Artifacts on failure**: controller logs and NodeClaim/NodePool/NodeClass
  YAMLs, kept 7 days.
- **Image**: a `build-image` job first builds the controller image, pushes it
  as `rg.fr-par.scw.cloud/karpenter-scaleway/karpenter-provider-scaleway:<sha>`
  and passes it to the harness through `E2E_IMAGE_REPO` / `E2E_IMAGE_TAG`
  (`addImageOverrides` in `test/e2e/harness/scenario.go`).

Required secrets: `SCW_ACCESS_KEY`, `SCW_SECRET_KEY`,
`SCW_DEFAULT_ORGANIZATION_ID`, `SCW_DEFAULT_PROJECT_ID`, and optionally
`SCW_CR_TOKEN`. The key must be scoped to a dedicated CI project with
`KubernetesFullAccess`, `InstancesFullAccess`, `IPAMFullAccess` and
`VPCFullAccess`. See [test/e2e/README.md](../../test/e2e/README.md).

### `.github/workflows/cleanup-zombies.yml` (manual only)

Destroys Scaleway clusters tagged `karpenter-e2e=true` older than 2h on
`fr-par` / `nl-ams` / `pl-waw`, plus IAM applications named
`karpenter-e2e-*-controller` older than 2h. 2h is more than the 60 min job
timeout plus margin, so a running job is never mistaken for a zombie. Uses
the same secrets as `e2e.yml`.

## When a CI job fails

1. Reproduce locally: `make <target>` (e.g. `make unit`).
2. If it reproduces: fix and push.
3. If it does not (flake): open an issue linking the failed run. Do **not**
   retry without investigating.

## Debugging a failed E2E run

1. Open the failed run in the GitHub Actions UI.
2. Download the artifact of the failed cell (`e2e-<backend>-<scenario>-<run_id>`).
3. Check `controller.log` for reconciliation errors, `nodeclaims.yaml` for
   NodeClaim conditions, `terraform.tfstate` for what was provisioned.
4. If a cluster looks orphaned, run `cleanup-zombies.yml` manually.
