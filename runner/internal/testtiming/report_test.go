package testtiming

import (
	"testing"
	"time"
)

// loadFixtureChain loads three real "changed" gate summaries used across
// this package's tests, oldest first, matching the day's actual run order
// (trimmed down by testdata/derive_fixtures.py from five real summaries).
func loadFixtureChain(t *testing.T) []Record {
	t.Helper()
	names := []string{
		"summary-c4489560.json",
		"summary-d42b8f44.json",
		"summary-ef53c85d.json",
	}
	var records []Record
	for _, name := range names {
		record, err := LoadSummary("testdata/" + name)
		if err != nil {
			t.Fatalf("LoadSummary(%s): %v", name, err)
		}
		records = append(records, record)
	}
	return records
}

func TestBuildOnRealFixturesProducesSuiteStats(t *testing.T) {
	records := loadFixtureChain(t)
	report := Build(records, time.Time{}, time.Now())
	if report.RunCount != 3 {
		t.Fatalf("RunCount = %d, want 3", report.RunCount)
	}
	if len(report.Suites) == 0 {
		t.Fatal("expected at least one suite stat")
	}
	var runnerGo *SuiteStat
	for i := range report.Suites {
		if report.Suites[i].Suite == "runner-go" {
			runnerGo = &report.Suites[i]
		}
	}
	if runnerGo == nil {
		t.Fatal("expected a runner-go suite stat (present in every fixture)")
	}
	if runnerGo.TestCount == 0 {
		t.Fatalf("runner-go.TestCount = 0, want > 0")
	}
	if runnerGo.SecPerTest <= 0 {
		t.Fatalf("runner-go.SecPerTest = %v, want > 0", runnerGo.SecPerTest)
	}
}

func TestBuildSinceFilterTrimsHistoryTableButKeepsMedianSamples(t *testing.T) {
	records := loadFixtureChain(t)
	// The last fixture starts 2026-11-04T01:45; cut at 2026-11-04T00:00 so
	// only the last run is "in window" for the headline table.
	since, err := time.Parse(time.RFC3339, "2026-11-04T00:00:00Z")
	if err != nil {
		t.Fatalf("parse since: %v", err)
	}
	report := Build(records, since, time.Now())
	if report.RunCount != 1 {
		t.Fatalf("RunCount = %d, want 1 (only the last run started after since)", report.RunCount)
	}
	if len(report.Runs) != 1 {
		t.Fatalf("len(Runs) = %d, want 1", len(report.Runs))
	}
	// The rolling median still needs the earlier runs even though they are
	// outside the --since window, or every growth flag silently vanishes
	// when the person files a narrow report.
	found := false
	for _, suite := range report.Suites {
		if suite.Suite == "runner-go" && suite.Median > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("expected runner-go to still have a rolling median from history outside the --since window")
	}
}

func TestBuildEmptyHistoryIsSafe(t *testing.T) {
	report := Build(nil, time.Time{}, time.Now())
	if report.RunCount != 0 || len(report.Suites) != 0 || len(report.SlowTests) != 0 {
		t.Fatalf("expected an empty report, got %+v", report)
	}
}

func TestBuildFlagsASyntheticSlowdown(t *testing.T) {
	now := time.Now()
	makeRecord := func(offsetHours int, seconds float64, count int) Record {
		return Record{
			StartedAt: now.Add(time.Duration(-offsetHours) * time.Hour).Format(time.RFC3339),
			Suites: []Suite{{
				Suite:   "unit",
				Seconds: seconds,
				Outcome: "pass",
				Tests:   SuiteTests{Counts: Counts{Pass: count}},
			}},
		}
	}
	records := []Record{
		makeRecord(5, 10, 100),
		makeRecord(4, 10, 100),
		makeRecord(3, 10, 100),
		makeRecord(2, 10, 100),
		makeRecord(1, 20, 100), // same test count, time doubled: a real slowdown
	}
	report := Build(records, time.Time{}, now)
	var growth *GrowthFlag
	for i := range report.GrowthFlags {
		if report.GrowthFlags[i].Suite == "unit" {
			growth = &report.GrowthFlags[i]
		}
	}
	if growth == nil {
		t.Fatal("expected a growth flag for the suite whose time doubled with no new tests")
	}
	if growth.Kind != "slowdown" {
		t.Fatalf("Kind = %q, want slowdown (test count held steady)", growth.Kind)
	}
}

func TestBuildDoesNotFlagGrowthFromAddedCoverage(t *testing.T) {
	now := time.Now()
	makeRecord := func(offsetHours int, seconds float64, count int) Record {
		return Record{
			StartedAt: now.Add(time.Duration(-offsetHours) * time.Hour).Format(time.RFC3339),
			Suites: []Suite{{
				Suite:   "unit",
				Seconds: seconds,
				Outcome: "pass",
				Tests:   SuiteTests{Counts: Counts{Pass: count}},
			}},
		}
	}
	records := []Record{
		makeRecord(5, 10, 100),
		makeRecord(4, 10, 100),
		makeRecord(3, 10, 100),
		makeRecord(2, 10, 100),
		makeRecord(1, 20, 200), // time doubled because the test count doubled too
	}
	report := Build(records, time.Time{}, now)
	for _, growth := range report.GrowthFlags {
		if growth.Suite == "unit" && growth.Kind != "added-coverage" {
			t.Fatalf("Kind = %q, want added-coverage", growth.Kind)
		}
	}
}

func TestBuildChangesComparesTheTwoMostRecentRuns(t *testing.T) {
	records := loadFixtureChain(t)
	report := Build(records, time.Time{}, time.Now())
	if len(report.Changes) == 0 {
		t.Fatal("expected suite deltas between the two most recent runs")
	}
	// Deltas must be sorted by magnitude, largest first.
	for i := 1; i < len(report.Changes); i++ {
		if absFloat(report.Changes[i-1].DeltaSeconds) < absFloat(report.Changes[i].DeltaSeconds) {
			t.Fatalf("Changes not sorted by |delta| descending at index %d", i)
		}
	}
}

func TestBuildSlowTestsAreSortedAndCapped(t *testing.T) {
	records := loadFixtureChain(t)
	report := Build(records, time.Time{}, time.Now())
	if len(report.SlowTests) == 0 {
		t.Fatal("expected slow tests from the real fixtures")
	}
	if len(report.SlowTests) > 20 {
		t.Fatalf("len(SlowTests) = %d, want <= 20", len(report.SlowTests))
	}
	for i := 1; i < len(report.SlowTests); i++ {
		if report.SlowTests[i-1].Test.Seconds < report.SlowTests[i].Test.Seconds {
			t.Fatalf("SlowTests not sorted descending at index %d", i)
		}
	}
}

func TestMedianOfEmptyIsUnknown(t *testing.T) {
	if _, ok := median(nil); ok {
		t.Fatal("median of an empty slice should report !ok")
	}
}

func TestMedianOddAndEven(t *testing.T) {
	if value, _ := median([]float64{3, 1, 2}); value != 2 {
		t.Fatalf("median([3,1,2]) = %v, want 2", value)
	}
	if value, _ := median([]float64{4, 1, 2, 3}); value != 2.5 {
		t.Fatalf("median([4,1,2,3]) = %v, want 2.5", value)
	}
}
