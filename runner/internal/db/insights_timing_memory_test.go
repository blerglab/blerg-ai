package db

import (
	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"testing"
	"time"
)

func TestChartTimingKeepsEqualPeriodsAndLifetimeTotals(t *testing.T) {
	end := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	split := end.Add(-7 * 24 * time.Hour)
	start := split.Add(-7 * 24 * time.Hour)
	source := &activity.Timing{Seconds: map[string]float64{"model": 15 * 24 * 3600}}
	for h := start.Add(-24 * time.Hour); h.Before(end.Add(time.Hour)); h = h.Add(time.Hour) {
		source.Hours = append(source.Hours, activity.TimingHour{At: h, Phases: map[string]float64{"building": 3600}, Seconds: map[string]float64{"model": 3600}})
	}
	compact := chartTiming(source, []time.Time{start, split, end})
	var previous, current float64
	for _, h := range compact.Hours {
		if h.At.Before(start) || !h.At.Before(end) {
			t.Fatal("hours outside comparison")
		}
		if h.At.Before(split) {
			previous += h.Seconds["model"]
		} else {
			current += h.Seconds["model"]
		}
	}
	if current != 7*24*3600 || previous != current {
		t.Fatalf("unequal periods: %v %v", previous, current)
	}
	if len(compact.Hours) > 16 || len(source.Hours) < 300 || compact.Seconds["model"] != source.Seconds["model"] {
		t.Fatal("compaction changed lifetime evidence")
	}
}

func TestRememberedRunningTimingKeepsItsOriginalExpiry(t *testing.T) {
	s := insightTimingSession{ID: "expiry-check", Fingerprint: "same", Timing: &activity.Timing{}}
	rememberTiming(nil, "memory-test", s)
	key := timingMemoryKey{nil, "memory-test", s.ID}
	timingMemory.Lock()
	old := timingMemory.entries[key]
	old.at = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	timingMemory.entries[key] = old
	timingMemory.Unlock()
	s.CachedTiming = s.Timing
	rememberTiming(nil, "memory-test", s)
	found := rememberedTimings(nil, "memory-test", []string{s.ID})
	if !found[s.ID].at.Equal(old.at) {
		t.Fatal("cache hit extended a running conversation's expiry")
	}
}
