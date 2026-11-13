package main

import (
	"strings"

	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
)

// denylistPrefixes is the static list of name prefixes excluded from the
// catalog. Most of the noise (DEV1, GPU SKUs) is captured by EndOfService
// or Gpu>0 — this list catches the rest (currently only STARDUST trial).
var denylistPrefixes = []string{"STARDUST"}

type acceptResult struct {
	keep   bool
	reason string // empty if keep
}

// accept implements the catalog inclusion rules in fixed order: EndOfService,
// GPU, arch, name-prefix denylist. The first failing rule decides.
func accept(name string, st *instance.ServerType) acceptResult {
	if st.EndOfService {
		return acceptResult{false, "endOfService"}
	}
	if st.Gpu != nil && *st.Gpu > 0 {
		return acceptResult{false, "gpu"}
	}
	if mapArch(st.Arch) == "" {
		return acceptResult{false, "arch"}
	}
	for _, p := range denylistPrefixes {
		if strings.HasPrefix(name, p) {
			return acceptResult{false, "namePrefix"}
		}
	}
	return acceptResult{true, ""}
}

// mapArch normalises Scaleway Arch enum to Karpenter labels. Returns ""
// for unsupported arches (caller treats as exclusion).
func mapArch(a instance.Arch) string {
	switch a {
	case instance.ArchX86_64:
		return "amd64"
	case instance.ArchArm64:
		return "arm64"
	default:
		return ""
	}
}
