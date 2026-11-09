package server

import (
	"context"
	"sync"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

type mcpAuthorityDecision struct {
	cause           string
	expiresAt       time.Time
	intervalSeconds int
}

type mcpAuthorityResult struct {
	scope    ld.Scope
	sequence int64
	decision mcpAuthorityDecision
	until    time.Time
	done     chan struct{}
	ready    bool
}

// Only subscriber read decisions are shared. No credential, provider call,
// normalized row or owner/workspace membership is cached here.
type mcpAuthorityCache struct {
	mu      sync.Mutex
	entries map[string]*mcpAuthorityResult
	now     func() time.Time // immutable clock seam; nil uses the real clock
}

func (c *mcpAuthorityCache) time() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
func (c *mcpAuthorityCache) check(ctx context.Context, scope ld.Scope, sequence int64, fetch func() mcpAuthorityDecision) mcpAuthorityDecision {
	for {
		if ctx.Err() != nil {
			return mcpAuthorityDecision{cause: "authority_unavailable"}
		}
		c.mu.Lock()
		now := c.time()
		entry := c.entries[scope.Binding]
		if entry != nil && entry.scope == scope && entry.sequence == sequence {
			if !entry.ready {
				done := entry.done
				c.mu.Unlock()
				select {
				case <-ctx.Done():
					return mcpAuthorityDecision{cause: "authority_unavailable"}
				case <-done:
					continue
				}
			}
			if now.Before(entry.until) {
				decision := entry.decision
				c.mu.Unlock()
				return decision
			}
		}
		if c.entries == nil {
			c.entries = make(map[string]*mcpAuthorityResult)
		}
		if entry == nil && len(c.entries) >= 4096 {
			for key, old := range c.entries {
				if old.ready && !now.Before(old.until) {
					delete(c.entries, key)
				}
			}
			if len(c.entries) >= 4096 {
				c.mu.Unlock()
				return fetch()
			}
		}
		entry = &mcpAuthorityResult{scope: scope, sequence: sequence, done: make(chan struct{})}
		c.entries[scope.Binding] = entry
		c.mu.Unlock()
		decision := fetch()
		c.mu.Lock()
		entry.decision = decision
		entry.until = c.time().Add(time.Second)
		entry.ready = true
		close(entry.done)
		c.mu.Unlock()
		return decision
	}
}
