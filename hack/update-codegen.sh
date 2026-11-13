#!/usr/bin/env bash
# update-codegen.sh — regenerates DeepCopy and CRD manifests from the
# v1alpha1 Go types using controller-gen.
#
# Prerequisite:
#   go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5
#
# The hand-written zz_generated.deepcopy.go and the CRD YAML under
# charts/karpenter-scaleway/crds/ should match the controller-gen output.
# CI runs this script with --check to catch drift.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

if ! command -v controller-gen >/dev/null 2>&1; then
  echo "controller-gen not found in PATH — install via:" >&2
  echo "  go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5" >&2
  exit 1
fi

# Regenerate DeepCopy methods in-place.
controller-gen object paths=./api/v1alpha1/...

# Regenerate the CRD manifest into the Helm chart's crds/ directory so it
# ships with the chart as required by the Helm 3 convention.
controller-gen crd \
  paths=./api/v1alpha1/... \
  output:crd:dir=./charts/karpenter-scaleway/crds

echo "codegen OK"
