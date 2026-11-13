//go:build e2ecloud
// +build e2ecloud

// Package scenarios exercises the Karpenter Scaleway provider against
// real Scaleway-managed clusters provisioned by the Terraform modules
// under test/e2e/terraform/. Tests in this package run only when the
// build tag `e2ecloud` is set.
//
// They are NOT run by `go test ./...`, by the Phase 1 CI workflow
// (ci.yml — unit/envtest), nor by the envtest suite under
// test/e2e/e2e_test.go (build tag `e2e`, in-process apiserver).
//
// Entry points:
//   - GitHub Actions workflow .github/workflows/e2e.yml (nightly + tags).
//   - Local: `make e2e-cloud BACKEND=kapsule SCENARIO=ScaleUp` (see test/e2e/README.md).
//
// Required env at run time:
//
//	SCW_ACCESS_KEY, SCW_SECRET_KEY,
//	SCW_DEFAULT_ORGANIZATION_ID, SCW_DEFAULT_PROJECT_ID.
//
// Optional env:
//
//	E2E_BACKEND   "kapsule" or "kosmos" — restrict subtests to one
//	              backend (matches the per-cell isolation the
//	              workflow matrix needs). Default: both backends.
//	E2E_REGION    Scaleway region (default: fr-par).
//	E2E_RUN_ID    custom run ID for resource names. Default:
//	              auto-generated "loc-<8hex>" or
//	              "gh-<run_id>-a<attempt>-<backend>-<scenario>"
//	              under CI.
//
// Module paths (chart, Terraform modules) are resolved relative to
// the repo root via harness.AbsModuleDir, walking up to find go.mod.
// No env override is supported — keep the test invocation reproducible.
package scenarios
