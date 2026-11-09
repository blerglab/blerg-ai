package mcpgw

import (
	"context"
	"sync"
)

type boardReadScopeKey struct{}
type boardReadKey struct {
	gateway        *Gateway
	proof          Proof
	board, session string
}
type boardReadCredential struct {
	token  BoardToken
	revoke func()
}
type boardReadScope struct {
	mu     sync.Mutex
	closed bool
	tokens map[boardReadKey]boardReadCredential
}

// WithBoardReadScope shares read-only credentials within one server-owned read.
// The caller must close it after all calls finish. Every call still checks its
// tool and current session permission; credentials never cross proof boundaries.
func WithBoardReadScope(ctx context.Context) (context.Context, func()) {
	s := &boardReadScope{tokens: map[boardReadKey]boardReadCredential{}}
	return context.WithValue(ctx, boardReadScopeKey{}, s), func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return
		}
		s.closed = true
		for _, c := range s.tokens {
			c.revoke()
		}
		clear(s.tokens)
	}
}
