package server

import (
	"sync"
	"time"
)

// Keep at most one pending or running event scan per workspace. Turn end
// independently guarantees reconciliation, including edits during an event scan.
type boardInvariantQueue struct {
	mu      sync.Mutex
	pending map[string]bool
}

func (q *boardInvariantQueue) enqueue(key string, after func(time.Duration, func()), run func()) {
	q.mu.Lock()
	if q.pending == nil {
		q.pending = map[string]bool{}
	}
	if q.pending[key] {
		q.mu.Unlock()
		return
	}
	q.pending[key] = true
	q.mu.Unlock()
	after(3*time.Second, func() { defer func() { q.mu.Lock(); delete(q.pending, key); q.mu.Unlock() }(); run() })
}
