package server

// H2 cross-service test, through the real notice path (BLG-444's acceptance criterion: "one
// cross-service test runs through the real notice path"): a worker's own turn ends, crosses its
// card's first budget multiple, and the plan-drift fact is delivered to its ROOT Main's next
// input — never the worker's own.
//
// Codex round 3 (H): checkPlanTurn no longer reads card-time itself — that read raced BLG-424's
// own async materialization of the worker's timing and could see "pending" forever, with nothing
// ever retrying. The crossing check now runs from db.CheckCardTimeCrossing, called only once the
// timing has actually landed: from the insight-timing queue drain
// (db.ProcessQueuedInsightTimings) and from the ended-session backfill. This test drives the
// production order exactly: append turn_done (which, in production, is what queues the
// materialization — agentevents.go), queue it and drain the queue the same way
// RunInsightTimingBackfill's own goroutine would, and only then look for the notice. No manual
// RefreshInsightTiming and no direct call into checkPlanTurn/checkCardTimeCrossing.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestCardTimeCrossingDeliversToRootMainNotWorker(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	ctx := context.Background()
	worker := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(worker, fx.dc.ID)

	now := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	// Link BEFORE the run: BLG-424's own invalidate_insight_links trigger clears
	// session_insight_timings for a session the moment a NEW session_card_links row is written for
	// it, so linking first (matching real operation — a session is pinned before or during its
	// work) means the materialization below is never undone by this insert.
	if _, err := fx.pool.Exec(ctx, `INSERT INTO session_card_links VALUES($1,'board-x',42,'pin',$2)`, worker, now); err != nil {
		t.Fatal(err)
	}
	resultAt := now.Add(5 * time.Hour)
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET started_at=$2,ended_at=$3 WHERE id=$1`, worker, now, resultAt); err != nil {
		t.Fatal(err)
	}
	// One long tool call spanning past the default 4h budget interval.
	if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, worker, newUUID(), "status_changed", `{"status":"running"}`, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, worker, newUUID(), "tool_call", `{"call_id":"c1","tool":"Bash","input":{"command":"make build"}}`, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, worker, newUUID(), "tool_result", `{"call_id":"c1"}`, resultAt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, worker, newUUID(), "turn_done", `{}`, resultAt); err != nil {
		t.Fatal(err)
	}
	// Production order from here: turn_done only QUEUES the refresh (agentevents.go); draining the
	// queue is what materializes the timing AND (now) runs the crossing check for worker's cards.
	db.QueueInsightTiming(fx.pool, worker)
	if got := db.ProcessQueuedInsightTimings(ctx, fx.pool); got != 1 {
		t.Fatalf("got %d jobs processed, want 1", got)
	}

	// preparePlanNudgeMode's delivery recheck reads the Main's own latest turn_done to recompute
	// "current" drift; Main needs one of its own for the delivery path to proceed at all.
	if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, fx.parent, newUUID(), "turn_done", `{}`, resultAt); err != nil {
		t.Fatal(err)
	}

	mainNudge := planCarryingInput(t, fx.api, fx.parent, true)
	if !strings.Contains(mainNudge, "board-x#42") || !strings.Contains(mainNudge, "4h") {
		t.Fatalf("Main's next input did not carry the card-time notice: %q", mainNudge)
	}
	if strings.Contains(mainNudge, worker) {
		t.Fatalf("notice named the worker's session id, want ids and numbers only: %q", mainNudge)
	}

	workerNudge := planCarryingInput(t, fx.api, worker, true)
	if strings.Contains(workerNudge, "board-x#42") {
		t.Fatalf("the worker's own next input must never carry its own card-time notice (H2): %q", workerNudge)
	}
}
