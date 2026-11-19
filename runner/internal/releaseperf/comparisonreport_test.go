package releaseperf

import (
	"strings"
	"testing"
)

func TestGenerateComparisonReportIncludesChartPerScenario(t *testing.T) {
	baseline := sampleBaseline()
	run := Run{
		Version: "0.7.0",
		Commit:  "def456",
		Scenarios: []ScenarioResult{
			{Name: "workspace-switch", ClientSamplesMs: elevenSamplesAround(104)},
			{Name: "typing-long-session", ClientSamplesMs: append(elevenSamplesAround(8.1), make([]float64, 19)...)},
		},
	}
	compare := CompareRun(run, baseline, baseline.Host)
	md := GenerateComparisonReport(run, &baseline, compare)

	for _, want := range []string{"0.7.0", "def456", "workspace-switch", "typing-long-session", "```vega-lite", "0.6.0"} {
		if !strings.Contains(md, want) {
			t.Fatalf("comparison report missing %q:\n%s", want, md)
		}
	}
	if strings.Count(md, "```vega-lite") != 2 {
		t.Fatalf("expected one vega-lite chart per scenario (2), got %d:\n%s", strings.Count(md, "```vega-lite"), md)
	}
}

// TestChartTitlesStateTheFinding guards the owner's own review note: a
// chart's title must say what it found (e.g. "mostly client-side, not
// backend work", or the percent change and verdict), not just repeat the
// axis labels ("median"/"p95") — exactly how BLG-491's client-vs-server
// gap went unnoticed in the first published report.
func TestChartTitlesStateTheFinding(t *testing.T) {
	if got := chartTitle("cold-load", 8400, 3); !strings.Contains(got, "client") || !strings.Contains(got, "server") {
		t.Fatalf("expected a client-vs-server finding in the title, got %q", got)
	}
	if got := chartTitle("typing", 28, 21); strings.Contains(got, "mostly client-side") {
		t.Fatalf("expected no false client/server gap claim when they're close, got %q", got)
	}
	regressed := ScenarioComparison{Name: "blackboard", Verdict: VerdictRegressed, PercentChange: 0.42}
	if got := comparisonChartTitle(regressed); !strings.Contains(got, "42") || !strings.Contains(got, string(VerdictRegressed)) {
		t.Fatalf("expected the percent change and verdict in the title, got %q", got)
	}
}

func TestGenerateComparisonReportCallsOutHostMismatchFirst(t *testing.T) {
	baseline := sampleBaseline()
	mismatched := baseline.Host
	mismatched.Kernel = "99.0.0"
	compare := RunComparison{Verdict: VerdictNoVerdict, HostMismatchField: "kernel"}
	run := Run{Version: "0.7.0", Commit: "def456"}
	md := GenerateComparisonReport(run, &baseline, compare)
	if !strings.Contains(md, "mismatch") || !strings.Contains(md, "kernel") {
		t.Fatalf("expected the host mismatch to be called out, got:\n%s", md)
	}
	if strings.Contains(md, "## Charts") {
		t.Fatalf("expected no comparison charts when the host doesn't match the baseline:\n%s", md)
	}
}
