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
	return &Cache{ttl: ttl}
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
