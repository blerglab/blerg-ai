package server

import (
	"strings"
	"testing"
	"time"
)

func TestBoardInvariantEventsDebouncePerWorkspace(t *testing.T) {
	var q boardInvariantQueue
	callbacks := []func(){}
	runs := 0
	after := func(delay time.Duration, f func()) {
		if delay < 2*time.Second {
			t.Fatal("no debounce")
		}
		callbacks = append(callbacks, f)
	}
	for range 100 {
		q.enqueue("workspace", after, func() { runs++ })
	}
	if runs != 0 || len(callbacks) != 1 {
		t.Fatalf("request path ran %d scans, queued %d", runs, len(callbacks))
	}
	callbacks[0]()
	if runs != 1 {
		t.Fatal(runs)
	}
	q.enqueue("workspace", after, func() { runs++ })
	if len(callbacks) != 2 {
		t.Fatal("subsequent edits not queued")
	}
}

func TestUnplannedDriftAggregates(t *testing.T) {
	f := unplannedInvariantFact("milestone", "0.6.0", []int{9, 8, 7, 6, 5, 4, 3, 2, 1})
	if !strings.Contains(f.text, "9 unplanned") || !strings.Contains(f.text, "#1, #2, #3, #4, #5") || strings.Contains(f.text, "#6") || strings.Count(f.text, "blerg-runner") != 1 {
		t.Fatal(f.text)
	}
}
