package shellruntime

import (
	"context"
	"encoding/json"
	"sync"
)

// FairWriter multiplexes shell controls, shell bytes and ordinary agent/file
// traffic over one existing connection. Control gets priority but after four
// controls an ordinary/data frame gets a turn. All queues and waits are bounded.
type FairWriter struct {
	control, shell, ordinary chan *writeRequest
	done                     chan struct{}
	once                     sync.Once
}
type writeRequest struct {
	mu               sync.Mutex
	active, canceled bool

	ctx   context.Context
	data  []byte
	reply chan error
}

func NewFairWriter(write func(context.Context, []byte) error) *FairWriter {
	w := &FairWriter{control: make(chan *writeRequest, 32), shell: make(chan *writeRequest, 16), ordinary: make(chan *writeRequest, 64), done: make(chan struct{})}
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
	return w.send(ctx, data, lane)
}

// SendOrdinary avoids scanning a large trusted file payload merely to classify
// its already-known lane. It still uses exactly the same bounded socket writer.
func (w *FairWriter) SendOrdinary(ctx context.Context, data []byte) error {
	return w.send(ctx, data, w.ordinary)
}
func (w *FairWriter) send(ctx context.Context, data []byte, lane chan *writeRequest) error {
	r := &writeRequest{ctx: ctx, data: data, reply: make(chan error, 1)}
	select {
	case lane <- r:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return ErrClosed
	}
	select {
	case err := <-r.reply:
		return err
	case <-ctx.Done():
		r.mu.Lock()
		active := r.active
		r.canceled = true
		r.mu.Unlock()
		if active {
			return <-r.reply
		}
		return ctx.Err()
	case <-w.done:
		return ErrClosed
	}
}
func (w *FairWriter) run(write func(context.Context, []byte) error) {
	burst := 0
	turn := false
	for {
		var r *writeRequest
		found := false
		if burst < 4 {
			select {
			case r = <-w.control:
				found = true
				burst++
			default:
			}
		}
		if !found {
			if turn {
				select {
				case r = <-w.ordinary:
					found = true
				default:
				}
			} else {
				select {
				case r = <-w.shell:
					found = true
				default:
				}
			}
			if !found {
				select {
				case r = <-w.ordinary:
					found = true
				case r = <-w.shell:
					found = true
				default:
				}
			}
			if found {
				burst = 0
				turn = !turn
			}
		}
		if !found {
			select {
			case r = <-w.control:
				burst++
			case r = <-w.shell:
				burst = 0
			case r = <-w.ordinary:
				burst = 0
			case <-w.done:
				return
			}
		}
		r.mu.Lock()
		if r.canceled || r.ctx.Err() != nil {
			r.mu.Unlock()
			r.reply <- r.ctx.Err()
			continue
		}
		r.active = true
		r.mu.Unlock()
		r.reply <- write(r.ctx, r.data)
	}
}
