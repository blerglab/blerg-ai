package mcpgw

import (
	"sync"
	"time"
)

const recentGrantTTL = 30 * time.Second
const maxRecentGrants = 1024

// recentGrants caches only permission to bypass the lookup budget. Every request still
// reads the grant and checks public-route policy, so deletion and revocation remain immediate.
type recentGrants struct {
	mu     sync.Mutex
	byHash map[[32]byte]time.Time
}

func (c *recentGrants) contains(hash [32]byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.byHash[hash]
	return ok && now.Before(until)
}

func (c *recentGrants) remember(hash [32]byte, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byHash == nil {
		c.byHash = make(map[[32]byte]time.Time)
	}
	// A full cache sweeps expired entries on every remember, a refresh of a known token too, so
	// which entries survive never depends on map order.
	if len(c.byHash) >= maxRecentGrants {
		var oldest [32]byte
		var earliest time.Time
		for k, until := range c.byHash {
			if !now.Before(until) {
				delete(c.byHash, k)
				continue
			}
			if k != hash && (earliest.IsZero() || until.Before(earliest)) {
				oldest, earliest = k, until
			}
		}
		if _, ok := c.byHash[hash]; !ok && len(c.byHash) >= maxRecentGrants {
			delete(c.byHash, oldest)
		}
	}
	c.byHash[hash] = now.Add(recentGrantTTL)
}

// forget drops a token at once. Every refusal after the cache check calls it, so a token that
// was served and is then revoked or refused never again skips the lookup budget.
func (c *recentGrants) forget(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byHash, hash)
}
