package main

import (
	"testing"
	"time"
)

// The janitor must evict expired entries on its own: a key that is set once
// and never read again would otherwise stay in the map forever, since Get
// only evicts the key it touches.
func TestCacheJanitorSweepsExpiredEntries(t *testing.T) {
	c := NewCache(10 * time.Millisecond)
	c.Set("queried-once", 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := c.store.Load("queried-once"); !ok {
			return // swept without any Get on the key
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expired entry was never swept by the janitor")
}

// Gas Pulse keeps 1h and 24h entries in the shared 10-minute cache, so a
// per-entry TTL must override the instance TTL in both directions.
func TestCacheSetWithTTLOverridesInstanceTTL(t *testing.T) {
	c := NewCache(time.Hour)
	c.SetWithTTL("short", 1, 20*time.Millisecond)
	c.Set("default", 2)

	time.Sleep(40 * time.Millisecond)

	if _, ok := c.Get("short"); ok {
		t.Error("entry set with a 20ms TTL is still served after 40ms")
	}
	if v, ok := c.Get("default"); !ok || v != 2 {
		t.Errorf("Set entry under the 1h instance TTL: got (%v, %v), want (2, true)", v, ok)
	}
}

func TestCacheSetWithTTLOutlivesInstanceTTL(t *testing.T) {
	c := NewCache(20 * time.Millisecond)
	c.SetWithTTL("long", 1, time.Hour)

	time.Sleep(40 * time.Millisecond)

	if _, ok := c.Get("long"); !ok {
		t.Error("entry set with a 1h TTL expired with the 20ms instance TTL")
	}
}
