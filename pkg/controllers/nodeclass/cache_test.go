package nodeclass

import (
	"testing"
	"time"
)

func TestImageCache_HitMissExpiry(t *testing.T) {
	c := newImageCache(5 * time.Minute)
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }

	if _, ok := c.get("fr-par-1", "ubuntu_jammy"); ok {
		t.Fatal("expected miss on cold cache")
	}

	c.put("fr-par-1", "ubuntu_jammy", "img-uuid")
	got, ok := c.get("fr-par-1", "ubuntu_jammy")
	if !ok || got != "img-uuid" {
		t.Fatalf("expected hit imgID=img-uuid, got=%q ok=%v", got, ok)
	}

	// Advance clock past TTL.
	now = now.Add(5*time.Minute + time.Second)
	if _, ok := c.get("fr-par-1", "ubuntu_jammy"); ok {
		t.Fatal("expected miss after TTL expiry")
	}
}

func TestImageCache_KeyIsolation(t *testing.T) {
	c := newImageCache(5 * time.Minute)
	c.put("fr-par-1", "ubuntu_jammy", "a")
	c.put("nl-ams-1", "ubuntu_jammy", "b")
	c.put("fr-par-1", "debian_bookworm", "c")

	for _, tc := range []struct{ zone, label, want string }{
		{"fr-par-1", "ubuntu_jammy", "a"},
		{"nl-ams-1", "ubuntu_jammy", "b"},
		{"fr-par-1", "debian_bookworm", "c"},
	} {
		got, ok := c.get(tc.zone, tc.label)
		if !ok || got != tc.want {
			t.Errorf("(%s,%s): got=%q ok=%v want=%q", tc.zone, tc.label, got, ok, tc.want)
		}
	}
}

func TestImageCache_Invalidate(t *testing.T) {
	c := newImageCache(5 * time.Minute)
	c.put("fr-par-1", "ubuntu_jammy", "a")
	c.invalidate("fr-par-1", "ubuntu_jammy")
	if _, ok := c.get("fr-par-1", "ubuntu_jammy"); ok {
		t.Fatal("expected miss after invalidate")
	}
}
