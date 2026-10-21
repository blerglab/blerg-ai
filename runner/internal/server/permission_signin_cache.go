package server

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const permissionSignInTTL = 30 * time.Second
const maxPermissionSignIns = 1024

type permissionSignInResult struct {
	alive bool
	until time.Time
}

// permissionSignInCache is presentation only: approvals still prove liveness at core.
// Keys include the session's current grant snapshot, so a level change or revoke
// invalidates even a result fetched by another replica's earlier panel request.
type permissionSignInCache struct {
	mu       sync.Mutex
	entries  map[string]permissionSignInResult
	requests singleflight.Group
	now      func() time.Time
}

func (c *permissionSignInCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
func (c *permissionSignInCache) get(key string) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[key]
	if ok && c.clock().Before(v.until) {
		return v.alive, true
	}
	delete(c.entries, key)
	return false, false
}
func (c *permissionSignInCache) load(ctx context.Context, key string, fetch func(context.Context) (bool, bool)) bool {
	if alive, ok := c.get(key); ok {
		return alive
	}
	v, _, _ := c.requests.Do(key, func() (any, error) {
		if alive, ok := c.get(key); ok {
			return alive, nil
		}
		alive, cacheable := fetch(ctx)
		if !cacheable {
			return alive, nil
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.entries == nil {
			c.entries = make(map[string]permissionSignInResult)
		}
		now := c.clock()
		for k, v := range c.entries {
			if !now.Before(v.until) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxPermissionSignIns {
			var oldest string
			var until time.Time
			for k, v := range c.entries {
				if until.IsZero() || v.until.Before(until) {
					oldest, until = k, v.until
				}
			}
			delete(c.entries, oldest)
		}
		c.entries[key] = permissionSignInResult{alive, now.Add(permissionSignInTTL)}
		return alive, nil
	})
	alive, _ := v.(bool) // The singleflight callback always returns a bool and nil error.
	return alive
}
