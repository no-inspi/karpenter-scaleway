package scaleway

import (
	"testing"

	k8s "github.com/scaleway/scaleway-sdk-go/api/k8s/v1"
)

// Kapsule returns pool.node_type as "pro2_s" while the catalog, the
// CreatePool request and the NodeClaim instance-type label use "PRO2-S".
// Seen on a test cluster: every NodeClaim was flagged
// InstanceTypeDrift right after launch and replaced in a loop.
func TestNormalizeNodeType(t *testing.T) {
	cases := map[string]string{
		"pro2_s":        "PRO2-S",
		"gp1_xs":        "GP1-XS",
		"PRO2-S":        "PRO2-S",
		"pop2_hc_2c_4g": "POP2-HC-2C-4G",
		"basic2_a2c_4g": "BASIC2-A2C-4G",
		"":              "",
	}
	for in, want := range cases {
		if got := NormalizeNodeType(in); got != want {
			t.Errorf("NormalizeNodeType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToKapsulePoolNormalizesNodeType(t *testing.T) {
	p := toKapsulePool(&k8s.Pool{ID: "p", NodeType: "pro2_s", Tags: []string{"nodeclaim=nc-1"}})
	if p.NodeType != "PRO2-S" {
		t.Fatalf("NodeType = %q, want PRO2-S", p.NodeType)
	}
}
