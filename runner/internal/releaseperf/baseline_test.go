package releaseperf

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleHost() HostFingerprint {
	return HostFingerprint{CPUModel: "AMD Ryzen 9 7950X", LogicalCPUs: 32, MemoryGiB: 64, Kernel: "6.1.0", WSL: false, Browser: "Chromium 131"}
}

func sampleBaseline() Baseline {
	return Baseline{
		Version:     "0.6.0",
		Commit:      "abc123",
		RecordedAt:  time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		SeedVersion: SeedVersion,
		Host:        sampleHost(),
		Scenarios: map[string]BaselineScenario{
			"workspace-switch":    {Unit: "ms", N: 11, Median: 100, P95: 150, NoiseMedianMs: 5},
			"typing-long-session": {Unit: "ms", N: 30, Median: 8, P95: 12, BudgetP95: 16, NoiseMedianMs: 1},
			"insights-7d-backend": {Unit: "ms", N: 11, Median: 200, P95: 300, BudgetP95: 500, NoiseMedianMs: 10},
		},
	}
}

func TestCompareCleanWhenWithinMarginAndNoise(t *testing.T) {
	baseline := sampleBaseline()
	run := Run{Scenarios: []ScenarioResult{{Name: "workspace-switch", ClientSamplesMs: elevenSamplesAround(104)}}}
	outcome := CompareRun(run, baseline, sampleHost())
	if outcome.Verdict != VerdictClean {
		t.Fatalf("expected clean, got %v: %+v", outcome.Verdict, outcome.Scenarios)
	}
}

func TestCompareFlagsOnlyWhenBeyondMarginAndNoise(t *testing.T) {
	baseline := sampleBaseline()
	// 100 -> 118 is +18%, beyond the 15% margin, and the delta (18) exceeds
	// the stored noise floor (5): this must flag.
	run := Run{Scenarios: []ScenarioResult{{Name: "workspace-switch", ClientSamplesMs: elevenSamplesAround(118)}}}
	outcome := CompareRun(run, baseline, sampleHost())
	if outcome.Verdict != VerdictRegressed {
		t.Fatalf("expected regressed, got %v: %+v", outcome.Verdict, outcome.Scenarios)
	}
}

func TestCompareDoesNotFlagWhenChangeIsWithinNoiseFloor(t *testing.T) {
	baseline := sampleBaseline()
	baseline.Scenarios["workspace-switch"] = BaselineScenario{Unit: "ms", N: 11, Median: 100, P95: 150, NoiseMedianMs: 30}
	// +18% exceeds the margin but the noise floor (30) is bigger than the
	// 18-ms delta: must not flag, per the "both... margin and noise" rule.
	run := Run{Scenarios: []ScenarioResult{{Name: "workspace-switch", ClientSamplesMs: elevenSamplesAround(118)}}}
	outcome := CompareRun(run, baseline, sampleHost())
	if outcome.Verdict != VerdictClean {
		t.Fatalf("expected clean (within noise floor), got %v: %+v", outcome.Verdict, outcome.Scenarios)
	}
}

func TestCompareFlagsOnBudgetCrossingRegardlessOfMargin(t *testing.T) {
	baseline := sampleBaseline()
	// Only a tiny median move, well under the margin, but p95 crosses the
	// 16 ms typing budget.
	samples := elevenSamplesAround(8.1)
	samples = append(samples, make([]float64, 19)...)
	for i := 11; i < 30; i++ {
		samples[i] = 17
	}
	run := Run{Scenarios: []ScenarioResult{{Name: "typing-long-session", ClientSamplesMs: samples}}}
	outcome := CompareRun(run, baseline, sampleHost())
	if outcome.Verdict != VerdictRegressed {
		t.Fatalf("expected regressed (budget crossing), got %v: %+v", outcome.Verdict, outcome.Scenarios)
	}
}

func TestCompareHostMismatchGivesNoVerdict(t *testing.T) {
	baseline := sampleBaseline()
	run := Run{Scenarios: []ScenarioResult{{Name: "workspace-switch", ClientSamplesMs: elevenSamplesAround(100)}}}
	other := sampleHost()
	other.Kernel = "5.15.0"
	outcome := CompareRun(run, baseline, other)
	if outcome.Verdict != VerdictNoVerdict {
		t.Fatalf("expected no-verdict on host mismatch, got %v", outcome.Verdict)
	}
	if outcome.HostMismatchField != "kernel" {
		t.Fatalf("expected the mismatch to name kernel, got %q", outcome.HostMismatchField)
	}
}

func TestCompareNewScenarioIsRecordedNotFlagged(t *testing.T) {
	baseline := sampleBaseline()
	run := Run{Scenarios: []ScenarioResult{{Name: "search", ClientSamplesMs: elevenSamplesAround(50)}}}
	outcome := CompareRun(run, baseline, sampleHost())
	if outcome.Verdict != VerdictClean {
		t.Fatalf("expected clean (new scenario, nothing to regress against), got %v", outcome.Verdict)
	}
}

func TestSaveAndLoadBaselineRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "0.6.0.json")
	want := sampleBaseline()
	if err := SaveBaseline(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != want.Version || got.Scenarios["workspace-switch"].Median != 100 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestAcceptBaselineRequiresACleanCandidate(t *testing.T) {
	dir := t.TempDir()
	candidatePath := filepath.Join(dir, "0.7.0-candidate.json")
	candidate := sampleBaseline()
	candidate.Version = "0.7.0"
	candidate.Verdict = VerdictRegressed
	if err := SaveBaseline(candidatePath, candidate); err != nil {
		t.Fatal(err)
	}
	officialPath := filepath.Join(dir, "0.7.0.json")
	if err := AcceptBaseline(candidatePath, officialPath, "v0.7.0"); err == nil {
		t.Fatal("expected AcceptBaseline to refuse a non-clean candidate")
	}
	if _, err := os.Stat(officialPath); !os.IsNotExist(err) {
		t.Fatal("expected no official baseline file to be written for a refused accept")
	}
}

func TestAcceptBaselineStampsAcceptedAtAndReleaseTag(t *testing.T) {
	dir := t.TempDir()
	candidatePath := filepath.Join(dir, "0.7.0-candidate.json")
	candidate := sampleBaseline()
	candidate.Version = "0.7.0"
	candidate.Verdict = VerdictClean
	if err := SaveBaseline(candidatePath, candidate); err != nil {
		t.Fatal(err)
	}
	officialPath := filepath.Join(dir, "0.7.0.json")
	if err := AcceptBaseline(candidatePath, officialPath, "v0.7.0"); err != nil {
		t.Fatal(err)
	}
	accepted, err := LoadBaseline(officialPath)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ReleaseTag != "v0.7.0" {
		t.Fatalf("expected release_tag to be stamped, got %q", accepted.ReleaseTag)
	}
	if accepted.AcceptedAt.IsZero() {
		t.Fatal("expected accepted_at to be stamped")
	}
}

func elevenSamplesAround(v float64) []float64 {
	return []float64{v, v, v, v, v, v, v, v, v, v, v}
}
