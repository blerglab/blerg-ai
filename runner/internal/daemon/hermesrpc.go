package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

type hermesFrame struct {
	questionDone  string
	questionToken string
	wireBytes     int
	ID            json.RawMessage `json:"id"`
	Method        string          `json:"method"`
	Params        json.RawMessage `json:"params"`
	Result        json.RawMessage `json:"result"`
	Error         json.RawMessage `json:"error"`
}
type hermesRPC struct {
	writeOnce sync.Once
	writes    chan hermesWrite

	mu      sync.Mutex
	next    int
	pending map[string]chan hermesFrame
	input   io.WriteCloser
	wake    chan struct{}
	done    chan struct{}
	backlog []hermesFrame
	bytes   int
	failure string
	stop    func()
}
type hermesWrite struct {
	data []byte
	ctx  context.Context
	done chan error
}

type hermesRejection struct{}

func (hermesRejection) Error() string { return "Hermes rejected the request" }
func newHermesRPC(in io.WriteCloser, out io.Reader, stop func()) *hermesRPC {
	r := &hermesRPC{input: in, pending: map[string]chan hermesFrame{}, wake: make(chan struct{}, 1), done: make(chan struct{}), stop: stop}
	go r.read(out)
	return r
}
func (r *hermesRPC) read(out io.Reader) {
	defer close(r.done)
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 65536), 8<<20)
	for sc.Scan() {
		var frame hermesFrame
		if json.Unmarshal(sc.Bytes(), &frame) != nil {
			continue
		}
		r.mu.Lock()
		if frame.Method == "" && len(frame.ID) > 0 {
			ch := r.pending[string(frame.ID)]
			if len(frame.Error) > 0 && string(frame.Error) != "null" {
				frame.Error = json.RawMessage(`{}`)
			}
			r.mu.Unlock()
			if ch != nil {
				select {
				case ch <- frame:
				default:
				}
			}
			continue
		}
		if len(frame.Method) > 256 || len(frame.ID) > 256 {
			r.failure = "Hermes sent an invalid protocol envelope"
			r.mu.Unlock()
			r.stop()
			_ = r.input.Close()
			return
		}
		size := len(sc.Bytes())
		frame.wireBytes = size
		if len(r.backlog) >= 4096 || r.bytes+size > 64<<20 {
			r.failure = "Hermes event backlog exceeded its safety limit"
			r.mu.Unlock()
			r.stop()
			return
		}
		r.backlog = append(r.backlog, frame)
		r.bytes += size
		r.mu.Unlock()
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
	r.stop()
}
func (r *hermesRPC) write(parent context.Context, v any) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	return r.writeContext(ctx, v)
}
func (r *hermesRPC) writeContext(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil || len(data) > 8<<20 {
		return errors.New("hermes request exceeded protocol limits")
	}
	data = append(data, '\n')
	r.writeOnce.Do(func() { r.writes = make(chan hermesWrite, 16); go r.writeLoop() })
	item := hermesWrite{data: data, ctx: ctx, done: make(chan error, 1)}
	select {
	case r.writes <- item:
	case <-ctx.Done():
		r.stop()
		_ = r.input.Close()
		return ctx.Err()
	case <-r.done:
		return errors.New("hermes connection closed")
	}
	select {
	case err := <-item.done:
		if err != nil {
			r.stop()
			_ = r.input.Close()
		}
		return err
	case <-ctx.Done():
		r.stop()
		_ = r.input.Close()
		return ctx.Err()
	case <-r.done:
		_ = r.input.Close()
		return errors.New("hermes connection closed")
	}
}
func (r *hermesRPC) writeLoop() {
	for {
		select {
		case <-r.done:
			return
		case item := <-r.writes:
			if item.ctx.Err() != nil {
				item.done <- item.ctx.Err()
				continue
			}
			if f, ok := r.input.(*os.File); ok {
				deadline, _ := item.ctx.Deadline()
				_ = f.SetWriteDeadline(deadline)
			}
			var err error
			for len(item.data) > 0 {
				var n int
				n, err = r.input.Write(item.data)
				item.data = item.data[n:]
				if err != nil {
					break
				}
				if n == 0 {
					err = io.ErrShortWrite
					break
				}
			}
			item.done <- err
		}
	}
}
func (r *hermesRPC) call(ctx context.Context, method string, p any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	r.mu.Lock()
	if len(r.pending) >= 32 {
		r.mu.Unlock()
		return nil, errors.New("hermes request concurrency limit reached")
	}
	r.next++
	id := "blerg-" + strconv.Itoa(r.next)
	encoded, _ := json.Marshal(id)
	ch := make(chan hermesFrame, 1)
	r.pending[string(encoded)] = ch
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, string(encoded)); r.mu.Unlock() }()
	decode := func(f hermesFrame) (json.RawMessage, error) {
		if len(f.Error) > 0 && string(f.Error) != "null" {
			return nil, hermesRejection{}
		}
		return f.Result, nil
	}
	if err := r.writeContext(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": p}); err != nil {
		select {
		case f := <-ch:
			return decode(f)
		default:
			return nil, errors.New("hermes connection closed")
		}
	}
	select {
	case f := <-ch:
		return decode(f)
	case <-ctx.Done():
		select {
		case f := <-ch:
			return decode(f)
		default:
		}
		if method == "mcp.servers.status" {
			return nil, ctx.Err()
		} // cached read only; late replies cannot execute work.
		r.stop()
		return nil, errors.New("hermes request could not be confirmed; the process was stopped")
	case <-r.done:
		// The reader routes replies before closing done. A final acknowledgment
		// remains authoritative when EOF and that reply become ready together.
		select {
		case f := <-ch:
			return decode(f)
		default:
			return nil, errors.New("hermes connection closed before acknowledgment")
		}
	}

}
func (r *hermesRPC) pop() (hermesFrame, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.backlog) == 0 {
		return hermesFrame{}, false
	}
	f := r.backlog[0]
	r.backlog[0] = hermesFrame{}
	r.backlog = r.backlog[1:]
	r.bytes -= f.wireBytes
	if len(r.backlog) > 0 {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
	return f, true
}
