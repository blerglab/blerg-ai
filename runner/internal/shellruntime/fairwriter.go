package shellruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ErrWriteExpired reports a frame that was still queued at its start bound.
var ErrWriteExpired = errors.New("shell writer: queued frame expired")

// fileTurnAfter is how many other frames may pass a waiting file frame while
// no control frame is queued.
const fileTurnAfter = 4

// FairWriter is the one goroutine that writes a connection. It multiplexes
// shell controls, shell bytes, ordinary agent traffic and file replies. Control
// gets priority but after four controls an ordinary/data frame gets a turn. A
// file reply goes one frame at a time and only ever yields: it holds the socket
// for a single frame, never runs ahead of a queued control, and at most two
// are held here (one writing, one waiting). All queues and waits are bounded.
type FairWriter struct {
	control, shell, ordinary chan *writeRequest
	file                     chan *writeRequest
	done                     chan struct{}
	once                     sync.Once
}
type writeRequest struct {
	mu                        sync.Mutex
	active, started, canceled bool

	ctx   context.Context
	data  []byte
	reply chan error

	// A file reply writes frame(0..frames-1); next is the writer's own cursor.
	frames, next int
	frame        func(int) []byte
}

func NewFairWriter(write func(context.Context, []byte) error) *FairWriter {
	w := &FairWriter{control: make(chan *writeRequest, 32), shell: make(chan *writeRequest, 16), ordinary: make(chan *writeRequest, 64), file: make(chan *writeRequest, 1), done: make(chan struct{})}
	go w.run(write)
	return w
}

// Pending reports bounded queue depths only, never frame contents.
func (w *FairWriter) Pending() (control, shell, ordinary int) {
	return len(w.control), len(w.shell), len(w.ordinary)
}

func (w *FairWriter) Close() { w.once.Do(func() { close(w.done) }) }
func (w *FairWriter) Send(ctx context.Context, data []byte) error {
	var frame struct{ Type, Op string }
	_ = json.Unmarshal(data, &frame)
	lane := w.ordinary
	switch frame.Type {
	case "owner_shell_output":
		lane = w.shell
	case "owner_shell_request", "owner_shell_result", "owner_shell_phase":
		lane = w.control
		if frame.Op == "input" || frame.Op == "resize" {
			lane = w.shell
		}
	}
	return w.send(ctx, lane, &writeRequest{data: data}, time.Time{})
}

// SendOrdinary sends a frame of a known lane without scanning it. A frame not
// started by startBy (when set) is dropped unwritten with ErrWriteExpired.
func (w *FairWriter) SendOrdinary(ctx context.Context, data []byte, startBy time.Time) error {
	return w.send(ctx, w.ordinary, &writeRequest{data: data}, startBy)
}

// SendFile writes frame(0) to frame(frames-1) in order. Only the first frame is
// bound by startBy; ctx ends the reply at the next frame boundary.
func (w *FairWriter) SendFile(ctx context.Context, frames int, frame func(int) []byte, startBy time.Time) error {
	return w.send(ctx, w.file, &writeRequest{frames: frames, frame: frame}, startBy)
}

func (w *FairWriter) send(ctx context.Context, lane chan *writeRequest, r *writeRequest, startBy time.Time) error {
	r.ctx, r.reply = ctx, make(chan error, 1)
	var expired <-chan time.Time
	if !startBy.IsZero() {
		timer := time.NewTimer(time.Until(startBy))
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case lane <- r:
	case <-expired:
		return ErrWriteExpired
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return ErrClosed
	}
	for {
		select {
		case err := <-r.reply:
			return err
		case <-expired:
			expired = nil
			if !r.abandonUnstarted() {
				continue
			}
			return ErrWriteExpired
		case <-ctx.Done():
			r.mu.Lock()
			active := r.active
			r.canceled = true
			r.mu.Unlock()
			if !active {
				return ctx.Err()
			}
			select {
			case err := <-r.reply:
				return err
			case <-w.done:
				return ErrClosed
			}
		case <-w.done:
			return ErrClosed
		}
	}
}

func (r *writeRequest) abandonUnstarted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return false
	}
	r.canceled = true
	return true
}

// begin claims the request for one write unless its sender has gone.
func (r *writeRequest) begin() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.canceled || r.ctx.Err() != nil {
		return false
	}
	r.active, r.started = true, true
	return true
}

// writeFileFrame writes the reply's next frame and reports whether it finished.
func (r *writeRequest) writeFileFrame(write func(context.Context, []byte) error) bool {
	if !r.begin() {
		r.reply <- r.ctx.Err()
		return true
	}
	err := write(r.ctx, r.frame(r.next))
	r.next++
	r.mu.Lock()
	r.active = false
	r.mu.Unlock()
	// A sender canceled during this frame waits on it: answer here, not at the
	// next file turn, which a closed or control-saturated writer may never give.
	if err == nil && r.next < r.frames {
		err = r.ctx.Err()
	}
	if err != nil || r.next == r.frames {
		r.reply <- err
		return true
	}
	return false
}

func (w *FairWriter) run(write func(context.Context, []byte) error) {
	burst, passed := 0, 0
	turn := false
	var file *writeRequest
	for {
		select {
		case <-w.done:
			return
		default:
		}
		if file == nil {
			select {
			case file = <-w.file:
			default:
			}
		}
		var r *writeRequest
		if file == nil || passed < fileTurnAfter || len(w.control) > 0 {
			r = w.next(&burst, &turn)
		}
		if r == nil && file != nil {
			passed = 0
			if file.writeFileFrame(write) {
				file = nil
			}
			continue
		}
		if r == nil {
			select {
			case r = <-w.control:
				burst++
			case r = <-w.shell:
				burst = 0
			case r = <-w.ordinary:
				burst = 0
			case file = <-w.file:
				continue
			case <-w.done:
				return
			}
		}
		passed++
		if !r.begin() {
			r.reply <- r.ctx.Err()
			continue
		}
		r.reply <- write(r.ctx, r.data)
	}
}

// next takes a queued control, shell or ordinary frame without waiting.
func (w *FairWriter) next(burst *int, turn *bool) *writeRequest {
	if *burst < 4 {
		select {
		case r := <-w.control:
			*burst++
			return r
		default:
		}
	}
	var r *writeRequest
	first, second := w.shell, w.ordinary
	if *turn {
		first, second = second, first
	}
	select {
	case r = <-first:
	default:
		select {
		case r = <-second:
		default:
			// The burst cap yields to shell and ordinary frames, never to a file.
			select {
			case r = <-w.control:
				*burst = 1
				return r
			default:
				return nil
			}
		}
	}
	*burst = 0
	*turn = !*turn
	return r
}
