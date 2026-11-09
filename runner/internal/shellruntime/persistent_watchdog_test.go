package shellruntime

import (
	"testing"
	"time"
)

func TestPersistentWatchdogCountsSleepAndRejectsBackwardClock(t *testing.T) {
	wall := time.Unix(1000, 0)
	elapsed := time.Second
	clock := func() (time.Time, time.Duration, error) { return wall, elapsed, nil }
	expired, err := newPersistentWatchdog(wall.Add(time.Minute), clock)
	if err != nil {
		t.Fatal(err)
	}
	wall = wall.Add(-time.Hour)
	elapsed += 2 * time.Minute
	if !expired() {
		t.Fatal("host sleep escaped frozen-container watchdog")
	}
	wall = time.Unix(1000, 0)
	elapsed = time.Second
	expired, err = newPersistentWatchdog(wall.Add(time.Minute), clock)
	if err != nil {
		t.Fatal(err)
	}
	elapsed--
	if !expired() {
		t.Fatal("backward elapsed clock restored frozen runtime")
	}
}
