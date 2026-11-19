package testtiming

import (
	"sort"
	"time"
)

// GateRun is one gate run's headline numbers for the report's history table.
type GateRun struct {
	StartedAt    string
	Seconds      float64
	Workers      int
	Concurrency  int
	FailedSuites []string
	Changed      string
}

// SuiteStat is one suite's latest-vs-history numbers.
type SuiteStat struct {
	Suite       string
	LastSeconds float64
	Median      float64
	TestCount   int
	SecPerTest  float64
	Growth      *GrowthFlag
}

// SlowTest is one of the report's slowest single tests, with its own trend.
type SlowTest struct {
	Suite  string
	Test   TestTiming
	Growth *GrowthFlag
}

// SuiteDelta is how much a suite's wall time moved between the two most
// recent gate runs.
type SuiteDelta struct {
	Suite         string
	BeforeSeconds float64
	AfterSeconds  float64
	DeltaSeconds  float64
}

// Report is everything test-timing renders as markdown.
type Report struct {
	GeneratedAt time.Time
	Since       time.Time
	RunCount    int
	Runs        []GateRun
	Suites      []SuiteStat
	SlowTests   []SlowTest
	Changes     []SuiteDelta
	GrowthFlags []GrowthFlag
}

// parseTime parses an RFC3339 timestamp, treating a blank or malformed
// string as unknown (zero time) rather than failing the whole report: a
// parsing gap in one run's clock must not hide the rest of the history.
func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// Build turns gate history (oldest first) into a Report. since is a zero
// value to mean "no filter"; records keep contributing to the rolling
// median even when they fall outside the --since window, since the window
// only trims which runs show in the history table.
func Build(records []Record, since time.Time, now time.Time) Report {
	report := Report{GeneratedAt: now, Since: since}
	if len(records) == 0 {
		return report
	}

	testSecondsHistory := map[string][]float64{}

	var inWindow []Record
	for _, record := range records {
		started := parseTime(record.StartedAt)
		if since.IsZero() || started.IsZero() || !started.Before(since) {
			inWindow = append(inWindow, record)
		}
	}
	report.RunCount = len(inWindow)

	for _, record := range inWindow {
		report.Runs = append(report.Runs, GateRun{
			StartedAt:    record.StartedAt,
			Seconds:      record.Seconds,
			Workers:      record.Workers,
			Concurrency:  record.ConcurrentSuites,
			FailedSuites: record.FailedSuites(),
			Changed:      derefOr(record.Changed, ""),
		})
	}

	suiteOrder := []string{}
	seenSuite := map[string]bool{}
	for _, record := range records {
		for _, suite := range record.Suites {
			if !seenSuite[suite.Suite] {
				seenSuite[suite.Suite] = true
				suiteOrder = append(suiteOrder, suite.Suite)
			}
		}
	}

	latest := records[len(records)-1]
	latestBySuite := map[string]Suite{}
	for _, suite := range latest.Suites {
		latestBySuite[suite.Suite] = suite
	}

	for _, name := range suiteOrder {
		var pastSeconds, pastCounts []float64
		for index, record := range records {
			if index == len(records)-1 {
				continue
			}
			for _, suite := range record.Suites {
				if suite.Suite != name {
					continue
				}
				pastSeconds = append(pastSeconds, suite.Seconds)
				pastCounts = append(pastCounts, float64(suite.Tests.Counts.Total()))
				for _, test := range suite.Tests.Slowest {
					key := name + "\x00" + test.Package + "\x00" + test.Test
					testSecondsHistory[key] = append(testSecondsHistory[key], test.Seconds)
				}
			}
		}

		current, present := latestBySuite[name]
		if !present {
			continue
		}
		count := current.Tests.Counts.Total()
		stat := SuiteStat{Suite: name, LastSeconds: current.Seconds, TestCount: count}
		if med, ok := median(pastSeconds); ok {
			stat.Median = med
		}
		if count > 0 {
			stat.SecPerTest = current.Seconds / float64(count)
		}
		stat.Growth = suiteGrowth(name, pastSeconds, pastCounts, current.Seconds, count)
		if stat.Growth != nil {
			report.GrowthFlags = append(report.GrowthFlags, *stat.Growth)
		}
		report.Suites = append(report.Suites, stat)

		for _, test := range current.Tests.Slowest {
			key := name + "\x00" + test.Package + "\x00" + test.Test
			growth := testGrowth(name, test, testSecondsHistory[key])
			if growth != nil {
				report.GrowthFlags = append(report.GrowthFlags, *growth)
			}
		}
	}

	report.SlowTests = collectSlowTests(latest, testSecondsHistory)

	if len(records) >= 2 {
		report.Changes = suiteDeltas(records[len(records)-2], latest)
	}

	return report
}

func derefOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func collectSlowTests(latest Record, testSecondsHistory map[string][]float64) []SlowTest {
	var all []SlowTest
	for _, suite := range latest.Suites {
		for _, test := range suite.Tests.Slowest {
			key := suite.Suite + "\x00" + test.Package + "\x00" + test.Test
			all = append(all, SlowTest{Suite: suite.Suite, Test: test, Growth: testGrowth(suite.Suite, test, testSecondsHistory[key])})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Test.Seconds > all[j].Test.Seconds })
	limit := 20
	if len(all) < limit {
		limit = len(all)
	}
	return all[:limit]
}

func suiteDeltas(before, after Record) []SuiteDelta {
	beforeBySuite := map[string]float64{}
	for _, suite := range before.Suites {
		beforeBySuite[suite.Suite] = suite.Seconds
	}
	afterBySuite := map[string]float64{}
	for _, suite := range after.Suites {
		afterBySuite[suite.Suite] = suite.Seconds
	}
	names := map[string]bool{}
	for name := range beforeBySuite {
		names[name] = true
	}
	for name := range afterBySuite {
		names[name] = true
	}
	var deltas []SuiteDelta
	for name := range names {
		b, a := beforeBySuite[name], afterBySuite[name]
		deltas = append(deltas, SuiteDelta{Suite: name, BeforeSeconds: b, AfterSeconds: a, DeltaSeconds: a - b})
	}
	sort.Slice(deltas, func(i, j int) bool {
		return absFloat(deltas[i].DeltaSeconds) > absFloat(deltas[j].DeltaSeconds)
	})
	return deltas
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
