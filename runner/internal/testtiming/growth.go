package testtiming

import (
	"fmt"
	"sort"
)

const (
	growthWindow    = 10
	growthThreshold = 1.25
	minSamples      = 3
)

// median returns the middle value of a sorted copy of values, or (0, false)
// for an empty slice.
func median(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle], true
	}
	return (sorted[middle-1] + sorted[middle]) / 2, true
}

// suiteGrowth compares a suite's current wall time and test count against
// the rolling median of its own history (same suite, most recent samples
// first truncated to growthWindow). It reports growth only once the sample is
// large enough to mean something, and tells a slowdown apart from a suite
// that just gained tests: a slow individual test is still reported even when
// the suite's own total did not grow, because other tests may have sped up.
func suiteGrowth(name string, pastSeconds, pastCounts []float64, nowSeconds float64, nowCount int) *GrowthFlag {
	if len(pastSeconds) > growthWindow {
		pastSeconds = pastSeconds[len(pastSeconds)-growthWindow:]
	}
	if len(pastCounts) > growthWindow {
		pastCounts = pastCounts[len(pastCounts)-growthWindow:]
	}
	if len(pastSeconds) < minSamples {
		return nil
	}
	secondsMedian, ok := median(pastSeconds)
	if !ok || secondsMedian == 0 || nowSeconds <= secondsMedian*growthThreshold {
		return nil
	}
	pct := (nowSeconds/secondsMedian - 1) * 100
	countMedian, _ := median(pastCounts)
	perTestNow := nowSeconds
	if nowCount > 0 {
		perTestNow = nowSeconds / float64(nowCount)
	}
	var perTestHistory []float64
	for i, seconds := range pastSeconds {
		if i < len(pastCounts) && pastCounts[i] > 0 {
			perTestHistory = append(perTestHistory, seconds/pastCounts[i])
		}
	}
	perTestMedian, hasPerTest := median(perTestHistory)
	switch {
	case hasPerTest && perTestMedian > 0 && perTestNow > perTestMedian*growthThreshold:
		return &GrowthFlag{
			Suite: name, Kind: "slowdown", PercentGrowth: pct,
			Message: fmt.Sprintf("%s: time grew %.0f%% over its rolling median (%.1fs -> %.1fs) and per-test time grew too (%.3fs -> %.3fs/test) -- looks like a slowdown, not just more tests",
				name, pct, secondsMedian, nowSeconds, perTestMedian, perTestNow),
		}
	case float64(nowCount) > countMedian*growthThreshold || (countMedian == 0 && nowCount > 0):
		return &GrowthFlag{
			Suite: name, Kind: "added-coverage", PercentGrowth: pct,
			Message: fmt.Sprintf("%s: time grew %.0f%% over its rolling median (%.1fs -> %.1fs), but test count grew too (%.0f -> %d) -- looks like added coverage, not a slowdown",
				name, pct, secondsMedian, nowSeconds, countMedian, nowCount),
		}
	default:
		return &GrowthFlag{
			Suite: name, Kind: "growth", PercentGrowth: pct,
			Message: fmt.Sprintf("%s: time grew %.0f%% over its rolling median (%.1fs -> %.1fs)", name, pct, secondsMedian, nowSeconds),
		}
	}
}

// testGrowth flags a single test whose time grew over its own rolling
// median, independent of its suite's total.
func testGrowth(suite string, test TestTiming, pastSeconds []float64) *GrowthFlag {
	if len(pastSeconds) > growthWindow {
		pastSeconds = pastSeconds[len(pastSeconds)-growthWindow:]
	}
	if len(pastSeconds) < minSamples {
		return nil
	}
	testMedian, ok := median(pastSeconds)
	if !ok || testMedian == 0 || test.Seconds <= testMedian*growthThreshold {
		return nil
	}
	pct := (test.Seconds/testMedian - 1) * 100
	return &GrowthFlag{
		Suite: suite, Test: test.Test, Kind: "test-slowdown", PercentGrowth: pct,
		Message: fmt.Sprintf("%s: %s grew %.0f%% over its rolling median (%.2fs -> %.2fs)", suite, test.Test, pct, testMedian, test.Seconds),
	}
}

// GrowthFlag is one thing in a report that grew past the rolling median.
type GrowthFlag struct {
	Suite         string
	Test          string
	Kind          string
	PercentGrowth float64
	Message       string
}
