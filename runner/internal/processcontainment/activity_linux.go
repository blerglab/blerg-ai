//go:build linux

package processcontainment

import (
	"bufio"
	"encoding/json"
	"os"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/processtelemetry"
	"golang.org/x/sys/unix"
)

const (
	// activityTick paces the helper's sweep and sends. It is the most evidence a
	// helper loses when it ends; freeze, stop and the receipt never wait for it.
	activityTick = time.Second
	// activityFinalWait bounds how long an exiting helper waits for its last evidence.
	activityFinalWait = 100 * time.Millisecond
	// activityFrameBytes is PIPE_BUF: POSIX makes a nonblocking pipe write of at most
	// this size all or nothing, which is why telemetry has a pipe of its own.
	activityFrameBytes = 4096
	activityFrameSpans = 12
)

type activityQueue struct {
	events   chan processtelemetry.Event
	done     chan struct{}
	observer *processtelemetry.Observer
	sink     func(processtelemetry.Batch)
	dropped  atomic.Uint64
}

// EnableActivity must be called before Run. The hot path is a nonblocking send
// of the report's already available PID and timestamp: no /proc reads, clocks,
// allocation, locks, callbacks, or waits are added to an exec barrier.
func (t *Tree) EnableActivity() { t.activity = newActivityQueue(processtelemetry.ReadImage) }

func newActivityQueue(read func(int) (string, string)) *activityQueue {
	q := &activityQueue{events: make(chan processtelemetry.Event, processtelemetry.QueueSize), done: make(chan struct{}), observer: processtelemetry.NewObserver(read)}
	q.observer.Identity = processtelemetry.ProcessIdentity
	go q.run()
	return q
}

// run owns every /proc read the observer makes; only telemetry can wait here.
func (q *activityQueue) run() {
	defer close(q.done)
	tick := time.NewTicker(activityTick)
	defer tick.Stop()
	swept := uint64(0)
	for {
		select {
		case event, ok := <-q.events:
			if !ok {
				q.flush()
				return
			}
			q.observer.Apply(event)
		case <-tick.C:
			dropped := q.dropped.Load()
			q.observer.Sweep(dropped != swept)
			swept = dropped
			q.flush()
		}
	}
}

// flush sends what the observer holds. Nothing here waits: Drain gives up on a busy
// observer and the sink is nonblocking.
func (q *activityQueue) flush() {
	if q.sink == nil {
		return
	}
	if b := q.drain(); len(b.Spans) > 0 || b.Dropped > 0 {
		q.sink(b)
	}
}
func (q *activityQueue) drain() processtelemetry.Batch {
	b := q.observer.Drain(time.Now())
	b.Dropped += q.dropped.Swap(0)
	return b
}

// close ends the queue from the tracer. It waits for nothing: the tree's end, and
// the receipt that proves it, must not depend on a /proc read still in progress.
func (q *activityQueue) close() { close(q.events) }
func (q *activityQueue) publish(kind processtelemetry.Kind, pid, parent int, at time.Time) {
	if q == nil {
		return
	}
	select {
	case q.events <- processtelemetry.Event{Kind: kind, PID: pid, Parent: parent, At: at}:
	default:
		q.dropped.Add(1)
	}
}

// activityPipe writes telemetry to its own descriptor, in frames small enough to be
// atomic. A full pipe drops the frame and counts it; it can neither stall the
// control stream, which carries the ledger and every receipt, nor tear a frame.
type activityPipe struct {
	fd      int
	dropped atomic.Uint64
}

func (a *activityPipe) send(b processtelemetry.Batch) {
	dropped := b.Dropped + a.dropped.Swap(0)
	for first := true; first || len(b.Spans) > 0; first = false {
		frame := processtelemetry.Batch{Spans: b.Spans[:min(len(b.Spans), activityFrameSpans)], Dropped: dropped}
		b.Spans = b.Spans[len(frame.Spans):]
		raw, err := json.Marshal(frame)
		if err == nil && len(raw) < activityFrameBytes {
			var n int
			if n, err = unix.Write(a.fd, append(raw, '\n')); err == nil && n == len(raw)+1 {
				dropped = 0
				continue
			}
		}
		dropped += uint64(len(frame.Spans))
	}
	a.dropped.Add(dropped)
}

func (t *Tree) ProcessActivity() processtelemetry.Batch {
	if t.activity == nil {
		return processtelemetry.Batch{}
	}
	return t.activity.drain()
}

// FlushActivity is the last send, made after the receipt so it costs the receipt
// nothing. The consumer flushes when the queue closes; a helper exiting first would
// lose that, so this gives it a moment, bounded because a /proc read can stall.
func (t *Tree) FlushActivity() {
	if t.activity == nil {
		return
	}
	select {
	case <-t.activity.done:
	case <-time.After(activityFinalWait):
	}
	t.activity.flush()
}

// ProcessActivity hands over what helpers have pushed down their activity pipes.
// Telemetry never blocks a control receipt or affects containment decisions.
func (g *Group) ProcessActivity() processtelemetry.Batch { return g.processActivity(nil) }

// ProcessActivityForRoots drains only this session's admitted helper roots.
// Other sessions retain their evidence even when sharing a workspace group.
func (g *Group) ProcessActivityForRoots(roots []processtelemetry.SampleRoot) processtelemetry.Batch {
	allowed := make(map[int]string, len(roots))
	for _, root := range roots {
		allowed[root.PID] = root.Identity
	}
	return g.processActivity(allowed)
}
func (g *Group) processActivity(allowed map[int]string) processtelemetry.Batch {
	if !g.mu.TryLock() {
		return processtelemetry.Batch{}
	}
	peers := append([]*peer(nil), g.peers...)
	g.mu.Unlock()
	var result processtelemetry.Batch
	for _, p := range peers {
		if allowed != nil && (p.activityIdentity == "" || allowed[int(p.pid.Load())] != p.activityIdentity) {
			continue
		}
		if !p.activityMu.TryLock() {
			continue
		}
		for _, s := range p.activity.Spans {
			s.Root = int(p.pid.Load())
			if len(result.Spans) < processtelemetry.MaxSpans {
				result.Spans = append(result.Spans, s)
			} else {
				result.Dropped++
			}
		}
		result.Dropped += p.activity.Dropped
		p.activity = processtelemetry.Batch{}
		p.activityMu.Unlock()

	}
	return result
}

// readActivity accumulates one helper's pushed frames until the helper is gone.
// The tree never holds this pipe, so its end is the helper's end.
func (p *peer) readActivity(r *os.File) {
	defer func() { _ = r.Close() }()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, activityFrameBytes), activityFrameBytes)
	for scanner.Scan() {
		var b processtelemetry.Batch
		if json.Unmarshal(scanner.Bytes(), &b) != nil {
			continue
		}
		p.activityMu.Lock()
		for _, s := range b.Spans {
			if len(p.activity.Spans) < processtelemetry.MaxSpans {
				p.activity.Spans = append(p.activity.Spans, s)
			} else {
				p.activity.Dropped++
			}
		}
		p.activity.Dropped += b.Dropped
		p.activityMu.Unlock()
	}
}
