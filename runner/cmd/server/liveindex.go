package main

// The live-file unique index's own retry loop (BLG-470 round 2 #4), separate from main() so its
// backoff and stop-on-deterministic-failure behavior can be tested without a real database or a
// real clock.

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	liveIndexBackoffBase = time.Minute
	liveIndexBackoffCap  = 30 * time.Minute
)

// ctxSleep waits d or until ctx ends, whichever comes first; it reports whether it waited the
// full d (false means ctx ended first, so the caller should stop).
func ctxSleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// runLiveArtifactIndexLoop calls ensure until it succeeds or ctx ends. ErrLiveArtifactIndexBusy
// (another runner is building it) retries promptly, at the base delay. Any other failure backs
// off, doubling up to liveIndexBackoffCap, and is logged only when its message actually changes
// — not on every identical retry, the way a flat timer would. A deterministic failure
// (ErrLiveArtifactIndexCannotBuild: duplicate rows survived the repair pass) is logged once and
// stops the loop outright: nothing about the data changes between retries, so looping on a timer
// cannot fix it, and it needs a person. sleep is injected so a test can run the loop without
// waiting for a real backoff.
func runLiveArtifactIndexLoop(ctx context.Context, ensure func(context.Context) error, sleep func(context.Context, time.Duration) bool) {
	backoff := liveIndexBackoffBase
	var lastLogged string
	for {
		err := ensure(ctx)
		if err == nil {
			return
		}
		if errors.Is(err, db.ErrLiveArtifactIndexCannotBuild) {
			log.Printf("live artifact index: %v — stopping; this needs a person, not another retry", err)
			return
		}
		if errors.Is(err, db.ErrLiveArtifactIndexBusy) {
			backoff = liveIndexBackoffBase
		} else if msg := err.Error(); msg != lastLogged {
			log.Printf("live artifact index: %v (retrying in %s, backing off further while it keeps failing)", err, backoff)
			lastLogged = msg
		}
		wait := backoff
		if !errors.Is(err, db.ErrLiveArtifactIndexBusy) {
			if backoff *= 2; backoff > liveIndexBackoffCap {
				backoff = liveIndexBackoffCap
			}
		}
		if !sleep(ctx, wait) {
			return
		}
	}
}
