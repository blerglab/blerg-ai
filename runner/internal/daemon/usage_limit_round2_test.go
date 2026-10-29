package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestClaudeModelLimitWindows(t *testing.T) {
	for _, model := range []string{"Opus", "Sonnet", "Fable"} {
		for _, wording := range []string{model + " limit", model + " weekly limit", "weekly " + model + " limit"} {
			if got := claudeLimitWindow("You've hit your " + wording); got != "7d_"+strings.ToLower(model) {
				t.Errorf("%s: %s", wording, got)
			}
		}
	}
}

func TestClaudeModelMeterWindows(t *testing.T) {
	var answer claudeUsageAnswer
	if err := json.Unmarshal([]byte(`{"seven_day":{"utilization":40},"seven_day_opus":{"utilization":100,"resets_at":"2026-10-30T10:00:00Z"},"seven_day_sonnet":{"utilization":99.6},"seven_day_fable":{"utilization":20}}`), &answer); err != nil {
		t.Fatal(err)
	}
	got := claudeUsageWindows(answer)
	if len(got) != 4 {
		t.Fatalf("model windows missing: %+v", got)
	}
	for i, id := range []string{"7d", "7d_opus", "7d_sonnet", "7d_fable"} {
		if got[i].ID != id {
			t.Errorf("%d: %+v", i, got[i])
		}
	}
	if got[1].UsedPercent != 100 || got[1].ResetsAt.IsZero() {
		t.Fatal("model cap/reset lost")
	}
}

func TestPrintedResetJustAfterMidnight(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 1, 30, 0, time.UTC)
	want := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	got, ok := claudeRateLimitReset("resets 11:59pm (UTC)", now)
	if !ok || !got.Equal(want) {
		t.Errorf("Claude: %v %v, want %v", got, ok, want)
	}
	p, ok := codexUsageRefusalAt(codexEvent{Type: "error", Message: "You've hit your usage limit. Try again at 11:59 PM."}, "turn", now)
	if !ok || p.ResetAt == nil || !p.ResetAt.Equal(want) {
		t.Errorf("Codex: %+v %v, want %v", p, ok, want)
	}
	got, ok = claudeRateLimitReset("resets 11:59pm (UTC)", now.Add(time.Hour))
	if !ok || !got.Equal(want.Add(24*time.Hour)) {
		t.Errorf("next reset: %v %v", got, ok)
	}
}
