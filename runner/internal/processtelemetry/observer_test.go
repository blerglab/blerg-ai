package processtelemetry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestObserverSeesGeneratedScriptDescendantsWithoutArguments(t *testing.T) {
	at := time.Unix(1, 0)
	observer := NewObserver(func(pid int) (string, string) {
		if pid == 2 {
			return "go", "tests"
		}
		return "", ""
	})
	observer.Apply(Event{Kind: Fork, PID: 2, Parent: 1, At: at})
	observer.Apply(Event{Kind: Exec, PID: 2, At: at.Add(time.Second)})
	observer.Apply(Event{Kind: Exit, PID: 2, At: at.Add(8 * time.Second)})
	batch := observer.Drain(at.Add(10 * time.Second))
	if len(batch.Spans) != 1 || batch.Spans[0].Program != "go" || batch.Spans[0].End.Sub(batch.Spans[0].Start) != 7*time.Second {
		t.Fatalf("batch: %+v", batch)
	}
	raw, _ := json.Marshal(batch)
	if strings.Contains(string(raw), "argv") || strings.Contains(string(raw), "command") {
		t.Fatal(string(raw))
	}
}

func TestKnownProgramsUseOnlyAllowlistedSelectors(t *testing.T) {
	for _, tc := range []struct {
		exe            string
		argv           []string
		program, class string
	}{
		{"/private/module.test", nil, "go-test", "tests"},
		{"/bin/go", []string{"go", "-C", "private-directory", "test", "-secret=private"}, "go", "tests"},
		{"/bin/node", []string{"node", "/private/node_modules/vitest/vitest.mjs", "run", "private-spec"}, "vitest", "tests"},
		{"/bin/node", []string{"node", "/private/npm-cli.js", "test"}, "npm", "tests"},
		{"/bin/node", []string{"node", "/private/tsc", "--project", "private-project"}, "tsc", "build_lint"},
		{"/bin/golangci-lint", nil, "golangci-lint", "build_lint"},
		{"/bin/playwright", nil, "playwright", "tests"},
		{"/bin/node", []string{"node", "private-test-name.js"}, "", ""},
		{"/bin/npm", []string{"npm", "view", "test"}, "", ""},
		{"/bin/npm", []string{"npm", "--prefix", "test", "install"}, "", ""},
		{"/bin/node", []string{"node", "unrelated.js", "vitest.mjs"}, "", ""},
		{"/bin/node", []string{"node", "--eval", "vitest.mjs"}, "", ""},
	} {
		p, c := Classify(tc.exe, tc.argv)
		if p != tc.program || c != tc.class {
			t.Fatalf("%s: %q/%q", tc.exe, p, c)
		}
	}
}

func TestObserverBoundsAndDoesNotInventOwnership(t *testing.T) {
	o := NewObserver(func(int) (string, string) { return "vitest", "tests" })
	at := time.Unix(1, 0)
	for i := 1; i <= MaxSpans+20; i++ {
		o.Apply(Event{Kind: Exec, PID: i, At: at})
		o.Apply(Event{Kind: Exit, PID: i, At: at.Add(time.Second)})
	}
	got := o.Drain(at.Add(time.Second))
	if len(got.Spans) > MaxSpans || got.Dropped == 0 {
		t.Fatalf("unbounded: %d %d", len(got.Spans), got.Dropped)
	}
}

func TestDrainNeverWaitsForAnImageRead(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	o := NewObserver(func(int) (string, string) {
		close(entered)
		<-release
		return "go", "tests"
	})
	applied := make(chan struct{})
	go func() {
		defer close(applied)
		o.Apply(Event{Kind: Exec, PID: 2, At: time.Unix(1, 0)})
	}()
	<-entered
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		o.Drain(time.Unix(2, 0))
	}()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Error("Drain waited for a /proc read")
	}
	close(release)
	<-applied
}

func TestDrainSkipsABusyObserver(t *testing.T) {
	o := NewObserver(func(int) (string, string) { return "go", "tests" })
	o.mu.Lock()
	defer o.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		o.Drain(time.Unix(2, 0))
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Drain blocked on the observer lock")
	}
}

func TestSweepEndsAProcessWhoseExitWasDropped(t *testing.T) {
	o := NewObserver(func(int) (string, string) { return "go", "tests" })
	alive := true
	o.Identity = func(int) string {
		if alive {
			return "start-1"
		}
		return ""
	}
	at := time.Unix(1, 0)
	o.Apply(Event{Kind: Exec, PID: 2, At: at})
	if got := o.Drain(at.Add(time.Second)); len(got.Spans) != 1 {
		t.Fatalf("live process: %+v", got)
	}
	alive = false
	o.Sweep(false)
	if got := o.Drain(at.Add(time.Hour)); len(got.Spans) != 0 {
		t.Fatalf("phantom span after a dropped exit: %+v", got)
	}
}

func TestSweepEndsARecycledPID(t *testing.T) {
	o := NewObserver(func(int) (string, string) { return "go", "tests" })
	identity := "start-1"
	o.Identity = func(int) string { return identity }
	at := time.Unix(1, 0)
	o.Apply(Event{Kind: Exec, PID: 2, At: at})
	identity = "start-2"
	o.Sweep(false)
	if got := o.Drain(at.Add(time.Hour)); len(got.Spans) != 0 {
		t.Fatalf("phantom span on a recycled PID: %+v", got)
	}
}

func TestSweepAfterDroppedEventsReclaimsUnclassifiedEntries(t *testing.T) {
	o := NewObserver(func(int) (string, string) { return "", "" })
	o.Identity = func(int) string { return "" }
	at := time.Unix(1, 0)
	o.Apply(Event{Kind: Fork, PID: 2, Parent: 1, At: at})
	o.Sweep(false)
	if len(o.live) != 1 {
		t.Fatalf("a routine sweep must read only classified entries: %d", len(o.live))
	}
	o.Sweep(true)
	if len(o.live) != 0 {
		t.Fatalf("leaked after dropped events: %d", len(o.live))
	}
}
