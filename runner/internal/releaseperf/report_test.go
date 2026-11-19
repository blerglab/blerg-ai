package releaseperf

import (
	"strings"
	"testing"
)

func sampleResult() ScenarioResult {
	return ScenarioResult{
		Name:            "agents-manifest",
		ClientSamplesMs: []float64{10, 11, 12, 10, 11, 12, 10, 11, 12, 10, 11},
		ServerSamplesMs: []float64{2, 3, 2, 3, 2, 3, 2, 3, 2, 3, 2},
		WarmupClientMs:  15,
		WarmupServerMs:  4,
	}
}

func TestGenerateReportIncludesScenarioAndVerdict(t *testing.T) {
	r := Run{
		Version:   "0.6.0",
		Commit:    "abc1234",
		Verdict:   "baseline recorded, no comparison made",
		Scenarios: []ScenarioResult{sampleResult()},
	}
	md := GenerateReport(r)
	for _, want := range []string{"0.6.0", "abc1234", "agents-manifest", "baseline recorded, no comparison made", "median"} {
		if !strings.Contains(md, want) {
			t.Fatalf("report missing %q:\n%s", want, md)
		}
	}
}

// TestGenerateReportIncludesChart guards the ticket's own "readable report
// (markdown with a chart per scenario)" requirement for a first run with no
// prior baseline — GenerateComparisonReport already charts a run against a
// baseline, but GenerateReport (the no-baseline path every first release's
// own run actually takes) had no chart at all until this test was added.
func TestGenerateReportIncludesChart(t *testing.T) {
	r := Run{
		Version:   "0.6.0",
		Commit:    "abc1234",
		Verdict:   "baseline recorded, no comparison made",
		Scenarios: []ScenarioResult{sampleResult()},
	}
	md := GenerateReport(r)
	if !strings.Contains(md, "```vega-lite") {
		t.Fatalf("expected a vega-lite chart block, got:\n%s", md)
	}
	if !strings.Contains(md, "agents-manifest") || strings.Count(md, "```vega-lite") != 1 {
		t.Fatalf("expected exactly one chart, for the one seeded scenario, got:\n%s", md)
	}
}
