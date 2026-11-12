package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// fakeSleep never actually waits: it records the requested delay and reports ctx's own state, so
// the loop under test runs at full speed regardless of the real backoff durations involved.
func fakeSleep(delays *[]time.Duration) func(context.Context, time.Duration) bool {
	return func(ctx context.Context, d time.Duration) bool {
		*delays = append(*delays, d)
		return ctx.Err() == nil
	}
}

func TestLiveArtifactIndexLoopStopsOutrightOnADeterministicFailure(t *testing.T) {
	calls := 0
	ensure := func(context.Context) error {
		calls++
		return db.ErrLiveArtifactIndexCannotBuild
	}
	var delays []time.Duration
	runLiveArtifactIndexLoop(context.Background(), ensure, fakeSleep(&delays))
	if calls != 1 {
		t.Fatalf("ensure was called %d times; want exactly 1 (no retry after a deterministic failure)", calls)
	}
	if len(delays) != 0 {
		t.Fatalf("slept %v; want no sleep at all, the loop should return at once", delays)
	}
}

func TestLiveArtifactIndexLoopStopsOnSuccess(t *testing.T) {
	calls := 0
	ensure := func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	}
	var delays []time.Duration
	runLiveArtifactIndexLoop(context.Background(), ensure, fakeSleep(&delays))
	if calls != 3 {
		t.Fatalf("ensure was called %d times; want 3 (stop right after success)", calls)
	}
	if len(delays) != 2 {
		t.Fatalf("slept %d times; want 2 (between the two failures and the success, never after)", len(delays))
	}
}

func TestLiveArtifactIndexLoopBacksOffOnARepeatedFailureAndCapsIt(t *testing.T) {
	calls := 0
	ensure := func(context.Context) error {
		calls++
		if calls > 6 {
			return nil
		}
		return errors.New("still down")
	}
	var delays []time.Duration
	runLiveArtifactIndexLoop(context.Background(), ensure, fakeSleep(&delays))
	want := []time.Duration{
		liveIndexBackoffBase, liveIndexBackoffBase * 2, liveIndexBackoffBase * 4,
		liveIndexBackoffBase * 8, liveIndexBackoffBase * 16, liveIndexBackoffCap,
	}
	if len(delays) != len(want) {
		t.Fatalf("delays = %v; want %v", delays, want)
	}
	for i, d := range delays {
		if d != want[i] {
			t.Fatalf("delay %d = %v; want %v (doubling, capped at %v)", i, d, want[i], liveIndexBackoffCap)
		}
	}
}

func TestLiveArtifactIndexLoopResetsBackoffWhenAnotherRunnerIsBuilding(t *testing.T) {
	calls := 0
	ensure := func(context.Context) error {
		calls++
		switch calls {
		case 1, 2:
			return errors.New("still down") // backs off to 2x, 4x base
		case 3:
			return db.ErrLiveArtifactIndexBusy // another runner is handling it: back to base
		default:
			return nil
		}
	}
	var delays []time.Duration
	runLiveArtifactIndexLoop(context.Background(), ensure, fakeSleep(&delays))
	want := []time.Duration{liveIndexBackoffBase, liveIndexBackoffBase * 2, liveIndexBackoffBase}
	if len(delays) != len(want) {
		t.Fatalf("delays = %v; want %v", delays, want)
	}
	for i, d := range delays {
		if d != want[i] {
			t.Fatalf("delay %d = %v; want %v", i, d, want[i])
		}
	}
}

func TestLiveArtifactIndexLoopStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	ensure := func(context.Context) error {
		calls++
		cancel() // the caller is shutting down, mid-retry
		return errors.New("still down")
	}
	var delays []time.Duration
	runLiveArtifactIndexLoop(ctx, ensure, fakeSleep(&delays))
	if calls != 1 {
		t.Fatalf("ensure was called %d times; want 1 (the loop must stop once ctx ends, not keep retrying)", calls)
	}
}
