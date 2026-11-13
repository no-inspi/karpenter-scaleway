package scaleway

import "strings"

// NormalizeNodeType maps a Scaleway node type to the canonical form used by
// the instance type catalog, CreatePool and the NodeClaim
// node.kubernetes.io/instance-type label ("PRO2-S").
//
// The Kapsule API accepts "PRO2-S" on CreatePool but returns the pool's
// node_type as "pro2_s". Comparing the raw value made every Kapsule
// NodeClaim look drifted (InstanceTypeDrift) right after launch.
func NormalizeNodeType(nodeType string) string {
	return strings.ToUpper(strings.ReplaceAll(nodeType, "_", "-"))
}
