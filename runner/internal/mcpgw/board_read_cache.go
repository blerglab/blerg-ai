package mcpgw

import (
	"encoding/json"
	"sync"
	"time"
)

type boardReadCacheKey struct {
	scope      boardReadKey
	tool, args string
}
type boardReadCacheEntry struct {
	raw   json.RawMessage
	until time.Time
}
type boardReadCache struct {
	mu      sync.Mutex
	entries map[boardReadCacheKey]boardReadCacheEntry
}

func (c *boardReadCache) get(key boardReadCacheKey, now time.Time) json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && now.Before(e.until) {
		return append(json.RawMessage(nil), e.raw...)
	}
	return nil
}
func (c *boardReadCache) put(key boardReadCacheKey, raw json.RawMessage, now time.Time) {
	// Error results (including not-found and authorization) are never cached.
	var result struct {
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(raw, &result) != nil || result.IsError {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[boardReadCacheKey]boardReadCacheEntry{}
	}
	for k, e := range c.entries {
		if !now.Before(e.until) {
			delete(c.entries, k)
		}
	}
	// Bound both count and bytes: this is a pagination optimization, not storage.
	if len(c.entries) >= 128 || len(raw) > 128*1024 {
		return
	}
	c.entries[key] = boardReadCacheEntry{append(json.RawMessage(nil), raw...), now.Add(2 * time.Second)}
}
