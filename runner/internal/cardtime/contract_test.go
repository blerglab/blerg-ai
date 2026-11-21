package cardtime

// Contract tests for ActiveSecondsFromTiming, using the real event shapes activity.SplitOrdered
// consumes (tool_call/tool_result/status_changed/turn_done JSON payloads, the same wire shape the
// server actually writes to agent_events), not synthetic structs. Required by the acceptance
// criteria: "Contract tests for totals use real event shapes (empty, single worker, restart
// mid-turn, unlinked work)." "Unlinked work" itself is a DB-layer attribution concern (which
// session rows get included), not a timing-computation one, and is covered by the db-layer tests
// alongside H1; this file covers the timing side: empty, single worker, and restart mid-turn.

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/activity"
)

func mustEvent(t *testing.T, kind string, at time.Time, payload map[string]any) activity.Event {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal event payload: %v", err)
	}
	return activity.Event{At: at, Kind: kind, Payload: raw}
}

func TestContractEmptySessionHasNoActiveSeconds(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	timing := activity.SplitOrdered(start, end, slices.Values[[]activity.Event](nil), nil)
	active, unknown := ActiveSecondsFromTiming(timing)
	if active != 0 {
		t.Fatalf("empty session: got active=%v, want 0", active)
	}
	// An empty session's whole window is unclassified ("unknown"), which must disqualify the
	// whole card's total (CardTotal), not read as zero active work.
	if unknown != end.Sub(start).Seconds() {
		t.Fatalf("empty session: got unknown=%v, want the full window (%v)", unknown, end.Sub(start).Seconds())
	}
}

// A single worker running one real tool call (started, status running, result, turn_done) for a
// known duration produces that duration as active seconds and no unknown time.
func TestContractSingleWorkerToolCall(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	toolStart := start.Add(time.Second)
	toolEnd := toolStart.Add(10 * time.Minute)
	end := toolEnd.Add(time.Second)
	events := []activity.Event{
		mustEvent(t, "status_changed", start, map[string]any{"status": "running"}),
		mustEvent(t, "tool_call", toolStart, map[string]any{"call_id": "c1", "tool": "Read", "input": map[string]any{}}),
		mustEvent(t, "tool_result", toolEnd, map[string]any{"call_id": "c1"}),
		mustEvent(t, "turn_done", end, map[string]any{}),
	}
	timing := activity.SplitOrdered(start, end, slices.Values(events), nil)
	active, unknown := ActiveSecondsFromTiming(timing)
	if unknown != 0 {
		t.Fatalf("single worker: got unknown=%v, want 0", unknown)
	}
	if active <= 0 || active > end.Sub(start).Seconds() {
		t.Fatalf("single worker: got active=%v, want a positive share of the %v window", active, end.Sub(start).Seconds())
	}
}

// Restart mid-turn: a tool_call with no matching tool_result (the daemon never got to send it
// before the restart) must close at the window end, not run unbounded or panic, and must not be
// silently dropped from the active total — it is attributed up to the point of data loss.
func TestContractRestartMidTurnClosesOpenSpanAtWindowEnd(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	toolStart := start.Add(time.Second)
	end := toolStart.Add(5 * time.Minute) // the window the caller recomputes up to; no tool_result ever arrives
	events := []activity.Event{
		mustEvent(t, "status_changed", start, map[string]any{"status": "running"}),
		mustEvent(t, "tool_call", toolStart, map[string]any{"call_id": "c1", "tool": "Bash", "input": map[string]any{"command": "sleep 999"}}),
	}
	timing := activity.SplitOrdered(start, end, slices.Values(events), nil)
	active, _ := ActiveSecondsFromTiming(timing)
	if timing.IncompleteTools != 1 {
		t.Fatalf("restart mid-turn: got IncompleteTools=%d, want 1 (marks the total approximate)", timing.IncompleteTools)
	}
	if active <= 0 || active > end.Sub(start).Seconds() {
		t.Fatalf("restart mid-turn: got active=%v, want a bounded positive share of the %v window, not unbounded or zero", active, end.Sub(start).Seconds())
	}
}
