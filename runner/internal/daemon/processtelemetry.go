package daemon

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/processtelemetry"
)

// Every row stays in the transcript's tables for good, so a process that lives for
// an hour has to cost a handful of rows. Sampling stays fine; persistence is coarse.
const processFlushEvery = 30 * time.Second

type processCall struct {
	start, end time.Time
	task       bool
	eligible   bool
}
type processRoot struct {
	sample processtelemetry.SampleRoot
	call   string
}
type toolProcessMonitor struct {
	mu       sync.Mutex
	pollMu   sync.Mutex
	calls    map[string]processCall
	roots    map[int]processRoot
	branches map[[2]int]string
	sampler  *processtelemetry.Sampler
	source   func([]processtelemetry.SampleRoot) processtelemetry.Batch
	emit     func(agent.Event)

	pending []processtelemetry.Span
	dropped uint64
	flushed time.Time
}

func newToolProcessMonitor(source func([]processtelemetry.SampleRoot) processtelemetry.Batch, emit func(agent.Event)) *toolProcessMonitor {
	return &toolProcessMonitor{calls: map[string]processCall{}, roots: map[int]processRoot{}, branches: map[[2]int]string{}, sampler: processtelemetry.NewSampler(), source: source, emit: emit, flushed: time.Now()}
}
func (m *toolProcessMonitor) observe(ev agent.Event) {
	if ev.Kind == "process_activity" {
		return
	}
	if ev.Kind == "turn_done" {
		// The turn's metrics read rows up to turn_done, so what is already collected
		// goes first. This runs in the event callback: it never samples.
		m.flush()
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch p := ev.Payload.(type) {
	case agent.ToolCallPayload:
		if len(m.calls) >= 256 {
			for id, c := range m.calls {
				if !c.end.IsZero() && !c.task {
					delete(m.calls, id)
				}
			}
		}
		if len(m.calls) < 256 {
			name := strings.ToLower(p.Tool)
			m.calls[p.CallID] = processCall{start: ev.Ts, eligible: !strings.Contains(name, "wait") && name != "sleep"}
		}
	case agent.ToolResultPayload:
		c, ok := m.calls[p.CallID]
		if ok {
			c.end = ev.Ts
			m.calls[p.CallID] = c
		}
	case agent.BackgroundTaskStartedPayload:
		c, ok := m.calls[p.CallID]
		if ok {
			c.task = true
			c.end = time.Time{}
			m.calls[p.CallID] = c
		}
	case agent.BackgroundTaskDonePayload:
		c, ok := m.calls[p.CallID]
		if ok {
			c.task = false
			c.end = ev.Ts
			m.calls[p.CallID] = c
		}
	}
}
func (m *toolProcessMonitor) candidate(start, end time.Time) string {
	match := ""
	for id, c := range m.calls {
		if !c.eligible || c.start.After(end) || !c.end.IsZero() && c.end.Before(start) {
			continue
		}
		if match != "" {
			return ""
		}
		match = id
	}
	return match
}
func (m *toolProcessMonitor) started(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	identity := processtelemetry.ProcessIdentity(pid)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.roots) >= 128 {
		for id, root := range m.roots {
			if processtelemetry.ProcessIdentity(id) != root.sample.Identity {
				delete(m.roots, id)
			}
		}
	}
	if len(m.roots) < 128 {
		m.roots[pid] = processRoot{sample: processtelemetry.SampleRoot{PID: pid, Identity: identity}, call: m.candidate(time.Now(), time.Now())}
	}
}
func (m *toolProcessMonitor) poll() {
	if !m.pollMu.TryLock() {
		return
	}
	defer m.pollMu.Unlock()

	m.mu.Lock()
	roots := make([]processtelemetry.SampleRoot, 0, len(m.roots))
	for _, r := range m.roots {
		roots = append(roots, r.sample)
	}
	m.mu.Unlock()
	var batch processtelemetry.Batch
	if m.source != nil {
		batch = m.source(roots)
	} else {
		batch = m.sampler.Sample(roots, time.Now())
	}
	m.mu.Lock()
	filtered := batch.Spans[:0]
	for _, span := range batch.Spans {
		if _, ok := m.roots[span.Root]; ok {
			filtered = append(filtered, span)
		}
	}
	batch.Spans = filtered
	for i := range batch.Spans {
		s := &batch.Spans[i]
		key := [2]int{s.Root, s.Branch}
		id := m.roots[s.Root].call
		if c, ok := m.calls[id]; !ok || c.start.After(s.End) || !c.end.IsZero() && c.end.Before(s.Start) {
			id = ""
		}
		if id == "" {
			id = m.branches[key]
			if c, ok := m.calls[id]; !ok || !c.end.IsZero() && c.end.Before(s.Start) {
				id = ""
			}
		}
		if id == "" {
			id = m.candidate(s.Start, s.End)
		}
		if id != "" && len(m.branches) < processtelemetry.MaxProcesses {
			m.branches[key] = id
		}
		s.CallID = id
		// Kernel identifiers remain private to correlation, never canonical events.
		s.PID, s.Root, s.Branch = 0, 0, 0
	}
	m.mergeLocked(batch)
	m.mu.Unlock()
}

// merge folds one poll into the pending row. Spans of one call and program that
// touch become one: consumers read intervals, never process counts.
func (m *toolProcessMonitor) merge(batch processtelemetry.Batch) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mergeLocked(batch)
}
func (m *toolProcessMonitor) mergeLocked(batch processtelemetry.Batch) {
	m.dropped += batch.Dropped
next:
	for _, s := range batch.Spans {
		for i := range m.pending {
			p := &m.pending[i]
			if p.CallID == s.CallID && p.Program == s.Program && p.Class == s.Class && p.Basis == s.Basis && !s.Start.After(p.End) && !p.Start.After(s.End) {
				if s.Start.Before(p.Start) {
					p.Start = s.Start
				}
				if s.End.After(p.End) {
					p.End = s.End
				}
				continue next
			}
		}
		if len(m.pending) >= processtelemetry.MaxSpans {
			m.dropped++
			continue
		}
		m.pending = append(m.pending, s)
	}
}
func (m *toolProcessMonitor) flush() {
	m.mu.Lock()
	batch := processtelemetry.Batch{Spans: m.pending, Dropped: m.dropped}
	m.pending, m.dropped = nil, 0
	m.mu.Unlock()
	if len(batch.Spans) > 0 || batch.Dropped > 0 {
		m.emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "process_activity", Payload: batch})
	}
}
func (m *toolProcessMonitor) tick(now time.Time) {
	m.poll()
	m.mu.Lock()
	due := now.Sub(m.flushed) >= processFlushEvery
	if due {
		m.flushed = now
	}
	m.mu.Unlock()
	if due {
		m.flush()
	}
}
func (m *toolProcessMonitor) run(ctx context.Context) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			m.poll()
			m.flush()
			return
		case now := <-tick.C:
			m.tick(now)
		}
	}
}
