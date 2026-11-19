package ownershell

import "sync"

// StreamCredit counts committed data bytes, not sequence counters or skipped
// offsets. A duplicate ack or a large explicit eviction gap cannot mint credit.
// A new access epoch uses a new instance anchored at the last received offset.
type StreamCredit struct {
	mu                      sync.Mutex
	window, available       uint64
	base, queued, sent, ack uint64
}

func NewStreamCredit(window, offset uint64) *StreamCredit {
	if window == 0 || window > 1<<20 {
		window = OutputWindow
	}
	return &StreamCredit{window: window, available: window, base: offset, queued: offset, sent: offset, ack: offset}
}
func (c *StreamCredit) Available() uint64 { c.mu.Lock(); defer c.mu.Unlock(); return c.available }

// Reserve precedes publication to the bounded output queue. Gaps wait until all
// earlier frames are acknowledged, giving skipped bytes an unambiguous boundary.
func (c *StreamCredit) Reserve(offset uint64, size int, gap bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if size <= 0 || size > MaxFrame || uint64(size) > c.available || uint64(size) > ^uint64(0)-offset {
		return false
	}
	if offset != c.queued {
		if !gap || offset < c.queued || c.available != c.window || c.sent != c.ack || c.queued != c.ack {
			return false
		}
		c.base = offset
	}
	c.queued = offset + uint64(size)
	c.available -= uint64(size)
	return true
}

// Committed is called at the final epoch-checked transport handoff. Merely
// reserving a queue slot must not make an offset acknowledgeable.
func (c *StreamCredit) Committed(offset uint64, size int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if size <= 0 || size > MaxFrame || uint64(size) > ^uint64(0)-offset || offset+uint64(size) > c.queued {
		return ErrDenied
	}
	if offset != c.sent && (offset != c.base || c.sent != c.ack || offset <= c.sent) {
		return ErrDenied
	}
	c.sent = offset + uint64(size)
	return nil
}

func (c *StreamCredit) Acknowledge(offset uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if offset < c.ack || offset > c.sent || offset < c.base && offset != c.ack {
		return ErrDenied
	}
	released := uint64(0)
	if offset >= c.base {
		released = offset - max(c.ack, c.base)
	}
	c.ack = offset
	c.available = min(c.window, c.available+released)
	return nil
}

// Acknowledged is the contiguous capacity receipt at this transport stage.
// It does not imply server retention at a later forwarding stage.
func (c *StreamCredit) Acknowledged() uint64 { c.mu.Lock(); defer c.mu.Unlock(); return c.ack }
