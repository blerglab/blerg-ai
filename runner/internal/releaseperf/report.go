package releaseperf

import (
	"fmt"
	"strings"
)

// ScenarioResult is one scenario's measured numbers from a single run.
type ScenarioResult struct {
	Name            string
	ClientSamplesMs []float64
	ServerSamplesMs []float64 // empty when the scenario has no backend leg
	WarmupClientMs  float64
	WarmupServerMs  float64
}

// Run is everything GenerateReport needs to render one release-perf run's
// Markdown report.
type Run struct {
	Version   string
	Commit    string
	Verdict   string
	Scenarios []ScenarioResult
}

// GenerateReport renders a Run as the Markdown report handed to Main to
// publish. First-run behavior (no baseline to compare against) is the only
// case implemented so far: see docs/design/release-performance.md for the
// full comparison/chart format this grows into once a baseline exists.
func GenerateReport(r Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Release performance check — %s\n\n", r.Version)
	fmt.Fprintf(&b, "Commit: `%s`\n\n", r.Commit)
	fmt.Fprintf(&b, "**Verdict: %s**\n\n", r.Verdict)
	fmt.Fprintf(&b, "| Scenario | N (client) | median ms (client) | p95 ms (client) | N (server) | median ms (server) | p95 ms (server) |\n")
	fmt.Fprintf(&b, "| --- | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, s := range r.Scenarios {
		serverN, serverMedian, serverP95 := "—", "—", "—"
		if len(s.ServerSamplesMs) > 0 {
			serverN = fmt.Sprintf("%d", len(s.ServerSamplesMs))
			serverMedian = fmt.Sprintf("%.3f", Median(s.ServerSamplesMs))
			serverP95 = fmt.Sprintf("%.3f", P95(s.ServerSamplesMs))
		}
		fmt.Fprintf(&b, "| %s | %d | %.3f | %.3f | %s | %s | %s |\n",
			s.Name, len(s.ClientSamplesMs), Median(s.ClientSamplesMs), P95(s.ClientSamplesMs),
			serverN, serverMedian, serverP95)
	}
	b.WriteString("\n## Charts\n\n")
	for _, s := range r.Scenarios {
		fmt.Fprintf(&b, "### %s\n\n", s.Name)
		serverMedian := 0.0
		if len(s.ServerSamplesMs) > 0 {
			serverMedian = Median(s.ServerSamplesMs)
		}
		b.WriteString(vegaLiteFirstRunChart(s.Name, Median(s.ClientSamplesMs), P95(s.ClientSamplesMs), serverMedian))
		b.WriteString("\n")
	}
	b.WriteString("\n## Per-scenario detail\n\n")
	for _, s := range r.Scenarios {
		fmt.Fprintf(&b, "### %s\n\n", s.Name)
		fmt.Fprintf(&b, "- Discarded warm-up (client): %.3f ms\n", s.WarmupClientMs)
		if len(s.ServerSamplesMs) > 0 {
			fmt.Fprintf(&b, "- Discarded warm-up (server): %.3f ms\n", s.WarmupServerMs)
		}
		fmt.Fprintf(&b, "- Client samples (ms): %v\n", s.ClientSamplesMs)
		if len(s.ServerSamplesMs) > 0 {
			fmt.Fprintf(&b, "- Server samples (ms): %v\n", s.ServerSamplesMs)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// GenerateComparisonReport renders a run against a baseline: the summary
// table with percent change and verdict, one inline Vega-Lite chart per
// scenario (median/p95, last release vs. this one — inline data only, no
// external resources, per the session's rich-output rules), and
// per-scenario detail. A host mismatch is called out first, since it
// changes what the rest of the report means (docs/design/release-performance.md,
// "Report").
func GenerateComparisonReport(run Run, baseline *Baseline, compare RunComparison) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Release performance check — %s\n\n", run.Version)
	fmt.Fprintf(&b, "Commit: `%s`\n\n", run.Commit)
	if baseline != nil {
		fmt.Fprintf(&b, "Compared against: `%s` (commit `%s`)\n\n", baseline.Version, baseline.Commit)
	}
	fmt.Fprintf(&b, "**Verdict: %s**\n\n", compare.Verdict)
	if compare.Verdict == VerdictNoVerdict && compare.HostMismatchField != "" {
		fmt.Fprintf(&b, "**Host mismatch on `%s`** — a baseline is only valid on the machine that recorded it; no comparison was attempted.\n\n", compare.HostMismatchField)
		return b.String()
	}

	b.WriteString("| Scenario | median before | median now | Δ% | p95 now | verdict |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | --- |\n")
	for _, s := range compare.Scenarios {
		before := "—"
		pct := "—"
		if !s.New {
			before = fmt.Sprintf("%.3f", s.MedianBefore)
			pct = fmt.Sprintf("%+.1f%%", s.PercentChange*100)
		}
		verdict := string(s.Verdict)
		if s.New {
			verdict = "new"
		}
		fmt.Fprintf(&b, "| %s | %s | %.3f | %s | %.3f | %s |\n", s.Name, before, s.MedianNow, pct, s.P95Now, verdict)
	}

	b.WriteString("\n## Charts\n\n")
	byName := map[string]ScenarioComparison{}
	for _, s := range compare.Scenarios {
		byName[s.Name] = s
	}
	for _, s := range run.Scenarios {
		cmp := byName[s.Name]
		fmt.Fprintf(&b, "### %s\n\n", s.Name)
		b.WriteString(vegaLiteComparisonChart(s.Name, cmp, baseline))
		b.WriteString("\n")
	}
	return b.String()
}

// chartTitle states the finding a chart's reader needs, not just the
// metric names on its axes (the session's own visualizing conventions): a
// client total much larger than the matching server number means most of
// the time is client-side/network/connect cost, not backend work — exactly
// the shape BLG-491's sendInitialState cost took before it was named, and
// worth saying in the title itself rather than leaving a reader to infer
// it from two bars.
func chartTitle(name string, clientMedian, serverMedian float64) string {
	if serverMedian > 0 && clientMedian > 0 && serverMedian/clientMedian < 0.5 {
		return fmt.Sprintf("%s: %.0fms client vs %.0fms server — mostly client-side/connect, not backend work", name, clientMedian, serverMedian)
	}
	return fmt.Sprintf("%s: %.0fms median", name, clientMedian)
}

// vegaLiteFirstRunChart renders one scenario's median/p95 for a run with no
// prior baseline to compare against (GenerateReport) — a plain two-bar
// chart, since there is no "last release" series yet. The first accepted
// baseline's own report switches to vegaLiteComparisonChart automatically
// once a prior release exists to compare against. serverMedian is 0 when
// the scenario has no server-reported sample.
func vegaLiteFirstRunChart(name string, median, p95, serverMedian float64) string {
	var b strings.Builder
	b.WriteString("```vega-lite\n")
	fmt.Fprintf(&b, `{
  "title": %q,
  "data": {"values": [
    {"metric": "median", "ms": %.3f},
    {"metric": "p95", "ms": %.3f}
  ]},
  "mark": "bar",
  "encoding": {
    "x": {"field": "metric", "type": "nominal"},
    "y": {"field": "ms", "type": "quantitative"},
    "color": {"field": "metric", "type": "nominal"}
  }
}`, chartTitle(name, median, serverMedian), median, p95)
	b.WriteString("\n```\n")
	return b.String()
}

// comparisonChartTitle states the finding: how much the median moved and
// against what verdict, not just "median/p95" (the session's own
// visualizing conventions — see chartTitle, above, for the first-run case).
func comparisonChartTitle(cmp ScenarioComparison) string {
	if cmp.New {
		return fmt.Sprintf("%s: %.0fms median (new this release, no prior baseline)", cmp.Name, cmp.MedianNow)
	}
	return fmt.Sprintf("%s: %+.0f%% vs last release (%s)", cmp.Name, cmp.PercentChange*100, cmp.Verdict)
}

// vegaLiteComparisonChart renders one scenario's median/p95, last release
// vs. this one, as an inline-data Vega-Lite bar chart.
func vegaLiteComparisonChart(name string, cmp ScenarioComparison, baseline *Baseline) string {
	if cmp.Name == "" {
		cmp.Name = name // a scenario absent from the comparison (dropped this release) still needs a real name in its title
	}
	beforeLabel := "last release"
	if baseline != nil {
		beforeLabel = baseline.Version
	}
	var b strings.Builder
	b.WriteString("```vega-lite\n")
	fmt.Fprintf(&b, `{
  "title": %q,
  "data": {"values": [
    {"release": %q, "metric": "median", "ms": %.3f},
    {"release": "this release", "metric": "median", "ms": %.3f},
    {"release": %q, "metric": "p95", "ms": %.3f},
    {"release": "this release", "metric": "p95", "ms": %.3f}
  ]},
  "mark": "bar",
  "encoding": {
    "x": {"field": "release", "type": "nominal"},
    "y": {"field": "ms", "type": "quantitative"},
    "column": {"field": "metric", "type": "nominal"},
    "color": {"field": "release", "type": "nominal"}
  }
}`, comparisonChartTitle(cmp), beforeLabel, cmp.MedianBefore, cmp.MedianNow, beforeLabel, cmp.P95Before, cmp.P95Now)
	b.WriteString("\n```\n")
	return b.String()
}
