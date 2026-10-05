package server

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func stageEvent(session string, seq int64, sec int, plan bool, runtime string, stages ...protocol.StartStage) db.InsightEvent {
	raw, _ := json.Marshal(protocol.StartStagePayload{Plan: plan, Runtime: runtime, Stages: stages})
	return db.InsightEvent{SessionID: session, Seq: seq, Kind: "start_stage", Ts: at(sec), Payload: string(raw)}
}

func st(id, state string) protocol.StartStage { return protocol.StartStage{ID: id, State: state} }

// clusterPlan is the ordered plan a cluster start opens an attempt with.
func clusterPlan() []protocol.StartStage {
	return []protocol.StartStage{
		st("queued", "active"), st("schedule", "pending"), st("image", "pending"), st("connect", "pending"),
		st("clone", "pending"), st("plugins", "pending"), st("engine", "pending"), st("ready", "pending"),
	}
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.001 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// A start as it really is recorded: a plan, then updates that merge by id; a stage that is jumped over by a
// later one counts as done at that moment (the viewer's "furthest active/done" rule).
func TestAnalyzeAttemptsWalksTheStagesByTheFurthestRule(t *testing.T) {
	events := []db.InsightEvent{
		stageEvent("s1", 1, 0, true, "cluster", clusterPlan()...),
		stageEvent("s1", 2, 0, false, "", st("queued", "done"), st("schedule", "active")),
		stageEvent("s1", 3, 1, false, "", st("connect", "done"), st("clone", "active")),
		stageEvent("s1", 4, 151, false, "", st("clone", "done"), st("plugins", "active")),
		stageEvent("s1", 5, 241, false, "", st("plugins", "warning"), st("engine", "active")),
		stageEvent("s1", 6, 243, false, "", st("ready", "done")),
	}
	got := analyzeAttempts(events, func(string) string { return "cluster" })
	if len(got) != 1 {
		t.Fatalf("attempts = %d, want 1", len(got))
	}
	a := got[0]
	if !a.Ok || a.Resume || a.Runtime != "cluster" || a.FailedStage != "" {
		t.Fatalf("attempt = %+v", a)
	}
	near(t, "time to ready", a.Seconds(), 243)
	near(t, "queued", a.Steps["queued"], 0)
	near(t, "schedule", a.Steps["schedule"], 1)
	near(t, "image (skipped over)", a.Steps["image"], 0)
	near(t, "clone", a.Steps["clone"], 150)
	near(t, "plugins", a.Steps["plugins"], 90)
	near(t, "engine", a.Steps["engine"], 2)
	if _, ok := a.Steps["ready"]; ok {
		t.Error("ready is the end of an attempt, not a step with a duration")
	}
}

func TestAnalyzeAttemptsRecordsAFailureAndAResume(t *testing.T) {
	events := []db.InsightEvent{
		// a start that fails while queued
		stageEvent("s1", 1, 0, true, "cluster", clusterPlan()...),
		stageEvent("s1", 2, 0, false, "", st("queued", "failed")),
		// the same session resumed later, and this one works
		stageEvent("s1", 3, 600, true, "cluster", clusterPlan()...),
		stageEvent("s1", 4, 601, false, "", st("queued", "done"), st("schedule", "active")),
		stageEvent("s1", 5, 640, false, "", st("ready", "done")),
		// another session whose first events in the range are updates of an attempt that began earlier
		stageEvent("s2", 1, 5, false, "", st("clone", "done"), st("plugins", "active")),
	}
	got := analyzeAttempts(events, func(string) string { return "cluster" })
	if len(got) != 2 {
		t.Fatalf("attempts = %d, want 2 (updates with no plan are ignored): %+v", len(got), got)
	}
	if got[0].Ok || got[0].FailedStage != "queued" || got[0].Resume {
		t.Errorf("first attempt = %+v, want a failure at queued", got[0])
	}
	if !got[1].Ok || !got[1].Resume {
		t.Errorf("second attempt = %+v, want a successful resume", got[1])
	}
	near(t, "resume time to ready", got[1].Seconds(), 40)
}

func TestAnalyzeAttemptsKeepsSessionsApart(t *testing.T) {
	events := []db.InsightEvent{
		stageEvent("a", 1, 0, true, "cluster", clusterPlan()...),
		stageEvent("a", 2, 10, false, "", st("ready", "done")),
		stageEvent("b", 1, 100, true, "docker", st("queued", "active"), st("engine", "pending"), st("ready", "pending")),
		stageEvent("b", 2, 103, false, "", st("ready", "done")),
	}
	got := analyzeAttempts(events, func(id string) string {
		if id == "a" {
			return "cluster"
		}
		return "docker"
	})
	if len(got) != 2 || got[0].SessionID != "a" || got[1].SessionID != "b" {
		t.Fatalf("attempts = %+v", got)
	}
	near(t, "a", got[0].Seconds(), 10)
	near(t, "b", got[1].Seconds(), 3)
	if got[1].Runtime != "docker" {
		t.Errorf("runtime = %q", got[1].Runtime)
	}
}

func statusEvent(session string, seq int64, sec int, status string) db.InsightEvent {
	return db.InsightEvent{SessionID: session, Seq: seq, Kind: "status_changed", Ts: at(sec),
		Payload: `{"status":"` + status + `","reason":"x"}`}
}

func TestStatusSecondsSplitsBusyAndIdleAndStopsAtTheEnd(t *testing.T) {
	end := at(200)
	sessions := map[string]db.InsightSession{
		"s1": {ID: "s1", Status: "stopped", StartedAt: at(0), EndedAt: &end},
		"s2": {ID: "s2", Status: "idle", StartedAt: at(0)}, // still alive: counted up to "now"
	}
	events := []db.InsightEvent{
		statusEvent("s1", 1, 0, "running"),
		statusEvent("s1", 2, 60, "idle"),
		statusEvent("s1", 3, 100, "running"),
		statusEvent("s1", 4, 130, "waiting"),
		statusEvent("s1", 5, 150, "stopped"), // terminal: nothing counts after it
		statusEvent("s2", 1, 0, "running"),
		statusEvent("s2", 2, 20, "idle"),
	}
	running, waiting, idle := statusSeconds(events, sessions, at(0), at(1000), at(50))
	near(t, "running", running, 60+30+20)
	near(t, "waiting", waiting, 20)
	near(t, "idle", idle, 40+30) // s1 idle 60..100, s2 idle 20..50 (now)
}

func TestStatusSecondsClipsToTheRange(t *testing.T) {
	events := []db.InsightEvent{statusEvent("s1", 1, 0, "running"), statusEvent("s1", 2, 100, "idle")}
	sessions := map[string]db.InsightSession{"s1": {ID: "s1", Status: "idle", StartedAt: at(0)}}
	running, _, idle := statusSeconds(events, sessions, at(50), at(150), at(150))
	near(t, "running (50..100)", running, 50)
	near(t, "idle (100..150)", idle, 50)
}

func TestQuantilesUseLinearInterpolation(t *testing.T) {
	q := quantilesOf([]float64{10, 1, 5, 2, 8, 3, 4, 9, 6, 7})
	if q.Count != 10 {
		t.Fatalf("count = %d", q.Count)
	}
	near(t, "p50", q.P50, 5.5)
	near(t, "p90", q.P90, 9.1)
	near(t, "max", q.Max, 10)
	if z := quantilesOf(nil); z.Count != 0 || z.P50 != 0 || z.Max != 0 {
		t.Errorf("empty = %+v", z)
	}
	one := quantilesOf([]float64{42})
	near(t, "single p90", one.P90, 42)
}

func TestCostUsesPerMillionPrices(t *testing.T) {
	price := db.ModelPrice{Model: "m", InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75}
	// 1M input, 100k output, 10M cache reads, 200k cache writes
	got := costUSD(1_000_000, 100_000, 10_000_000, 200_000, price)
	near(t, "cost", got, 3+1.5+3+0.75)
}

func TestSlowestKeepsTheLongestAndHidesWhatMayNotBeSeen(t *testing.T) {
	attempts := []startRecord{
		{SessionID: "a", Start: at(0), End: at(10), Ok: true, Runtime: "cluster"},
		{SessionID: "b", Start: at(0), End: at(300), Ok: true, Runtime: "cluster"},
		{SessionID: "c", Start: at(0), End: at(100), Ok: true, Runtime: "cluster"},
		{SessionID: "d", Start: at(0), End: at(50), Ok: false, Runtime: "cluster"},
	}
	visible := func(id string) bool { return id != "c" } // c is somebody else's private session
	got := slowestStarts(attempts, 2, visible)
	if len(got) != 2 || got[0].SessionID != "b" || got[1].SessionID != "a" {
		t.Fatalf("slowest = %+v, want b then a (c hidden, d not successful)", got)
	}
}
