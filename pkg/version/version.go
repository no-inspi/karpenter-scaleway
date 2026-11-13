// Package version exposes the controller's build-time version string.
// The default "dev" sentinel is overridden via:
//
//	-ldflags "-X github.com/no-inspi/karpenter-scaleway/pkg/version.Version=v0.1.0"
//
// at build time (see Dockerfile + Makefile release-image target).
package version

var Version = "dev"
