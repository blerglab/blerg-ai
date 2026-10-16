package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The daemon's report is percentages and reset times only: the wire shape the
// server and the sidebar read.
func TestUsageLimitsWireShape(t *testing.T) {
	reset := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(UsageLimits{
		Type: "usage_limits", Provider: "codex",
		Windows:   []UsageWindow{{ID: "5h", UsedPercent: 42, ResetsAt: reset}, {ID: "7d", UsedPercent: 7.5}},
		FetchedAt: reset.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"usage_limits","provider":"codex","windows":[{"id":"5h","used_percent":42,"resets_at":"2026-10-16T14:00:00Z"},{"id":"7d","used_percent":7.5}],"fetched_at":"2026-10-16T13:00:00Z"}`
	if string(raw) != want {
		t.Fatalf("usage_limits =\n%s\nwant\n%s", raw, want)
	}
}

// A daemon with nothing reported carries no usage_limits key in its DaemonInfo.
func TestDaemonInfoOmitsAbsentUsageLimits(t *testing.T) {
	raw, _ := json.Marshal(DaemonInfo{ID: "d1"})
	if strings.Contains(string(raw), "usage_limits") {
		t.Fatalf("absent limits were marshalled: %s", raw)
	}
	raw, _ = json.Marshal(DaemonLimitsChanged{Type: "daemon_limits_changed", DaemonID: "d1"})
	if !strings.Contains(string(raw), `"usage_limits":[]`) && !strings.Contains(string(raw), `"usage_limits":null`) {
		t.Fatalf("daemon_limits_changed must always say its list: %s", raw)
	}
}
