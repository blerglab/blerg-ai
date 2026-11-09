package testtiming

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRenderMarkdownOnRealFixtures(t *testing.T) {
	records := loadFixtureChain(t)
	report := Build(records, time.Time{}, time.Now())
	markdown := RenderMarkdown(report, "blerg")

	for _, want := range []string{
		"# Test timing report: blerg",
		"## What it says",
		"## Gate runs",
		"## Suites",
		"## Gate wall time over time",
		"## Slowest tests",
		"## What changed since the last report",
		"```vega-lite",
	} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("markdown missing %q\n\n%s", want, markdown)
		}
	}
}

func TestRenderMarkdownChartHasNoHardcodedColor(t *testing.T) {
	records := loadFixtureChain(t)
	report := Build(records, time.Time{}, time.Now())
	markdown := RenderMarkdown(report, "blerg")
	start := strings.Index(markdown, "```vega-lite")
	end := strings.Index(markdown[start:], "```\n")
	chartBlock := markdown[start : start+end]
	for _, forbidden := range []string{"color", "\"#", "scheme"} {
		if strings.Contains(strings.ToLower(chartBlock), forbidden) {
			t.Fatalf("chart spec sets %q; Blerg must draw it in theme tokens, not a hardcoded color\n%s", forbidden, chartBlock)
		}
	}
}

func TestRenderMarkdownChartIsValidJSON(t *testing.T) {
	records := loadFixtureChain(t)
	report := Build(records, time.Time{}, time.Now())
	markdown := RenderMarkdown(report, "blerg")
	start := strings.Index(markdown, "```vega-lite\n") + len("```vega-lite\n")
	end := strings.Index(markdown[start:], "\n```")
	raw := markdown[start : start+end]
	var spec map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatalf("chart spec is not valid JSON: %v\n%s", err, raw)
	}
	data, ok := spec["data"].(map[string]interface{})
	if !ok {
		t.Fatal("spec.data missing")
	}
	values, ok := data["values"].([]interface{})
	if !ok || len(values) == 0 {
		t.Fatal("spec.data.values should carry the report's gate runs inline")
	}
}

func TestRenderMarkdownOnEmptyHistory(t *testing.T) {
	report := Build(nil, time.Time{}, time.Now())
	markdown := RenderMarkdown(report, "blerg")
	if !strings.Contains(markdown, "Nothing to report") {
		t.Fatalf("expected an explicit empty-history message, got:\n%s", markdown)
	}
}

func TestRenderMarkdownNoGrowthSaysSoPlainly(t *testing.T) {
	now := time.Now()
	records := []Record{
		{StartedAt: now.Format(time.RFC3339), Suites: []Suite{{Suite: "unit", Seconds: 1, Outcome: "pass", Tests: SuiteTests{Counts: Counts{Pass: 1}}}}},
	}
	report := Build(records, time.Time{}, now)
	markdown := RenderMarkdown(report, "")
	if !strings.Contains(markdown, "Test time looks stable") {
		t.Fatalf("expected the no-growth message, got:\n%s", markdown)
	}
}
