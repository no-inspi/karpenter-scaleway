# E2E / integration tests

End-to-end tests for the NodeClass reconciler. Runs against an
**in-process kube-apiserver** via controller-runtime's `envtest` (no
Docker, no kind, no real cloud). Aligned with the test pattern used by
upstream `sigs.k8s.io/karpenter` and the AWS / AKS providers.

## What's covered

| Test | What it verifies |
|---|---|
| `TestE2E_NodeClassReady` | Reconciler writes `Ready=True` + `resolvedImageID` after a Marketplace hit. |
| `TestE2E_ImageNotFound` | Typo in `imageLabel` surfaces as `Ready=False` / `ImageNotFound`. |
| `TestE2E_ValidationPending` | Marketplace 5xx surfaces as `Ready=False` / `ValidationPending`. |
| `TestE2E_KapsuleUnusedFields` | Kosmos-only fields populate `status.unusedFieldsForBackend` when the backend is Kapsule. |

The tests exercise:

- The chart's CRD YAML applied against a real apiserver (validation,
  status subresource, schema enforcement).
- The actual reconciler code path (controller-runtime manager + watch +
  Reconcile loop).
- The Scaleway SDK's `MarketplaceClient` against an `httptest.Server`
  routed via `scw.WithAPIURL`.

## What's NOT covered

Real Scaleway Instance / Pool provisioning — that requires a real
Scaleway account and is covered by the real-cluster suite below. The
Backend interface methods (`Create`/`Delete`/`Get`/`List`) are tested
with hand-rolled fakes in `pkg/backend/{kapsule,kosmos}/*_test.go`.

## Prerequisites

- Go 1.22+ (matches `go.mod`)
- `setup-envtest` — auto-installed by `setup-envtest.sh` on first run.
- **No Docker required.**

## Run

```sh
./test/e2e/run.sh
```

`run.sh` calls `setup-envtest.sh` to fetch the matching `kube-apiserver`
+ `etcd` binaries (cached under `~/Library/Application Support/io.kubebuilder.envtest/`
on macOS), then runs `go test -tags=e2e ./test/e2e/...`.

Cold start: ~5-8 s (binaries + envtest boot). Warm runs: ~6-7 s with all
4 tests in <2.5 s combined.

To pick a different apiserver version:

```sh
ENVTEST_K8S_VERSION=1.30.0 ./test/e2e/run.sh
```

## Notes

- Tests use the build tag `e2e` so `go test ./...` does **not** trigger
  them. They only run via `run.sh` or `go test -tags=e2e ./test/e2e/...`.
- A single `envtest.Environment` is shared across the suite via
  `TestMain`; each subtest gets its own controller-runtime manager
  instance, but the apiserver only boots once.
- The Marketplace mock intercepts `/marketplace/v2/local-images` only.
  Tests that need other Scaleway endpoints would need to extend
  `marketplaceMock`.

---

## Phase 2 — real-cluster scenarios

Lives under `test/e2e/{terraform,harness,scenarios}/`. Build tag `e2ecloud`. **Costs real Scaleway € — read this section before running.**

### Run locally (one cell)

Prerequisites:

- `terraform` ≥ 1.5
- `helm` ≥ 3.10
- `kubectl` ≥ 1.30
- An IAM application with `KubernetesFullAccess`/`InstancesFullAccess`/etc. on a Scaleway project (the harness module provisions a per-run application but it needs root creds to do so).
- Env: `SCW_ACCESS_KEY`, `SCW_SECRET_KEY`, `SCW_DEFAULT_ORGANIZATION_ID`, `SCW_DEFAULT_PROJECT_ID`.

```sh
# Single scenario
make e2e-cloud BACKEND=kapsule SCENARIO=ScaleUp

# Full kapsule matrix (4 scenarios — drift is kosmos-only)
make e2e-cloud BACKEND=kapsule

# Custom run_id (otherwise auto-generated as loc-<8hex>)
E2E_RUN_ID=alice-debug make e2e-cloud BACKEND=kosmos SCENARIO=Drift
```

Each scenario provisions its own cluster, runs ~5–10 min of assertions, then destroys. If `terraform destroy` fails, the cluster is reaped within 6h by `.github/workflows/cleanup-zombies.yml`.

### Wall-time per cell

| Phase | Kapsule | Kosmos |
|---|---|---|
| `terraform apply` | 4–6 min | 6–10 min |
| `helm install --wait` | 30–60 s | 30–60 s |
| Scenario assertions | 2–7 min | 4–9 min |
| `terraform destroy` | 2–3 min | 3–5 min |
| **Total** | **~10–15 min** | **~14–25 min** |

### File layout

- `terraform/{kapsule,kosmos}/` — per-backend modules. Per-run resources tagged `karpenter-e2e=true` so cleanup-zombies can find orphans.
- `harness/` — Go wrappers (`Terraform`, `KarpenterInstaller`, `SetupCluster`, `PodPending`). Unit-tested under `make unit` against POSIX shims; no real Scaleway needed for unit tests.
- `scenarios/*_test.go` — build tag `e2ecloud`. NOT picked up by `go test ./...`. Five scenarios: `ScaleUp`, `Drift` (Kosmos-only), `Consolidation`, `Cordon`, `Expire`. Each iterates `harness.SelectedBackends()` (filtered by `E2E_BACKEND` env when set).

### Why a separate build tag?

`test/e2e/e2e_test.go` (Phase 1, build tag `e2e`) runs an in-process apiserver via envtest — no cloud. The Phase 2 scenarios need a separate tag (`e2ecloud`) to keep the two suites disjoint and let CI run only one of them per job (`make envtest` vs `make e2e-cloud`).

