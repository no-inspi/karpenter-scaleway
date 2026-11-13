#!/usr/bin/env bash
# run.sh — runs the integration suite against an in-process envtest
# (kube-apiserver + etcd binaries downloaded once via setup-envtest).
#
# No Docker, no kind. ~3-5 s start-up cold, near-instant on subsequent runs.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ASSETS="$("${REPO_ROOT}/test/e2e/setup-envtest.sh")"

KUBEBUILDER_ASSETS="${ASSETS}" \
  go test -tags=e2e -count=1 -v "${REPO_ROOT}/test/e2e/..."
