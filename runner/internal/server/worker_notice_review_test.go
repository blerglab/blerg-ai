package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestEveryChildTurnRequiresWorkerNotice(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	p := runnerPrincipal{Kind: "human", Sub: "owner-1"}
	for _, text := range []string{"hostile", workerNotice(f.parent, childTurnFinished, f.parent)} {
		if problem := f.api.sendRelay(t.Context(), p, f.parent, text, childTurnSource); problem == nil || problem.Status != 500 {
			t.Fatal("raw child relay accepted")
		}
		if _, problem := f.api.deliverMessage(t.Context(), p, f.parent, text, childTurnSource); problem == nil || problem.Status != 500 {
			t.Fatal("raw child delivery accepted")
		}
	}
}

func TestWorkerReplayChargesOnlyFirstDelivery(t *testing.T) {
	f := newDelegFx(t)
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child := f.childOf(f.parent)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.parent, Source: childTurnSource, ClientID: "child-question-review-replay", Text: workerNotice(child, childTurnAsked, newUUID())}
	if _, err := acceptSessionInput(t.Context(), f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		replaySessionInputs(t.Context(), f.hub, f.pool, f.dc, f.parent)
		if received := f.parentNote(f.parent, time.Second); received.ClientID != msg.ClientID {
			t.Fatal("retry did not deliver the pending logical input")
		}
	}
	var wakes int
	if err := f.pool.QueryRow(t.Context(), `SELECT wakes FROM child_relay_budgets WHERE child_id=$1`, child).Scan(&wakes); err != nil || wakes != 1 {
		t.Fatalf("replay charged %d times: %v", wakes, err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT wakes FROM main_relay_budgets WHERE parent_id=$1`, f.parent).Scan(&wakes); err != nil || wakes != 1 {
		t.Fatalf("Main replay charged %d times: %v", wakes, err)
	}
}

func TestWorkerFailedDispatchRefundsBudgets(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	msg := protocol.AgentUserMessage{SessionID: f.parent, Text: workerNotice(child, childTurnAsked, newUUID()), ClientID: "retry"}
	// An unregistered replacement connection cannot accept a frame.
	dc := &DaemonConn{ID: f.dc.ID}
	if sendWorkerFrame(t.Context(), f.hub, f.pool, dc, msg, []byte(`{}`)) {
		t.Fatal("disconnected frame accepted")
	}
	var n int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM child_relay_budgets)+(SELECT count(*) FROM main_relay_budgets)+(SELECT count(*) FROM worker_wake_receipts)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed send burned budget: %d %v", n, err)
	}
}

func TestWorkerClusterConflictDoesNotChargeWake(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster',status='disconnected' WHERE id=$1`, f.parent); err != nil {
		t.Fatal(err)
	}
	problem := f.api.sendWorkerNotice(t.Context(), runnerPrincipal{Kind: "human", Sub: "owner-1"}, f.parent, workerEvent{child, childTurnAsked, newUUID()}, "child-question-disconnected")
	if problem == nil || problem.Status != 409 {
		t.Fatalf("expected disconnected conflict: %v", problem)
	}
	var n int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM child_relay_budgets)+(SELECT count(*) FROM main_relay_budgets)+(SELECT count(*) FROM worker_wake_receipts)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("409 charged a wake: %d %v", n, err)
	}
}

func TestWorkerUnknownLegacyReplaySettles(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprint(deleted), func(t *testing.T) {
			f := newDelegFx(t)
			f.dc.SetInputReceipts(true)
			f.hub.SetSessionOwner(f.parent, f.dc.ID)
			text := "Worker event: unavailable. Read the worker list explicitly."
			if deleted {
				text = workerNotice(newUUID(), childTurnFinished, newUUID())
			}
			msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.parent, Source: childTurnSource, ClientID: "child-turn-legacy", Text: text}
			if _, err := acceptSessionInput(t.Context(), f.hub, f.pool, msg); err != nil {
				t.Fatal(err)
			}
			replaySessionInputs(t.Context(), f.hub, f.pool, f.dc, f.parent)
			received := f.parentNote(f.parent, time.Second)
			if received.Text != text {
				t.Fatal("legacy notice lost")
			}
			payload, _ := json.Marshal(map[string]string{"client_id": msg.ClientID})
			if err := settleSessionInput(t.Context(), f.pool, protocol.AgentEvent{SessionID: f.parent, Kind: "user_message", Payload: payload}); err != nil {
				t.Fatal(err)
			}
			pending, err := db.HasPendingSessionInputs(t.Context(), f.pool, f.parent)
			if err != nil || pending {
				t.Fatalf("legacy input never settled: %v", err)
			}
		})
	}
}

