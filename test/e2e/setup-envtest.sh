#!/usr/bin/env bash
# setup-envtest.sh — fetches the kube-apiserver/etcd binaries needed by
# controller-runtime's envtest. Prints the binary directory on stdout
# (suitable for `KUBEBUILDER_ASSETS=$(setup-envtest.sh)`).
#
# Configurable via ENVTEST_K8S_VERSION. Default tracks a recent line
# compatible with k8s.io/api v0.35.0 used in go.mod.
set -euo pipefail

ENVTEST_VERSION="${ENVTEST_K8S_VERSION:-1.34.0}"
GOBIN="${GOBIN:-${GOPATH:-${HOME}/go}/bin}"

if ! command -v setup-envtest >/dev/null 2>&1 && [[ ! -x "${GOBIN}/setup-envtest" ]]; then
  echo "installing setup-envtest..." >&2
  GOBIN="${GOBIN}" go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest >&2
fi

SETUP_ENVTEST="$(command -v setup-envtest 2>/dev/null || echo "${GOBIN}/setup-envtest")"
"${SETUP_ENVTEST}" use "${ENVTEST_VERSION}" -p path
