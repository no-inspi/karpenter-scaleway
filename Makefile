SHELL := /bin/bash
GO ?= go
CONTROLLER_GEN_VERSION ?= v0.16.5
# v1.61 is incompatible with the Go 1.26.x toolchain (golang.org/x/tools mismatch).
GOLANGCI_LINT_VERSION ?= v1.64.8

.PHONY: all build test lint lint-ci unit envtest e2e-cloud ci generate-catalog generate-crd release-image release-image-login release-binary release-chart release-chart-login release

all: build

build:
	$(GO) build ./...

# Legacy alias — people tend to type `make test`. Points to unit.
test: unit

# CI job "unit": fast tests without the e2e build tag, race detector on.
unit:
	$(GO) test -race -count=1 ./...

# CI job "envtest": in-process integration tests with controller-runtime
# envtest + httptest Scaleway mocks. The `e2e` tag is excluded from `unit`.
# test/e2e/setup-envtest.sh installs and resolves KUBEBUILDER_ASSETS
# (k8s 1.34.0 by default, override with ENVTEST_K8S_VERSION).
envtest:
	KUBEBUILDER_ASSETS="$$(./test/e2e/setup-envtest.sh)" \
		$(GO) test -tags=e2e -race -count=1 ./test/e2e/...

# Legacy alias. `make lint` = quick local go vet.
lint:
	$(GO) vet ./...

# CI job "lint": full golangci-lint. Strict, can take 2-5 min.
lint-ci:
	$(GO) run github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --timeout=5m

# Runs locally what the CI runs.
ci: lint-ci unit envtest

# Regenerate pkg/instancetype/catalog.json from the live Scaleway API.
# Requires SCW credentials in env or ~/.config/scw/config.yaml.
generate-catalog:
	$(GO) run ./cmd/generate-catalog

generate-crd:
	$(GO) run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) \
		object paths=./api/v1alpha1/...
	$(GO) run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) \
		crd paths=./api/v1alpha1/... output:crd:dir=./charts/karpenter-scaleway/crds

# "e2e-cloud": runs the Karpenter scenarios against a real Scaleway
# cluster provisioned on the fly by Terraform. This costs real money on
# Scaleway — read test/e2e/README.md before running it.
#
# Variables :
#   BACKEND        kapsule | kosmos          (REQUIRED)
#   SCENARIO       ScaleUp | Drift | ...     (default: all)
#   E2E_RUN_ID     unique run identifier    (default: e2e-$(shell date +%s))
#   E2E_REGION     Scaleway region          (default: fr-par)
#
# Credentials expected in the environment (not in Make):
#   SCW_ACCESS_KEY SCW_SECRET_KEY
#   SCW_DEFAULT_ORGANIZATION_ID SCW_DEFAULT_PROJECT_ID
e2e-cloud:
	@if [ -z "$(BACKEND)" ]; then echo "BACKEND=kapsule|kosmos required"; exit 2; fi
	E2E_RUN_ID="$${E2E_RUN_ID:-e2e-$$(date +%s)}" \
	E2E_REGION="$${E2E_REGION:-fr-par}" \
	E2E_BACKEND=$(BACKEND) \
	$(GO) test -tags=e2ecloud -count=1 -timeout=60m \
		$(if $(SCENARIO),-run=^TestE2E_$(SCENARIO)$$,) \
		./test/e2e/scenarios/...

# ---------------------------------------------------------------------
# Image publication
# ---------------------------------------------------------------------

# Where to push the controller image. Default = the public Scaleway
# CR namespace referenced by charts/karpenter-scaleway/values.yaml so
# `helm install` works without --set image.repository for users.
IMAGE_REPO ?= rg.fr-par.scw.cloud/karpenter-scaleway/karpenter-provider-scaleway

# Tag for the build. Local default = `git describe`. CI overrides
# this with the commit SHA so each smoke run pulls a fresh image.
IMAGE_TAG ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Buildx target platform. Single-arch by default to keep CI fast;
# multi-arch (linux/amd64,linux/arm64) is a PR-C concern.
IMAGE_PLATFORMS ?= linux/amd64

# Buildx + push. Requires `docker login` to $(IMAGE_REPO)'s registry
# already done (see release-image-login or the build-image CI job).
release-image:
	docker buildx build \
		--platform=$(IMAGE_PLATFORMS) \
		--build-arg VERSION=$(IMAGE_TAG) \
		--tag $(IMAGE_REPO):$(IMAGE_TAG) \
		--push \
		.

# Convenience target: docker login to Scaleway CR using SCW_CR_TOKEN.
# The Scaleway CR auth pattern is `-u nologin -p <iam-app-key>`.
# Fails fast with a clear error if SCW_CR_TOKEN (or its SCW_SECRET_KEY
# fallback) is unset.
release-image-login:
	@token="$${SCW_CR_TOKEN:-$$SCW_SECRET_KEY}"; \
	if [ -z "$$token" ]; then \
		echo "SCW_CR_TOKEN or SCW_SECRET_KEY required (Scaleway IAM app key with ContainerRegistryFullAccess)"; \
		exit 2; \
	fi; \
	echo "$$token" | docker login rg.fr-par.scw.cloud -u nologin --password-stdin

# ---------------------------------------------------------------------
# Release binaries
# ---------------------------------------------------------------------

