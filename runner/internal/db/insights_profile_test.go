package db

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func TestInsightsSlowDiagnosticsAreBounded(t *testing.T) {
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(previous)
	insightsSlow.last = map[InsightsOperation]time.Time{}
	insightsSlow.suppressed = map[InsightsOperation]int{}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	recordSlowInsights(InsightsTiming, 50*time.Millisecond, now)
	if out.Len() != 0 {
		t.Fatal("fast query logged")
	}
	recordSlowInsights(InsightsTiming, 250*time.Millisecond, now)
	recordSlowInsights(InsightsTiming, 300*time.Millisecond, now.Add(time.Second))
	recordSlowInsights(InsightsTiming, 400*time.Millisecond, now.Add(time.Minute))
	if strings.Count(out.String(), "insights: slow") != 2 || !strings.Contains(out.String(), "suppressed=1") || !strings.Contains(out.String(), "operation=timing") {
		t.Fatalf("diagnostics: %s", out.String())
	}
	before := out.Len()
	recordSlowInsights(InsightsOperation(999), time.Second, now)
	if out.Len() != before {
		t.Fatal("unregistered operation logged")
	}
}
