package scaleway

import (
	"strings"
	"testing"
)

func TestRenderCloudInit(t *testing.T) {
	tests := []struct {
		name        string
		params      CloudInitParams
		mustHave    []string
		mustNotHave []string
	}{
		{
			name: "kosmos GP1-XS basic",
			params: CloudInitParams{
				PoolID:      "pool-uuid-1",
				PoolRegion:  "fr-par",
				APIKey:      "scw-secret-xyz",
				K8sAgentURL: "https://github.com/scaleway/k8s-agent/releases/download/v0.1.5/k8s-agent_linux_amd64",
			},
			mustHave: []string{
				"#!/bin/bash",
				"set -euo pipefail",
				"POOL_ID=pool-uuid-1",
				"POOL_REGION=fr-par",
				"SCW_SECRET_KEY=scw-secret-xyz",
				"k8s-agent_linux_amd64",
				"--kosmos",
				// The kubelet drop-in must declare the karpenter.sh/unregistered
				// taint, and it must be staged before k8s-agent runs so kubelet
				// picks it up on first start.
				"/etc/systemd/system/kubelet.service.d/20-karpenter-taints.conf",
				"--register-with-taints=karpenter.sh/unregistered:NoExecute",
			},
			mustNotHave: []string{
				"{{", "}}",
				"--pool-id=",
				"--api-key=",
				"--node-name=",
				"k8s-agent.service",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := RenderCloudInit(tt.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, s := range tt.mustHave {
				if !strings.Contains(out, s) {
					t.Errorf("missing %q in output:\n%s", s, out)
				}
			}
			for _, s := range tt.mustNotHave {
				if strings.Contains(out, s) {
					t.Errorf("unexpected %q in output:\n%s", s, out)
				}
			}
		})
	}
}

// TestRenderCloudInit_TaintBeforeAgent guards the taint ordering invariant:
// the kubelet systemd drop-in carrying --register-with-taints must be written
// before k8s-agent installs and starts kubelet. If the order flips, kubelet
// boots without the taint and Karpenter logs a registration error every join.
func TestRenderCloudInit_TaintBeforeAgent(t *testing.T) {
	out, err := RenderCloudInit(CloudInitParams{
		PoolID:      "pool-uuid-1",
		PoolRegion:  "fr-par",
		APIKey:      "scw-secret-xyz",
		K8sAgentURL: "https://example.test/k8s-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	taintIdx := strings.Index(out, "karpenter.sh/unregistered:NoExecute")
	agentIdx := strings.Index(out, "/usr/local/bin/k8s-agent --kosmos")
	if taintIdx < 0 || agentIdx < 0 {
		t.Fatalf("expected both taint drop-in and k8s-agent invocation in script:\n%s", out)
	}
	if taintIdx >= agentIdx {
		t.Errorf("kubelet taint drop-in (idx %d) must be staged before k8s-agent invocation (idx %d)", taintIdx, agentIdx)
	}
}
