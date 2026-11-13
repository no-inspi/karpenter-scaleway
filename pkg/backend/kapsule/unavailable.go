package kapsule

import (
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// unavailableOfferingTTL is how long an (instance type, zone) offering stays
// masked after Scaleway failed to provision it. Long enough for Karpenter to
// re-solve the pending pods against other offerings, short enough that a
// transient capacity shortage does not exclude the offering for long.
const unavailableOfferingTTL = 3 * time.Minute

// unavailableOfferings remembers offerings that recently ended in a Scaleway
// "creation_error" so GetInstanceTypes can report them as unavailable and
// Karpenter's next launch attempt picks another instance type or zone.
type unavailableOfferings struct {
	mu      sync.Mutex
	entries map[string]time.Time // key → expiry
	now     func() time.Time
}

func newUnavailableOfferings() *unavailableOfferings {
	return &unavailableOfferings{entries: map[string]time.Time{}, now: time.Now}
}

func offeringKey(instanceType, zone string) string { return instanceType + "/" + zone }

// MarkUnavailable masks the offering for unavailableOfferingTTL.
func (u *unavailableOfferings) MarkUnavailable(instanceType, zone string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.entries[offeringKey(instanceType, zone)] = u.now().Add(unavailableOfferingTTL)
}

// Apply flips Available to false on every masked offering of types, pruning
// expired entries along the way. types must be owned by the caller
// (pricing.Hydrate returns fresh copies).
func (u *unavailableOfferings) Apply(types []*karpcp.InstanceType) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.entries) == 0 {
		return
	}
	now := u.now()
	for key, expiry := range u.entries {
		if !now.Before(expiry) {
			delete(u.entries, key)
		}
	}
	for _, t := range types {
		for _, o := range t.Offerings {
			r := o.Requirements.Get(corev1.LabelTopologyZone)
			if r == nil {
				continue
			}
			for _, zone := range r.Values() {
				if _, ok := u.entries[offeringKey(t.Name, zone)]; ok {
					o.Available = false
				}
			}
		}
	}
}

// IsUnavailable reports whether the offering is currently masked.
func (u *unavailableOfferings) IsUnavailable(instanceType, zone string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	expiry, ok := u.entries[offeringKey(instanceType, zone)]
	return ok && u.now().Before(expiry)
}