### Why no Kapsule drift?

The Kapsule backend's `IsDrifted` (see `pkg/backend/drift/drift.go`) intentionally ignores `Spec.ImageLabel` because the Scaleway Kapsule control plane chooses worker images internally. Mutating `ImageLabel` on a Kapsule cluster therefore produces no drift signal. The `Drift` scenario runs only against Kosmos.

### Pre-flight discovery wait

`SetupCluster` calls `harness.WaitForAPIDiscovery` between `terraform apply` and `helm upgrade --install`. This is not optional cosmetic latency — without it, helm's discovery phase calls `metrics.k8s.io/v1beta1` (registered by Kapsule's metrics-server addon) before the metrics-server pod is Ready and bails with `client rate limiter Wait returned an error: context deadline exceeded` after ~12 s. The harness replicates the exact call helm makes (`Discovery().ServerGroupsAndResources()`) on a 5 s loop until it returns nil error or the context deadline fires (5 min on Kapsule, 12 min on Kosmos, where metrics-server stays Pending until the bootstrap node joins after ~5–7 min); once our discovery succeeds, helm's will too. On a healthy fresh cluster the wait completes in 30–90 s.

We deliberately do *not* check `kubectl wait apiservice --all --for=condition=Available` — that was the first attempt and it succeeded too eagerly because the unhealthy `metrics.k8s.io/v1beta1` APIService had not yet been registered at probe time. Replicating helm's actual call is more honest and surfaces the same error string helm would, with no kubectl tooling dependency.

### CI configuration (GitHub Actions)

The nightly cron (`.github/workflows/e2e.yml`) and the manual `workflow_dispatch` smoke require **four repo secrets** (`gh secret set <NAME> --repo <owner>/<repo>`):

| Secret | Format | Where to find |
|---|---|---|
| `SCW_ACCESS_KEY` | `SCW…` (20 alphanumeric) | Console → IAM → API keys, on creation |
| `SCW_SECRET_KEY` | UUID, **shown once at creation** | Console → IAM → API keys, on creation |
| `SCW_DEFAULT_ORGANIZATION_ID` | UUID | Console top-left — Organization ID |
| `SCW_DEFAULT_PROJECT_ID` | UUID | Console → Project Dashboard → Project ID |

Recommended IAM application permissions on the target project (least-privilege within what the per-run Terraform module needs to do):

- `KubernetesFullAccess` — create/destroy Kapsule and Kosmos clusters.
- `IAMManager` — provision per-run controller IAM apps with `expires_at = apply_time + 6h`.
- `VPCFullAccess` — Kapsule/Kosmos VPC + private network.
- `InstancesFullAccess` — Kosmos worker instances.
- `IPAMFullAccess` — IP allocations on Kapsule pools.

Use a **dedicated CI IAM application** (not your personal key) so it can be rotated/revoked independently. The harness then derives a *second*, scoped, 6h-TTL key per run for the controller itself.

Verify the secrets are visible to Actions:

```sh
gh secret list --repo <owner>/<repo>
# Expect exactly the four SCW_* names; no extra entries.
```

Without these, the test fails fast at `requireEnv` ([scenario.go](harness/scenario.go)) with `SCW_DEFAULT_PROJECT_ID is required for real-cluster scenarios` before any Terraform call — no cluster created, no cost, but no signal either.

## Image publication for smoke runs

The E2E workflow publishes a fresh controller image **per run** to Scaleway CR before launching the matrix. The matrix then installs the chart with `--set image.repository=… --set image.tag=<commit-sha>`.

This is wired automatically via the `build-image` job in `.github/workflows/e2e.yml` and the `addImageOverrides` helper in `test/e2e/harness/scenario.go`. There is nothing to do locally — when running `make e2e-cloud` on a workstation the env vars are unset and the chart falls back to `values.yaml`'s default repository.

### Authentication: two options

The `build-image` job authenticates to Scaleway CR with the standard `docker login -u nologin -p <secret-key>` pattern. The login step accepts either of two repo secrets, in this order:

| Priority | Secret | When to use |
| --- | --- | --- |
| 1 (preferred) | `SCW_CR_TOKEN` | Dedicated IAM application with `ContainerRegistryFullAccess` scoped to namespace `karpenter-scaleway`. Separation of privilege — recommended. |
| 2 (fallback) | `SCW_SECRET_KEY` | The shared CI secret already used by the E2E matrix. Quick path: just attach `ContainerRegistryFullAccess` to the existing CI IAM application. Prefer option 1. |

The workflow fails fast with a clear error if **neither** is set.

### Scaleway-side provisioning (one-time)

1. Create a Scaleway CR namespace `karpenter-scaleway` in region `fr-par`. This is the production namespace that hosts released images and charts (it replaced the pre-release `test-karpenter-pkg` namespace).
2. Mark it **public** (Scaleway console → Container Registry → namespace settings → Visibility = Public). Required so the chart's default `imagePullSecrets: []` works at install time.
3. Pick one of:
   - **Option A (clean)**: IAM → Applications → create `karpenter-e2e-cr-push` → attach a policy with `ContainerRegistryFullAccess` (scope to namespace `karpenter-scaleway` if available, otherwise project-level) → generate an API key → copy the **Secret Key** into repo secret `SCW_CR_TOKEN`.
   - **Option B (quick)**: extend the existing CI IAM application's policy to add `ContainerRegistryFullAccess`. No new secret to provision — the workflow falls back to `SCW_SECRET_KEY`.

The Secret Key is shown once at API-key creation; if lost, generate a new key (and revoke the old one).
