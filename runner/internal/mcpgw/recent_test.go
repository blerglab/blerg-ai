package mcpgw

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"
)

func TestRecentGrantsBoundAndExpiry(t *testing.T) {
	var c recentGrants
	now := time.Now()
	hash := sha256.Sum256([]byte("example"))
	c.remember(hash, now)
	if !c.contains(hash, now) || c.contains(hash, now.Add(recentGrantTTL)) {
		t.Fatal("cache must expire at its TTL")
	}
	for i := range maxRecentGrants * 2 {
		c.remember(sha256.Sum256(fmt.Appendf(nil, "%d", i)), now)
	}
	if len(c.byHash) > maxRecentGrants {
		t.Fatal("cache exceeded bound")
	}
	c.remember(hash, now.Add(recentGrantTTL))
	if len(c.byHash) != 1 {
		t.Fatal("expired entries were retained")
	}
}
