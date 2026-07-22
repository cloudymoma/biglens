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
