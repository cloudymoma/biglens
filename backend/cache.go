package main

import (
	"sync"
	"time"
)

type cacheEntry struct {
	data      any
	expiresAt time.Time
}

type Cache struct {
	store sync.Map
	ttl   time.Duration
}

func NewCache(ttl time.Duration) *Cache {
	c := &Cache{ttl: ttl}
	go c.janitor()
	return c
}

// janitor periodically sweeps expired entries. Get only evicts the key it
// touches, so without this, keys built from dynamic parameters (search terms,
// date ranges, filter combinations) that are never requested again would
// accumulate forever.
func (c *Cache) janitor() {
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		c.store.Range(func(key, val any) bool {
			if now.After(val.(*cacheEntry).expiresAt) {
				c.store.Delete(key)
			}
			return true
		})
	}
}

func (c *Cache) Get(key string) (any, bool) {
	val, ok := c.store.Load(key)
	if !ok {
		return nil, false
	}
	entry := val.(*cacheEntry)
	if time.Now().After(entry.expiresAt) {
		c.store.Delete(key)
		return nil, false
	}
	return entry.data, true
}

func (c *Cache) Set(key string, data any) {
	c.store.Store(key, &cacheEntry{
		data:      data,
		expiresAt: time.Now().Add(c.ttl),
	})
}

func (c *Cache) Delete(key string) {
	c.store.Delete(key)
}