# Version stamped into the binary (via ldflags) and into the archive
# names. Same default as IMAGE_TAG so a `git describe` drives both the
# image tag and the binary archives in lock-step. CI/release.yml (PR-D)
# overrides this with the tag ref.
BINARY_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Output directory for cross-compiled archives + checksums. gitignored.
DIST_DIR ?= dist

# <os>/<arch> pairs to cross-compile. The controller only ever runs
# in-cluster (Linux), and this mirrors the ci.yml build matrix.
RELEASE_PLATFORMS ?= linux/amd64 linux/arm64

# Shared with the Dockerfile build stage: strip debug info (-s -w) and
# inject the version. Paired with `go build -trimpath` for reproducible
# output.
RELEASE_LDFLAGS := -s -w -X github.com/no-inspi/karpenter-scaleway/pkg/version.Version=$(BINARY_VERSION)

# Cross-compile ./cmd/controller for every RELEASE_PLATFORMS pair into a
# per-platform .tar.gz under $(DIST_DIR) (binary at archive root), then
# emit a single coreutils-format SHA256SUMS. No Docker/registry — pure
# `go build`, reproducible locally. release.yml (PR-D) attaches these to
# the GitHub release. sha256sum (Linux) / shasum -a 256 (macOS) fallback
# keeps it portable across dev + CI.
release-binary:
	@rm -rf $(DIST_DIR)
	@mkdir -p $(DIST_DIR)
	@bin=karpenter-provider-scaleway; \
	for platform in $(RELEASE_PLATFORMS); do \
		os="$${platform%/*}"; arch="$${platform#*/}"; \
		echo ">> building $${os}/$${arch} ($(BINARY_VERSION))"; \
		CGO_ENABLED=0 GOOS="$${os}" GOARCH="$${arch}" \
			$(GO) build -trimpath -ldflags="$(RELEASE_LDFLAGS)" \
			-o "$(DIST_DIR)/$${bin}" ./cmd/controller || exit 1; \
		tar -czf "$(DIST_DIR)/$${bin}_$(BINARY_VERSION)_$${os}_$${arch}.tar.gz" \
			-C "$(DIST_DIR)" "$${bin}"; \
		rm -f "$(DIST_DIR)/$${bin}"; \
	done; \
	cd $(DIST_DIR) && \
	if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum *.tar.gz > SHA256SUMS; \
	else \
		shasum -a 256 *.tar.gz > SHA256SUMS; \
	fi
	@echo ">> artifacts in $(DIST_DIR):" && ls -1 $(DIST_DIR)

# ---------------------------------------------------------------------
# Helm chart publishing
# ---------------------------------------------------------------------

# OCI repo the chart is pushed to. Sub-namespace `charts` of the same
# Scaleway CR namespace as the controller image, so image and chart
# live side-by-side but in separate paths. `helm push` appends the
# chart name, landing the chart at karpenter-scaleway/charts/karpenter-scaleway.
CHART_OCI_REPO ?= oci://rg.fr-par.scw.cloud/karpenter-scaleway/charts

# Chart source + package output dir (gitignored dist/, shared with release-binary).
CHART_DIR ?= charts/karpenter-scaleway

# Version + appVersion stamped into the packaged chart. Empty by default,
# so a local `make release-chart` uses the version already in Chart.yaml.
# release.yml overrides this with the git tag (leading `v` stripped) so a
# tag push publishes a chart whose version matches the image + binaries.
CHART_VERSION ?=

# helm package + push. Requires `helm registry login` already done
# (see release-chart-login or the release.yml workflow). The packaged
# version comes from Chart.yaml (bump it before tagging — see RELEASE.md)
# unless CHART_VERSION is set, in which case it overrides both version
# and appVersion.
release-chart:
	@mkdir -p $(DIST_DIR)
	@rm -f $(DIST_DIR)/karpenter-scaleway-*.tgz
	helm package $(CHART_DIR) --destination $(DIST_DIR) $(if $(CHART_VERSION),--version $(CHART_VERSION) --app-version $(CHART_VERSION))
	helm push $(DIST_DIR)/karpenter-scaleway-*.tgz $(CHART_OCI_REPO)

# Convenience: helm login to Scaleway CR. Mirrors release-image-login —
# same `-u nologin` + IAM-app-key-over-stdin pattern, SCW_CR_TOKEN with
# SCW_SECRET_KEY fallback (kept consistent with the image login flow).
release-chart-login:
	@token="$${SCW_CR_TOKEN:-$$SCW_SECRET_KEY}"; \
	if [ -z "$$token" ]; then \
		echo "SCW_CR_TOKEN or SCW_SECRET_KEY required (Scaleway IAM key with ContainerRegistryFullAccess)"; \
		exit 2; \
	fi; \
	echo "$$token" | helm registry login rg.fr-par.scw.cloud -u nologin --password-stdin

# ---------------------------------------------------------------------
# Release orchestrator
# ---------------------------------------------------------------------

# Ordering: release-binary runs FIRST because it does `rm -rf $(DIST_DIR)`
# to start from a clean dir; running it first means the later release-chart
# `helm package` deposits its .tgz into dist without being clobbered. Final
# dist/ therefore holds the binary archives + SHA256SUMS (uploaded by
# release.yml) alongside the chart tgz. Assumes both registry logins are
# already done (release-image-login + release-chart-login) — release.yml
# runs them first. VERSION is driven by git tags (IMAGE_TAG / BINARY_VERSION
# both default to `git describe`); on a tag push the CI job pins them to the
# tag ref.
release: release-binary release-image release-chart
