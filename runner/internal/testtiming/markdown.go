package testtiming

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RenderMarkdown turns a Report into markdown with an inline-data Vega-Lite
// chart. No color is ever set on the chart's marks or scales: Blerg draws
// it in the viewer's own theme tokens.
func RenderMarkdown(report Report, repo string) string {
	var out strings.Builder

	fmt.Fprintf(&out, "# Test timing report%s\n\n", titleSuffix(repo))
	fmt.Fprintf(&out, "Generated %s", report.GeneratedAt.UTC().Format("2006-01-02 15:04 UTC"))
	if !report.Since.IsZero() {
		fmt.Fprintf(&out, " · since %s", report.Since.UTC().Format("2006-01-02 15:04 UTC"))
	}
	fmt.Fprintf(&out, " · %d gate run(s) in window\n\n", report.RunCount)

	if report.RunCount == 0 {
		out.WriteString("No gate runs found in this window. Nothing to report.\n")
		return out.String()
	}

	writeWhatItSays(&out, report)
	writeGateHistory(&out, report)
	writeSuiteStats(&out, report)
	writeChart(&out, report)
	writeSlowTests(&out, report)
	writeChanges(&out, report)

	return out.String()
}

func titleSuffix(repo string) string {
	if repo == "" {
		return ""
	}
	return ": " + repo
}

func writeWhatItSays(out *strings.Builder, report Report) {
	out.WriteString("## What it says\n\n")
	if len(report.GrowthFlags) == 0 {
		out.WriteString("No suite or test grew past its rolling median by 25% or more. Test time looks stable.\n\n")
		return
	}
	fmt.Fprintf(out, "%d suite(s) or test(s) grew 25%% or more over their rolling median:\n\n", len(report.GrowthFlags))
	sorted := append([]GrowthFlag(nil), report.GrowthFlags...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PercentGrowth > sorted[j].PercentGrowth })
	for _, flag := range sorted {
		fmt.Fprintf(out, "- %s\n", flag.Message)
	}
	out.WriteString("\nFile a card for any growth above that is not explained by a known, intentional change.\n\n")
}

func writeGateHistory(out *strings.Builder, report Report) {
	out.WriteString("## Gate runs\n\n")
	out.WriteString("| Started | Wall seconds | Concurrency | Workers | Failed suites |\n")
	out.WriteString("|---|---|---|---|---|\n")
	for _, run := range report.Runs {
		failed := "none"
		if len(run.FailedSuites) > 0 {
			failed = strings.Join(run.FailedSuites, ", ")
		}
		fmt.Fprintf(out, "| %s | %.1f | %d | %d | %s |\n", run.StartedAt, run.Seconds, run.Concurrency, run.Workers, failed)
	}
	out.WriteString("\n")
}

func writeSuiteStats(out *strings.Builder, report Report) {
	out.WriteString("## Suites\n\n")
	out.WriteString("| Suite | Last seconds | Rolling median | Test count | Seconds/test | Growth |\n")
	out.WriteString("|---|---|---|---|---|---|\n")
	suites := append([]SuiteStat(nil), report.Suites...)
	sort.Slice(suites, func(i, j int) bool { return suites[i].LastSeconds > suites[j].LastSeconds })
	for _, suite := range suites {
		growth := "-"
		if suite.Growth != nil {
			growth = fmt.Sprintf("+%.0f%%", suite.Growth.PercentGrowth)
		}
		fmt.Fprintf(out, "| %s | %.1f | %s | %d | %.3f | %s |\n",
			suite.Suite, suite.LastSeconds, formatMedian(suite.Median), suite.TestCount, suite.SecPerTest, growth)
	}
	out.WriteString("\n")
}

func formatMedian(value float64) string {
	if value == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", value)
}

func writeSlowTests(out *strings.Builder, report Report) {
	out.WriteString("## Slowest tests\n\n")
	if len(report.SlowTests) == 0 {
		out.WriteString("No per-test timing recorded for this window.\n\n")
		return
	}
	out.WriteString("| Suite | Test | Seconds | Trend |\n")
	out.WriteString("|---|---|---|---|\n")
	for _, slow := range report.SlowTests {
		trend := "flat"
		if slow.Growth != nil {
			trend = fmt.Sprintf("+%.0f%% over median", slow.Growth.PercentGrowth)
		}
		fmt.Fprintf(out, "| %s | %s | %.2f | %s |\n", slow.Suite, slow.Test.Test, slow.Test.Seconds, trend)
	}
	out.WriteString("\n")
}

func writeChanges(out *strings.Builder, report Report) {
	out.WriteString("## What changed since the last report\n\n")
	if len(report.Changes) == 0 {
		out.WriteString("Only one gate run in history; nothing to compare yet.\n\n")
		return
	}
	out.WriteString("| Suite | Before | After | Delta |\n")
	out.WriteString("|---|---|---|---|\n")
	shown := 0
	for _, delta := range report.Changes {
		if delta.DeltaSeconds == 0 {
			continue
		}
		fmt.Fprintf(out, "| %s | %.1f | %.1f | %+.1f |\n", delta.Suite, delta.BeforeSeconds, delta.AfterSeconds, delta.DeltaSeconds)
		shown++
		if shown >= 15 {
			break
		}
	}
	if shown == 0 {
		out.WriteString("No suite's wall time changed between the two most recent runs.\n")
	}
	out.WriteString("\n")
}

// vegaLiteDatum is one point the chart plots: a gate run's total wall time.
type vegaLiteDatum struct {
	Run     string  `json:"run"`
	Seconds float64 `json:"seconds"`
}

func writeChart(out *strings.Builder, report Report) {
	out.WriteString("## Gate wall time over time\n\n")
	var data []vegaLiteDatum
	for _, run := range report.Runs {
		data = append(data, vegaLiteDatum{Run: run.StartedAt, Seconds: run.Seconds})
	}
	spec := map[string]interface{}{
		"$schema":     "https://vega.github.io/schema/vega-lite/v5.json",
		"description": "Gate wall time per run",
		"data":        map[string]interface{}{"values": data},
		"mark":        "line",
		"encoding": map[string]interface{}{
			"x": map[string]interface{}{"field": "run", "type": "temporal", "title": "Gate run"},
			"y": map[string]interface{}{"field": "seconds", "type": "quantitative", "title": "Wall seconds"},
		},
	}
	encoded, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		fmt.Fprintf(out, "(chart omitted: %v)\n\n", err)
		return
	}
	out.WriteString("```vega-lite\n")
	out.Write(encoded)
	out.WriteString("\n```\n\n")
}
