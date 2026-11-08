package db

import (
	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"time"
)

type timingMemoryKey struct {
	pool      *pgxpool.Pool
	owner, id string
}
type timingMemoryEntry struct {
	fingerprint string
	timing      *activity.Timing
	at          time.Time
}

var timingMemory = struct {
	sync.Mutex
	entries map[timingMemoryKey]timingMemoryEntry
	hours   int
}{entries: map[timingMemoryKey]timingMemoryEntry{}}

func rememberedTimings(pool *pgxpool.Pool, owner string, ids []string) map[string]timingMemoryEntry {
	timingMemory.Lock()
	defer timingMemory.Unlock()
	found := map[string]timingMemoryEntry{}
	for _, id := range ids {
		if entry, ok := timingMemory.entries[timingMemoryKey{pool, owner, id}]; ok {
			found[id] = entry
		}
	}
	return found
}
func rememberTiming(pool *pgxpool.Pool, owner string, s insightTimingSession) {
	if s.Timing == nil {
		return
	}
	timingMemory.Lock()
	defer timingMemory.Unlock()
	key := timingMemoryKey{pool, owner, s.ID}
	if old, ok := timingMemory.entries[key]; ok {
		if old.fingerprint == s.Fingerprint && s.CachedTiming != nil {
			return
		}
		timingMemory.hours -= len(old.timing.Hours) + len(old.timing.GateRuns)
	}
	timingMemory.entries[key] = timingMemoryEntry{s.Fingerprint, s.Timing, time.Now()}
	timingMemory.hours += len(s.Timing.Hours) + len(s.Timing.GateRuns)
	// Bound both map size and retained hourly evidence. Evict oldest entries;
	// PostgreSQL remains the durable cache after memory eviction or restart.
	for len(timingMemory.entries) > 1024 || timingMemory.hours > 120000 {
		var oldest timingMemoryKey
		var at time.Time
		for k, v := range timingMemory.entries {
			if at.IsZero() || v.at.Before(at) {
				oldest = k
				at = v.at
			}
		}
		timingMemory.hours -= len(timingMemory.entries[oldest].timing.Hours) + len(timingMemory.entries[oldest].timing.GateRuns)
		delete(timingMemory.entries, oldest)
	}
}

// Keep lifetime totals for drill-down, but send only dated evidence needed by
// the two chart periods. Daily sums never cross their comparison boundaries.
func chartTiming(t *activity.Timing, windows []time.Time) *activity.Timing {
	if t == nil || len(windows) != 3 {
		return t
	}
	from, split, to := windows[0], windows[1], windows[2]
	result := *t
	result.Hours = nil
	daily := to.Sub(split) > 24*time.Hour
	for _, h := range t.Hours {
		if h.At.Before(from) || !h.At.Before(to) {
			continue
		}
		at := h.At
		if daily {
			at = at.Truncate(24 * time.Hour)
			boundary := from
			if !h.At.Before(split) {
				boundary = split
			}
			if at.Before(boundary) {
				at = boundary
			}
		}
		n := len(result.Hours)
		if n == 0 || !result.Hours[n-1].At.Equal(at) {
			result.Hours = append(result.Hours, activity.TimingHour{At: at, Phases: map[string]float64{}, Seconds: map[string]float64{}, Detail: map[string]float64{}})
			n++
		}
		for k, v := range h.Phases {
			result.Hours[n-1].Phases[k] += v
		}
		for k, v := range h.Seconds {
			result.Hours[n-1].Seconds[k] += v
		}
		for k, v := range h.Detail {
			result.Hours[n-1].Detail[k] += v
		}
	}
	result.GateRuns = nil
	type runKey struct {
		at                  time.Time
		kind, suite, region string
	}
	groups := map[runKey]int{}
	for _, r := range t.GateRuns {
		if r.At.Before(from) || !r.At.Before(to) {
			continue
		}
		if daily {
			at := r.At.Truncate(24 * time.Hour)
			boundary := from
			if !r.At.Before(split) {
				boundary = split
			}
			if at.Before(boundary) {
				at = boundary
			}
			r.At = at
		}
		key := runKey{r.At, r.Kind, r.Suite, r.Region}
		n, ok := groups[key]
		if !ok {
			groups[key] = len(result.GateRuns)
			result.GateRuns = append(result.GateRuns, r)
			continue
		}
		target := &result.GateRuns[n]
		target.Seconds += r.Seconds
		target.Runs += r.Runs
		target.Passed += r.Passed
		target.Failed += r.Failed
		target.UnknownOutcome += r.UnknownOutcome
		target.Repeated += r.Repeated
		target.Reruns += r.Reruns
		target.RepeatUnknown += r.RepeatUnknown
		target.FailedSeconds += r.FailedSeconds
		target.CountUnknown = target.CountUnknown || r.CountUnknown
	}
	return &result
}
