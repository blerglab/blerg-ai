package ownershell

import (
	"sync"
	"time"
)

// OutputWindow is how many output bytes a shell stage may hold unconsumed. The
// daemon grants its helper this much credit, so its queue can never overflow.
const OutputWindow = 64 << 10

// ServerOutputWindow is how many output bytes a runtime may send the server for
// one shell before the server returns credit. The server returns it only as the
// manager finishes with each frame (handed to the browser queue, or dropped
// while detached), so a slow but progressing browser throttles the runtime
// end to end instead of overflowing a server queue.
const ServerOutputWindow = 4 * OutputWindow

// OutputCredit is implemented by a Supervisor whose runtime sends output under
// a credit window. Consumed reports n bytes of the shell's output as finished.
type OutputCredit interface {
	Consumed(SignedGrant, int)
}

// OutputStall is how long a consumer may refuse all output before it counts as
// dead. Until then every stage waits, so pressure reaches the program's write.
const OutputStall = 5 * time.Second

// OutputPipe is a bounded FIFO of terminal bytes. Frame boundaries carry no
// meaning in a terminal stream, so the reader coalesces whatever is queued into
// one frame of at most the reader's limit. Write never blocks: a producer that must not
// stall (a socket reader also carrying control replies) learns about overflow
// from the result and decides what that means. Memory is capped at limit bytes.
type OutputPipe struct {
	mu      sync.Mutex
	buf     []byte
	limit   int
	closed  bool
	aborted bool
	ready   chan struct{} // capacity one; signaled on every state change
	gone    chan struct{} // closed by Abort
}

func NewOutputPipe(limit int) *OutputPipe {
	return &OutputPipe{limit: limit, ready: make(chan struct{}, 1), gone: make(chan struct{})}
}

func (p *OutputPipe) signal() {
	select {
	case p.ready <- struct{}{}:
	default:
	}
}

// Write appends b. It returns false, keeping nothing, when the pipe was closed
// or b would take it past its limit.
func (p *OutputPipe) Write(b []byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.aborted || len(p.buf)+len(b) > p.limit {
		return false
	}
	p.buf = append(p.buf, b...)
	p.signal()
	return true
}

// Close marks the end of the stream. Queued bytes remain readable.
func (p *OutputPipe) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.signal()
}

// Abort discards queued bytes and wakes every waiter.
func (p *OutputPipe) Abort() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.aborted {
		p.aborted = true
		p.buf = nil
		close(p.gone)
	}
	p.signal()
}

// Aborted is closed once Abort runs.
func (p *OutputPipe) Aborted() <-chan struct{} { return p.gone }

// Len reports queued bytes.
func (p *OutputPipe) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.buf)
}

// Read waits for queued bytes and returns up to limit of them as one new slice.
// It returns false at the end of a closed stream, after Abort, or when stop closes.
func (p *OutputPipe) Read(limit int, stop <-chan struct{}) ([]byte, bool) {
	for {
		p.mu.Lock()
		if p.aborted {
			p.mu.Unlock()
			return nil, false
		}
		if len(p.buf) > 0 {
			n := min(limit, len(p.buf))
			chunk := append([]byte(nil), p.buf[:n]...)
			// Shift instead of reslicing so the backing array never grows past limit.
			p.buf = p.buf[:copy(p.buf, p.buf[n:])]
			if len(p.buf) > 0 || p.closed {
				p.signal()
			}
			p.mu.Unlock()
			return chunk, true
		}
		if p.closed {
			p.mu.Unlock()
			return nil, false
		}
		p.mu.Unlock()
		select {
		case <-p.ready:
		case <-p.gone:
		case <-stop:
			return nil, false
		}
	}
}
