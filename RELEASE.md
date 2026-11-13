# Release runbook

How to cut a `karpenter-provider-scaleway` release. The pipeline publishes
three artifacts on a git tag: the controller **image**, the Helm **chart**
(both to the Scaleway Container Registry), and cross-compiled **binaries**
attached to a GitHub Release draft.

## Prerequisites

- Push access to `master` and permission to create tags.
- GitHub repo secret `SCW_CR_TOKEN` — a Scaleway IAM app key with
  `ContainerRegistryFullAccess` on the `karpenter-scaleway` namespace.
  `SCW_SECRET_KEY` is accepted as a fallback (same key the E2E workflow uses).
- Helm 3, Docker Buildx, and Go 1.26 for a local dry-run (optional).

> **Follow-up (not yet automated):** privilege separation — a dedicated CI
> account whose IAM rule is scoped to the `charts` namespace only, distinct
> from the image-push credential. Today a
> single `SCW_CR_TOKEN` pushes both image and chart.

## Steps

1. **Bump versions.** Edit `charts/karpenter-scaleway/Chart.yaml` — set
   `version` and `appVersion` to the new `X.Y.Z`.
2. **Update the changelog.** Add a `[X.Y.Z]` entry to `CHANGELOG.md`
   (Keep-a-Changelog format) summarising the release.
3. **Commit** the bump on `master` (via PR):
   `git commit -am "release: vX.Y.Z"`.
4. **Tag** an annotated tag and push it — this triggers `release.yml`:
   ```sh
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```
   Published artifact versions are derived from this tag with the leading
   `v` stripped, so tagging e.g. `v0.4.0-rc.1` publishes image/chart/binaries
   versioned `0.4.0-rc.1` without needing to edit Chart.yaml for a pre-release.
5. **Watch the workflow.** `gh run watch` — the `release` job runs
   `make release` (image + chart + binary) and creates a draft GitHub Release.
6. **Verify the registry artifacts:**
   ```sh
   # image
   docker manifest inspect rg.fr-par.scw.cloud/karpenter-scaleway/karpenter-provider-scaleway:vX.Y.Z
   # chart
   helm pull oci://rg.fr-par.scw.cloud/karpenter-scaleway/charts/karpenter-scaleway --version X.Y.Z
   ```
7. **Smoke-test the chart** on a throwaway cluster:
   ```sh
   helm install karpenter-scaleway \
     oci://rg.fr-par.scw.cloud/karpenter-scaleway/charts/karpenter-scaleway \
     --version X.Y.Z -n karpenter --create-namespace --set ...
   ```
8. **Publish the GitHub Release.** Edit the auto-created draft: paste the
   CHANGELOG section as release notes, confirm the `.tar.gz` binaries +
   `SHA256SUMS` are attached, then publish.

> **On failure:** `make release` builds the binaries, then pushes the image,
> then packages+pushes the chart. A mid-run failure may have already pushed a
> partial image and/or chart to the registry even though no GitHub Release
> draft was created — check the Scaleway registry directly before re-tagging.

## Test release

Validate the pipeline end-to-end with a release-candidate tag before the
first real release:

```sh
git tag -a v0.4.0-rc.1 -m "pipeline test"
git push origin v0.4.0-rc.1
```

Expected: workflow green → image + chart visible on the registry →
`helm install oci://…` works on a fresh cluster.
