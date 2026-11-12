package server

import "sync"

const boardDetailLimit = 10

// Rotate across candidates on subsequent runs rather than repeatedly spending
// the detail budget on the first page's unresolved cards.
type boardDetailProgress struct {
	mu     sync.Mutex
	offset map[string]int
}

func (p *boardDetailProgress) start(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.offset[key]
}
func (p *boardDetailProgress) finish(key string, start, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.offset == nil {
		p.offset = map[string]int{}
	}
	next := start + boardDetailLimit
	if total <= next {
		next = 0
	}
	p.offset[key] = next
}
func boardCodeNeedsTrail(d boardCodeAction) bool {
	return d.Column != "" || d.Tag || d.Drift == "partial_merge_preserved"
}
