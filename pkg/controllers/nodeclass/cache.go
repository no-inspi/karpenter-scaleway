package nodeclass

import (
	"sync"
	"time"
)

// imageCache is a tiny TTL-keyed cache for Marketplace lookups. The
// reconciler hits the Marketplace on every reconcile of every NodeClass;
// since the answer for a given (zone, label) pair is stable for hours, a
// 5-minute TTL is enough to dominate the steady-state and surface real
// changes (image relabeled, retired) within tolerable latency.
type imageCache struct {
	ttl   time.Duration
	now   func() time.Time
	mu    sync.RWMutex
	items map[string]cacheEntry
}

type cacheEntry struct {
	imageID   string
	expiresAt time.Time
}

func newImageCache(ttl time.Duration) *imageCache {
	return &imageCache{
		ttl:   ttl,
		now:   time.Now,
		items: map[string]cacheEntry{},
	}
}

// get returns the cached imageID for (zone, label), or false if absent or
// expired. Reads use a shared lock so concurrent reconciles don't serialise.
func (c *imageCache) get(zone, label string) (string, bool) {
	key := cacheKey(zone, label)
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[key]
	if !ok || c.now().After(e.expiresAt) {
		return "", false
	}
	return e.imageID, true
}

// put stores imageID for (zone, label) and resets the TTL clock.
func (c *imageCache) put(zone, label, imageID string) {
	key := cacheKey(zone, label)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheEntry{imageID: imageID, expiresAt: c.now().Add(c.ttl)}
}

// invalidate drops the entry — used when the reconciler observes a
// validation failure that should not be papered over by a stale hit.
func (c *imageCache) invalidate(zone, label string) {
	key := cacheKey(zone, label)
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

func cacheKey(zone, label string) string { return zone + "/" + label }
