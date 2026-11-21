package db

import (
	"log"
	"sync"
	"time"
)

// Fixed operation names keep query text, parameters and account identity out of
// diagnostics. Repeated slow reads are summarized at most once per minute.
type InsightsOperation int

const (
	InsightsScope InsightsOperation = iota
	InsightsMarks
	InsightsQuestions
	InsightsTiming
	InsightsSummary
	InsightsCards
	InsightsSkills
)

var insightsSlow = struct {
	sync.Mutex
	last       map[InsightsOperation]time.Time
	suppressed map[InsightsOperation]int
}{last: map[InsightsOperation]time.Time{}, suppressed: map[InsightsOperation]int{}}

func ObserveInsights(operation InsightsOperation) func() {
	start := time.Now()
	return func() {
		recordSlowInsights(operation, time.Since(start), time.Now())
	}
}

func recordSlowInsights(operation InsightsOperation, elapsed time.Duration, now time.Time) {
	if elapsed < 100*time.Millisecond {
		return
	}
	names := []string{"scope", "marks", "questions", "timing", "summary", "cards", "skills"}
	if int(operation) < 0 || int(operation) >= len(names) {
		return
	}
	insightsSlow.Lock()
	defer insightsSlow.Unlock()
	if now.Sub(insightsSlow.last[operation]) < time.Minute {
		insightsSlow.suppressed[operation]++
		return
	}
	log.Printf("insights: slow operation=%s elapsed_ms=%d suppressed=%d", names[operation], elapsed.Milliseconds(), insightsSlow.suppressed[operation])
	insightsSlow.last[operation] = now
	insightsSlow.suppressed[operation] = 0
}
