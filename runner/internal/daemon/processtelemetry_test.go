package daemon

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/processtelemetry"
	"strings"
	"testing"
	"time"
)

func TestProcessMonitorOwnsOnlyUnambiguousCallsAndStripsKernelIDs(t *testing.T) {
	at := time.Now()
	var emitted agent.Event
	source := func(roots []processtelemetry.SampleRoot) processtelemetry.Batch {
		if len(roots) != 1 || roots[0].PID != 7 {
			t.Fatalf("wrong admitted roots: %v", roots)
		}
		return processtelemetry.Batch{Spans: []processtelemetry.Span{{PID: 8, Root: 7, Branch: 8, Program: "go-test", Class: "tests", Start: at, End: at.Add(time.Second), Approximate: true, Basis: "proc_sample"}}}
	}
	m := newToolProcessMonitor(source, func(e agent.Event) { emitted = e })
	m.roots[7] = processRoot{sample: processtelemetry.SampleRoot{PID: 7, Identity: "first"}}
	m.observe(agent.Event{Ts: at, Payload: agent.ToolCallPayload{CallID: "a", Tool: "shell"}})
	m.poll()
	m.flush()
	raw, _ := json.Marshal(emitted.Payload)
	var b processtelemetry.Batch
	_ = json.Unmarshal(raw, &b)
	if len(b.Spans) != 1 || b.Spans[0].CallID != "a" || strings.Contains(string(raw), `"pid"`) || strings.Contains(string(raw), `"root"`) {
		t.Fatal(string(raw))
	}
	m.branches = map[[2]int]string{}
	m.observe(agent.Event{Ts: at, Payload: agent.ToolCallPayload{CallID: "b", Tool: "shell"}})
	m.poll()
	m.flush()
	raw, _ = json.Marshal(emitted.Payload)
	b = processtelemetry.Batch{}
	_ = json.Unmarshal(raw, &b)
	if b.Spans[0].CallID != "" {
		t.Fatal("invented concurrent ownership")
	}
}

func TestProcessMonitorCoalescesALongProcessToACoarseCadence(t *testing.T) {
	at := time.Now()
	const step = 250 * time.Millisecond
	tick := 0
	source := func([]processtelemetry.SampleRoot) processtelemetry.Batch {
		start := at.Add(time.Duration(tick) * step)
		tick++
		return processtelemetry.Batch{Spans: []processtelemetry.Span{{PID: 8, Root: 7, Branch: 8, Program: "go-test", Class: "tests", Start: start, End: start.Add(step), Approximate: true, Basis: "proc_sample"}}}
	}
	var batches []processtelemetry.Batch
	m := newToolProcessMonitor(source, func(e agent.Event) { batches = append(batches, e.Payload.(processtelemetry.Batch)) })
	m.roots[7] = processRoot{sample: processtelemetry.SampleRoot{PID: 7, Identity: "first"}}
	const minutes = 34
	for i := range minutes * 240 {
		m.tick(at.Add(time.Duration(i) * step))
	}
	m.flush()
	if limit := minutes*int(time.Minute/processFlushEvery) + 2; len(batches) > limit {
		t.Fatalf("%d rows for %d minutes of one process, limit %d", len(batches), minutes, limit)
	}
	var covered time.Duration
	for _, b := range batches {
		for _, s := range b.Spans {
			covered += s.End.Sub(s.Start)
		}
	}
	if covered != minutes*time.Minute {
		t.Fatalf("coalescing lost evidence: %s", covered)
	}
}

func TestProcessMonitorKeepsDistinctCallsApartAndBounded(t *testing.T) {
	at := time.Now()
	var emitted []agent.Event
	m := newToolProcessMonitor(nil, func(e agent.Event) { emitted = append(emitted, e) })
	for i := range processtelemetry.MaxSpans + 10 {
		// Separated in time, so nothing merges and the bound has to hold.
		start := at.Add(time.Duration(i) * time.Hour)
		m.merge(processtelemetry.Batch{Spans: []processtelemetry.Span{{Program: "go-test", Class: "tests", Start: start, End: start.Add(time.Second)}}})
	}
	m.merge(processtelemetry.Batch{Spans: []processtelemetry.Span{{CallID: "b", Program: "go-test", Class: "tests", Start: at, End: at.Add(time.Second)}}})
	m.flush()
	if len(emitted) != 1 {
		t.Fatalf("rows: %d", len(emitted))
	}
	b := emitted[0].Payload.(processtelemetry.Batch)
	if len(b.Spans) != processtelemetry.MaxSpans || b.Dropped != 11 {
		t.Fatalf("unbounded: %d spans, %d dropped", len(b.Spans), b.Dropped)
	}
}

func TestTurnDoneFlushesEvidenceWithoutSampling(t *testing.T) {
	at := time.Now()
	entered, release := make(chan struct{}), make(chan struct{})
	first := true
	source := func([]processtelemetry.SampleRoot) processtelemetry.Batch {
		if first {
			first = false
			return processtelemetry.Batch{Spans: []processtelemetry.Span{{PID: 8, Root: 7, Branch: 8, Program: "go-test", Class: "tests", Start: at, End: at.Add(time.Second)}}}
		}
		close(entered)
		<-release
		return processtelemetry.Batch{}
	}
	var emitted []agent.Event
	m := newToolProcessMonitor(source, func(e agent.Event) { emitted = append(emitted, e) })
	m.roots[7] = processRoot{sample: processtelemetry.SampleRoot{PID: 7, Identity: "first"}}
	m.poll()
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		m.poll()
	}()
	<-entered
	observed := make(chan struct{})
	go func() {
		defer close(observed)
		m.observe(agent.Event{Kind: "turn_done"})
	}()
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("turn_done waited on a sample")
	}
	if len(emitted) != 1 {
		t.Errorf("turn_done must carry collected evidence ahead of itself: %d rows", len(emitted))
	}
	close(release)
	<-polled
}