func TestWorkerResultBoundsAndPersonPrivacy(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	for range 23 {
		if _, err := db.CreateQuestion(t.Context(), f.pool, child, strings.Repeat("x", 5000)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.ListWorkerAsks(t.Context(), f.pool, child, true)
	if err != nil || len(rows) != 21 {
		t.Fatalf("database question count was not bounded: %d %v", len(rows), err)
	}
	for _, row := range rows {
		if len([]rune(row.Body)) != 4097 {
			t.Fatal("database question body was not bounded")
		}
	}
	for _, by := range []string{"person:owner-1", "session:" + f.parent} {
		id, err := db.CreateQuestion(t.Context(), f.pool, child, "Outcome")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.pool.Exec(t.Context(), `UPDATE messages SET status='answered',answer=$2,answered_by=$3,person_required=$4 WHERE id=$1`, id, "answer-"+by, by, strings.HasPrefix(by, "person:")); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ := f.do("GET", childRoute(f.parent, child, "/result"), f.parentTok, nil, nil)
	if code != 200 || out["truncated"] != true || len(out["open_asks"].([]any)) != 20 {
		t.Fatalf("unbounded asks: status=%d truncated=%v", code, out["truncated"])
	}
	for _, ask := range out["open_asks"].([]any) {
		if len(ask.(map[string]any)["body"].(string)) != 4096 {
			t.Fatal("body not bounded")
		}
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "answer-person:") || !strings.Contains(string(raw), "answer-session:") {
		t.Fatal("person answer exposed or Main answer lost")
	}
	for _, guidance := range []string{"Check its work before resuming", "Only the person can decide"} {
		if !strings.Contains(string(raw), guidance) {
			t.Fatal("result guidance missing")
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET delegation_via='person' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/result", "/events"} {
		code, _, _ = f.do("GET", childRoute(f.parent, child, suffix), f.parentTok, nil, nil)
		if code != 403 {
			t.Fatalf("person channel %s readable: %d", suffix, code)
		}
	}
}

func TestMainWakeCapReservesQuestionAndFinishCapacity(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	p := runnerPrincipal{Kind: "human", Sub: "owner-1"}
	for i := range 32 {
		kind := "question_resolved"
		if i >= 8 {
			kind = childTurnAsked
		}
		if i >= 20 {
			kind = childTurnFinished
		}
		child := f.childOf(f.parent)
		if problem := f.api.sendWorkerNotice(t.Context(), p, f.parent, workerEvent{child, kind, newUUID()}); problem != nil {
			t.Fatalf("priority slot %d denied: %v", i, problem)
		}
		_ = f.parentNote(f.parent, time.Second)
		if i == 7 {
			if problem := f.api.sendWorkerNotice(t.Context(), p, f.parent, workerEvent{child, "question_resolved", newUUID()}); problem == nil {
				t.Fatal("chatter used reserved slots")
			}
		}
	}
	if problem := f.api.sendWorkerNotice(t.Context(), p, f.parent, workerEvent{f.childOf(f.parent), childTurnAsked, newUUID()}); problem == nil {
		t.Fatal("Main flood exceeded cap")
	}
}

func TestWorkerChannelClosedRetriesAfterThrottle(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET delegation_via='person' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(channelRecommendation{ID: newUUID(), State: "approved", ChannelID: child})
	if _, _, err := db.AppendAgentEvent(t.Context(), f.pool, f.parent, newUUID(), "channel_recommendation", string(raw)); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if problem := claimWorkerWake(t.Context(), f.pool, child); problem != nil {
			t.Fatal(problem)
		}
	}
	// Simulate a restart after the durable end but before the hook records an intent.
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET status='ended',ended_at=statement_timestamp() WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
	var delivered bool
	if err := f.pool.QueryRow(t.Context(), `SELECT delivered FROM channel_close_notices WHERE child_id=$1`, child).Scan(&delivered); err != nil || delivered {
		t.Fatalf("throttled close lost: %v", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE child_relay_budgets SET window_start=statement_timestamp()-interval '2 minutes' WHERE child_id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
	if note := f.parentNote(f.parent, time.Second); !strings.Contains(note.Text, "kind=channel_closed") {
		t.Fatal("close did not retry")
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT delivered FROM channel_close_notices WHERE child_id=$1`, child).Scan(&delivered); err != nil || !delivered {
		t.Fatalf("close never settled: %v", err)
	}
}

func TestWorkerChatterReservesQuestionAndFinishCapacity(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child := f.childOf(f.parent)
	p := runnerPrincipal{Kind: "human", Sub: "owner-1"}
	for range 4 {
		if problem := f.api.sendWorkerNotice(t.Context(), p, f.parent, workerEvent{child, "question_resolved", newUUID()}); problem != nil {
			t.Fatal(problem)
		}
		_ = f.parentNote(f.parent, time.Second)
	}
	if problem := f.api.sendWorkerNotice(t.Context(), p, f.parent, workerEvent{child, "question_resolved", newUUID()}); problem == nil {
		t.Fatal("worker chatter consumed priority capacity")
	}
	for _, kind := range []string{childTurnAsked, childTurnFinished} {
		if problem := f.api.sendWorkerNotice(t.Context(), p, f.parent, workerEvent{child, kind, newUUID()}); problem != nil {
			t.Fatal(problem)
		}
		_ = f.parentNote(f.parent, time.Second)
	}
}
