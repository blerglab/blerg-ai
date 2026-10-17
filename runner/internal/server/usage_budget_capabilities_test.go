package server

import (
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Spend windows can have no known reset and must survive the descriptor boundary.
func TestUsageLimitsPreserveDeclaredSpendWindows(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	clean, ok := sanitizeUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{
		{ID: "month", UsedPercent: 0.4},
		{ID: "day", UsedPercent: 23},
		{ID: "spend-week", UsedPercent: 52},
		{ID: "capped", UsedPercent: 100},
	}}, now)
	if !ok || len(clean.Windows) != 4 {
		t.Fatalf("spend windows discarded: %+v, accepted=%v", clean, ok)
	}
	for i, want := range []string{"month", "day", "spend-week", "capped"} {
		if clean.Windows[i].ID != want || !clean.Windows[i].ResetsAt.IsZero() {
			t.Fatalf("window %d = %+v; must retain unknown reset", i, clean.Windows[i])
		}
	}
	if clean.Windows[0].UsedPercent != 0.4 {
		t.Fatalf("fractional spend utilization lost: %v", clean.Windows[0])
	}
	codex, ok := sanitizeUsageLimits(protocol.UsageLimits{Provider: "codex", Windows: []protocol.UsageWindow{
		{ID: "month", UsedPercent: 7}, {ID: "5h", UsedPercent: 38},
	}}, now)
	if !ok || len(codex.Windows) != 1 || codex.Windows[0].ID != "5h" {
		t.Fatalf("Codex must retain only its advertised windows: %+v", codex)
	}
}

func TestUsageLimitsSpendWindowsAreAdapterDeclared(t *testing.T) {
	t.Cleanup(engines.Register(engines.Descriptor{ID: "fixture-budget", Name: "Fixture", UsageWindows: []string{"month"}}))
	clean, ok := sanitizeUsageLimits(protocol.UsageLimits{Provider: "fixture-budget", Windows: []protocol.UsageWindow{
		{ID: "month", UsedPercent: 13}, {ID: "5h", UsedPercent: 91},
	}}, time.Now())
	if !ok || len(clean.Windows) != 1 || clean.Windows[0].ID != "month" {
		t.Fatalf("spend support must follow the adapter, not provider names: %+v", clean)
	}
}
