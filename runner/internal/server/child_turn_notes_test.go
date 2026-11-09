package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// parentNote waits for an agent_user_message to sessionID on fx.dc; "" after wait with none.
func (fx *delegFx) parentNote(sessionID string, wait time.Duration) protocol.AgentUserMessage {
	fx.t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case raw := <-fx.dc.send:
			var msg protocol.AgentUserMessage
			if json.Unmarshal(raw, &msg) == nil && msg.Type == "agent_user_message" && msg.SessionID == sessionID {
				return msg
			}
		case <-deadline:
			return protocol.AgentUserMessage{}
		}
	}
}

func (fx *delegFx) childEvent(child, _, kind, payload string) {
	fx.t.Helper()
	fx.childEventAt(child, kind, payload, time.Now())
}

// childEventAt is childEvent with an explicit event time, so a test can simulate the
// childTurnFinishedGrace window having already elapsed (BLG-447) without a real sleep.
func (fx *delegFx) childEventAt(child, kind, payload string, at time.Time) {
	fx.t.Helper()
	id := newUUID()
	HandleAgentEvent(context.Background(), fx.hub, fx.pool, fx.dc, protocol.AgentEvent{
		Type: "agent_event", SessionID: child, ClientEventID: id, Kind: kind, Payload: json.RawMessage(payload),
		Ts: at.UTC().Format(time.RFC3339Nano),
	})
}

// backdateChildTurnNotice pushes child's queued (undelivered) notice's queued_at into the past by
// `by`, so a test can simulate childTurnFinishedGrace/Ceiling having elapsed without a real sleep.
// BLG-447 review: the window is measured from the server's own queued_at (migration 145), not a
// daemon-reported event ts, so backdating the triggering event itself has no effect on it.
func (fx *delegFx) backdateChildTurnNotice(child string, by time.Duration) {
	fx.t.Helper()
	if _, err := fx.pool.Exec(context.Background(), `UPDATE child_turn_notices SET queued_at=$2 WHERE child_id=$1 AND NOT delivered`, child, time.Now().Add(-by)); err != nil {
		fx.t.Fatal(err)
	}
}

// childTurnDoneReady fires a turn_done, then backdates its queued notice past
// childTurnFinishedGrace and forces a reconciliation pass (BLG-447), for a test whose point is
// something other than that grace window: it must see its genuine finish delivered without a real
// wait.
func (fx *delegFx) childTurnDoneReady(child, stopReason string) {
	fx.t.Helper()
	fx.childEvent(child, "", "turn_done", `{"stop_reason":"`+stopReason+`"}`)
	fx.backdateChildTurnNotice(child, 2*childTurnFinishedGrace)
	if err := fx.api.reconcileChildTurns(context.Background()); err != nil {
		fx.t.Fatal(err)
	}
}

// A child's finished turn reaches its parent once, as a note from Blerg naming the child, the
// first line of its answer and how to read the result.
func TestParentIsToldWhenAChildTurnEnds(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.hub.SetSessionOwner(child, fx.dc.ID)

	fx.childEvent(child, "u1", "user_message", `{"text":"do the thing","source":"session"}`)
	fx.childEvent(child, "a1", "assistant_text", `{"text":"All done here.\nDetails follow.","done":true}`)
	fx.childEvent(child, "t1", "turn_done", `{"stop_reason":"end_turn"}`)
	note := fx.parentNote(fx.parent, 10*time.Second)
	want := "fixed finished worker notice"
	if note.Source != childTurnSource || !isWorkerNotice(note.Text, child, childTurnFinished) {
		t.Fatalf("parent note = %+v, want %q from %s", note, want, childTurnSource)
	}

	// The same turn reported again (a second turn_done, the session erroring) tells nobody twice.
	fx.childEvent(child, "t1b", "turn_done", `{"stop_reason":"error"}`)
	if again := fx.parentNote(fx.parent, 500*time.Millisecond); again.Type != "" {
		t.Fatalf("a second note for the same turn: %+v", again)
	}

	// The next turn is news again; an interrupted one is not.
	fx.childEvent(child, "u2", "user_message", `{"text":"stop","source":"session"}`)
	fx.childEvent(child, "t2", "turn_done", `{"stop_reason":"interrupted"}`)
	if n := fx.parentNote(fx.parent, 500*time.Millisecond); n.Type != "" {
		t.Fatalf("an interrupted turn was relayed: %+v", n)
	}
	fx.childEvent(child, "u3", "user_message", `{"text":"again","source":"session"}`)
	fx.childEvent(child, "e3", "error", `{"message":"model unavailable"}`)
	fx.childEvent(child, "t3", "turn_done", `{"stop_reason":"error"}`)
	failed := fx.parentNote(fx.parent, 10*time.Second)
	if !strings.Contains(failed.Text, "kind=failed") {
		t.Fatalf("failed note = %q", failed.Text)
	}
}

// A question and an approval each say what the parent can do.
func TestChildTurnNoteForQuestionsAndApprovals(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	ctx := context.Background()

	fx.api.noteChildTurn(ctx, child, childTurnAsked, "Which \"branch\" should I use?\nmore")
	asked := fx.parentNote(fx.parent, 5*time.Second)
	if !isWorkerNotice(asked.Text, child, childTurnAsked) {
		t.Fatalf("question note = %q", asked.Text)
	}
	fx.api.noteChildTurn(ctx, child, childTurnApproval, "Run rm -rf build?")
	approval := fx.parentNote(fx.parent, 5*time.Second)
	if !strings.Contains(approval.Text, "kind=approval") {
		t.Fatalf("approval note = %q", approval.Text)
	}
}

// A parent that has ended is not woken, and a session that is not a child tells nobody.
func TestChildTurnNoteNeverWakesAnEndedParent(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET status='ended', ended_at=now() WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	fx.api.noteChildTurn(context.Background(), child, childTurnFinished, "")
	if n := fx.parentNote(fx.parent, 500*time.Millisecond); n.Type != "" {
		t.Fatalf("an ended parent was woken: %+v", n)
	}
	fx.api.noteChildTurn(context.Background(), fx.parent, childTurnFinished, "")
	if n := fx.parentNote(fx.parent, 300*time.Millisecond); n.Type != "" {
		t.Fatalf("a session without a parent sent a note: %+v", n)
	}
}

// No caller can write a child-turn note: the source is Blerg's.
func TestChildTurnSourceIsReserved(t *testing.T) {
	fx := newDelegFx(t)
	_, apiErr := fx.api.SendMessage(context.Background(), runnerPrincipal{Kind: "human", Sub: "owner-1"}, fx.parent, "Child \"x\" finished its turn", childTurnSource)
	if apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a caller wrote in Blerg's voice: %+v", apiErr)
	}
}

// A turn whose commits the daemon could not bring back (an error with code child_sync) is still a
// finished turn: the result keeps its answer, and the parent's note says the commits are missing.
func TestChildTurnNoteReportsAFailedSync(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	ctx := context.Background()

	fx.childEvent(child, "", "user_message", `{"text":"do it","source":"session"}`)
	fx.childEvent(child, "", "assistant_text", `{"text":"Committed the fix.","done":true}`)
	fx.childEvent(child, "", "error", `{"message":"This turn's commits were not brought back to wip/x: fetch refused","retryable":true,"code":"child_sync"}`)
	fx.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)

	if got := fx.api.lastTurnSyncProblem(ctx, child); !strings.Contains(got, "fetch refused") {
		t.Fatalf("lastTurnSyncProblem = %q", got)
	}
	answer, failure := fx.api.lastTurnAnswer(ctx, child)
	if answer != "Committed the fix." || failure != "" {
		t.Fatalf("the result took the sync notice for the turn's failure: answer %q, failure %q", answer, failure)
	}
	note := fx.parentNote(fx.parent, 10*time.Second)
	if !isWorkerNotice(note.Text, child, childTurnFinished) || strings.Contains(note.Text, "fetch refused") {
		t.Fatalf("parent note = %q", note.Text)
	}

	// The next turn, with nothing wrong, says nothing about commits.
	fx.childEvent(child, "", "user_message", `{"text":"again","source":"session"}`)
	fx.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	if got := fx.api.lastTurnSyncProblem(ctx, child); got != "" {
		t.Fatalf("an earlier turn's sync problem was read as the last turn's: %q", got)
	}
	if next := fx.parentNote(fx.parent, 10*time.Second); next.Source != childTurnSource || !strings.Contains(next.Text, "kind=finished") || strings.Contains(next.Text, "NOT brought back") {
		t.Fatalf("a clean turn's note reported missing commits: %q", next.Text)
	}

	// A turn that failed reports its own error, never the sync notice recorded after it.
	fx.childEvent(child, "", "user_message", `{"text":"third","source":"session"}`)
	fx.childEvent(child, "", "error", `{"message":"model unavailable"}`)
	fx.childEvent(child, "", "error", `{"message":"This turn's commits were not brought back to wip/x: fetch refused","retryable":true,"code":"child_sync"}`)
	fx.childEvent(child, "", "turn_done", `{"stop_reason":"error"}`)
	if _, failure := fx.api.lastTurnAnswer(ctx, child); failure != "model unavailable" {
		t.Fatalf("the failed turn reported %q, want its own error", failure)
	}
}

func TestParentIsToldWhenChildPausesAfterRestart(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	fx.childEvent(child, "u1", "user_message", `{"text":"build it","source":"session"}`)
	report := protocol.SessionPaused{Type: "session_paused", SessionID: child, Reason: protocol.PausedByRestart}
	// A report from a different daemon is ignored.
	foreign := &DaemonConn{ID: "another-daemon"}
	handleSessionPaused(t.Context(), fx.hub, fx.pool, foreign, report)
	if note := fx.parentNote(fx.parent, 100*time.Millisecond); note.Type != "" {
		t.Fatalf("foreign daemon relayed a pause: %+v", note)
	}
	handleSessionPaused(t.Context(), fx.hub, fx.pool, fx.dc, report)
	note := fx.parentNote(fx.parent, 5*time.Second)
	if note.Source != childTurnSource || !strings.Contains(note.Text, "kind=restart_paused") || strings.Contains(note.Text, "waiting for a message") {
		t.Fatalf("paused child was silent or misreported: %+v", note)
	}
	handleSessionPaused(t.Context(), fx.hub, fx.pool, fx.dc, report)
	if again := fx.parentNote(fx.parent, 300*time.Millisecond); again.Type != "" {
		t.Fatalf("duplicate pause: %+v", again)
	}
}

func TestChildRestartPauseReasonAndEndedParent(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	// A continued recovery is not a failed child turn and does not relay a pause.
	fx.childEvent(child, "", "error", `{"message":"recovering","code":"session_recovery","retryable":true}`)
	fx.childEvent(child, "", "user_message", `{"text":"[system] ↻ Updated to abc1234 · continued","source":"system"}`)
	if note := fx.parentNote(fx.parent, 100*time.Millisecond); note.Type != "" {
		t.Fatalf("continued child reported paused: %+v", note)
	}
	report := protocol.SessionPaused{SessionID: child, Reason: protocol.PausedByRestart, Detail: "check the interrupted command"}
	handleSessionPaused(t.Context(), fx.hub, fx.pool, fx.dc, report)
	if note := fx.parentNote(fx.parent, 5*time.Second); !strings.Contains(note.Text, "kind=restart_paused") {
		t.Fatalf("pause reason lost: %+v", note)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='ended',ended_at=now() WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	fx.api.noteChildTurn(t.Context(), child, childRestartPaused, "new pause")
	if note := fx.parentNote(fx.parent, 100*time.Millisecond); note.Type != "" {
		t.Fatalf("ended parent was woken: %+v", note)
	}
}

func TestPostedChildQuestionUsesRealParentNoticeChannel(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	child, token := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, title: "Question worker"})
	rec := privDo(fx.api.HandlePostMessages, "POST", "/x", "", token, `{"kind":"ask","body":"Which branch?"}`)
	if rec.Code != 201 {
		t.Fatal(rec.Body.String())
	}
	var posted struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &posted)
	note := fx.parentNote(fx.parent, 5*time.Second)
	for _, part := range []string{"Worker event:", posted.ID, "kind=asked", "session result " + child} {
		if !strings.Contains(note.Text, part) {
			t.Fatalf("real notice missing %q: %+v", part, note)
		}
	}
	if note.Source != childTurnSource {
		t.Fatalf("wrong notice channel: %q", note.Source)
	}
}

// A harness background completion can start a second turn without a new user input. BLG-447: the
// FIRST turn_done, with the background task still open, is transient — production evidence
// (worker 5a7b00c0) showed the real engine end a turn like this and resume on its own a moment
// later. The parent must hear nothing from it; only the later, genuine finish — background work
// actually done, the child's own final turn_done — is news.
func TestChildBackgroundContinuationReportsItsOwnTurn(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)
	f.childEvent(child, "", "user_message", `{"text":"Run the checks","source":"session"}`)
	f.childEvent(child, "", "background_task_started", `{"task_id":"fixture-task","call_id":"fixture-call","kind":"bash"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Checks are running in the background.","done":true}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	if n := f.parentNote(f.parent, 1*time.Second); n.Type != "" {
		t.Fatalf("a transient turn_done with the background task still open was reported finished: %+v", n)
	}
	// Past the grace window too, with the background task still open: it is the open work itself,
	// not just elapsed time, that must keep this held (BLG-447 review).
	f.backdateChildTurnNotice(child, 2*childTurnFinishedGrace)
	if err := f.api.reconcileChildTurns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.parentNote(f.parent, 500*time.Millisecond); n.Type != "" {
		t.Fatalf("grace elapsing alone released a notice with background work still open: %+v", n)
	}
	f.childEvent(child, "", "background_task_done", `{"task_id":"fixture-task","status":"completed"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"All checks passed.","done":true}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	if n := f.parentNote(f.parent, 10*time.Second); !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("background turn notice missing: %+v", n)
	}
}

func TestChildTurnIntentSurvivesRestartAndLostDeliveryReceipt(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)
	// The event commits and is acknowledged while the relay hook is unavailable.
	f.hub.SetChildTurnHook(nil)
	f.childEvent(child, "", "user_message", `{"text":"Finish the work","source":"session"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Work complete.","done":true}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	f.childEvent(child, "", "user_message", `{"text":"Check the result","source":"session"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Second report complete.","done":true}`)
	f.childEvent(child, "", "error", `{"code":"child_sync","message":"second turn delivery failed"}`)
	// Backdated past childTurnFinishedGrace (BLG-447): this test is about restart/receipt
	// durability, not about transience, so its genuine finish must already be ready to send.
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	f.backdateChildTurnNotice(child, 2*childTurnFinishedGrace)
	// The daemon reads its event acknowledgments before reconnecting.
	drain(f.dc.send)
	restarted := NewAPI(f.hub, f.pool, delegDaemonToken, nil, "")
	restarted.coreAuth = f.api.coreAuth
	if err := restarted.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
	n := f.parentNote(f.parent, 3*time.Second)
	if n.Source != childTurnSource || n.ClientID == "" || !strings.Contains(n.Text, "kind=finished") || strings.Contains(n.Text, "second turn delivery failed") {
		t.Fatalf("restart lost the latest coalesced report: %+v", n)
	}
	if extra := f.parentNote(f.parent, 100*time.Millisecond); extra.Type != "" {
		t.Fatalf("offline backlog woke parent twice: %+v", extra)
	}
	// Simulate a crash after durable acceptance but before the journal receipt.
	if _, err := f.pool.Exec(t.Context(), `UPDATE child_turn_notices SET delivered=false WHERE child_id=$1`, child); err != nil {
		t.Fatal(err)
	}
	restarted = NewAPI(f.hub, f.pool, delegDaemonToken, nil, "")
	restarted.coreAuth = f.api.coreAuth
	if err := restarted.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
	if duplicate := f.parentNote(f.parent, 100*time.Millisecond); duplicate.Type != "" {
		t.Fatalf("replay duplicated input: %+v", duplicate)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1 AND client_id=$2`, f.parent, n.ClientID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable inputs=%d: %v", count, err)
	}
}

func TestChildErrorStatusAndTurnEndShareOneNotice(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)
	f.childEvent(child, "", "user_message", `{"text":"Run the checks","source":"session"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Checking the work.","done":true}`)
	f.childEvent(child, "", "error", `{"message":"engine unavailable"}`)
	f.childEvent(child, "", "status_changed", `{"status":"error"}`)
	if n := f.parentNote(f.parent, 5*time.Second); n.Source != childTurnSource {
		t.Fatalf("failure notice missing: %+v", n)
	}
	f.childEvent(child, "", "turn_done", `{"stop_reason":"error"}`)
	if n := f.parentNote(f.parent, 300*time.Millisecond); n.Type != "" {
		t.Fatalf("same failure delivered twice: %+v", n)
	}
}

func TestChildTurnQueuedNoticeReplaysOnHeartbeat(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)
	f.hub.SetChildTurnHook(nil)
	f.childEvent(child, "", "user_message", `{"text":"Finish the work","source":"session"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Queued report complete.","done":true}`)
	f.childTurnDoneReady(child, "end_turn")
	// Simulate a connected daemon whose outbound socket is backpressured.
	func() {
		for {
			select {
			case f.dc.send <- []byte(`{"type":"fixture_queued"}`):
			default:
				return
			}
		}
	}()
	if err := f.api.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingSessionInputs(t.Context(), f.pool, f.parent)
	if err != nil || len(pending) != 1 {
		t.Fatalf("notice was not durably queued: %+v %v", pending, err)
	}
	drain(f.dc.send)
	replayPendingSessionInputs(t.Context(), f.hub, f.pool, f.dc, []string{f.parent})
	note := f.parentNote(f.parent, time.Second)
	if note.Source != childTurnSource || note.ClientID == "" || !strings.Contains(note.Text, "kind=finished") {
		t.Fatalf("heartbeat lost queued notice: %+v", note)
	}
	payload, _ := json.Marshal(map[string]string{"text": note.Text, "source": note.Source, "client_id": note.ClientID})
	f.childEvent(f.parent, "", "user_message", string(payload))
	replayPendingSessionInputs(t.Context(), f.hub, f.pool, f.dc, []string{f.parent})
	if again := f.parentNote(f.parent, 100*time.Millisecond); again.Type != "" {
		t.Fatalf("consumed notice was replayed: %+v", again)
	}
}

func TestChildTurnOfflineBacklogCoalescesToLatest(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetChildTurnHook(nil)
	for _, report := range []string{"First report", "Second report", "Latest report"} {
		raw, _ := json.Marshal(map[string]any{"text": report, "done": true})
		f.childEvent(child, "", "assistant_text", string(raw))
		f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	}
	f.backdateChildTurnNotice(child, 2*childTurnFinishedGrace)
	drain(f.dc.send)
	if err := f.api.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
	n := f.parentNote(f.parent, time.Second)
	if !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("backlog did not retain latest: %+v", n)
	}
	if extra := f.parentNote(f.parent, 100*time.Millisecond); extra.Type != "" {
		t.Fatalf("backlog woke parent again: %+v", extra)
	}
}

func TestChildTurnUnconsumedOutboxCoalescesToLatest(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetChildTurnHook(nil)
	f.childEvent(child, "", "assistant_text", `{"text":"Earlier report","done":true}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	f.backdateChildTurnNotice(child, 2*childTurnFinishedGrace)
	drain(f.dc.send)
	// First input is accepted but the parent has not consumed it.
	if err := f.api.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := f.parentNote(f.parent, time.Second)
	f.childEvent(child, "", "assistant_text", `{"text":"Latest report","done":true}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	f.backdateChildTurnNotice(child, 2*childTurnFinishedGrace)
	drain(f.dc.send)
	if err := f.api.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingSessionInputs(t.Context(), f.pool, f.parent)
	if err != nil || len(pending) != 1 || !strings.Contains(pending[0].Frame, "kind=finished") || !strings.Contains(pending[0].Frame, first.ClientID) {
		t.Fatalf("unconsumed notice multiplied or stayed stale: %+v %v", pending, err)
	}
	if extra := f.parentNote(f.parent, 100*time.Millisecond); extra.Type != "" {
		t.Fatalf("unconsumed continuation woke parent again: %+v", extra)
	}
	// A restarted server replays only the latest notice under the same receipt ID.
	replayPendingSessionInputs(t.Context(), f.hub, f.pool, f.dc, []string{f.parent})
	n := f.parentNote(f.parent, time.Second)
	if n.ClientID != first.ClientID || !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("reconnect replay: %+v", n)
	}
}

func TestChildTurnInvalidParentRetiresIntent(t *testing.T) {
	for _, change := range []string{"status='error'", "kind='terminal'"} {
		t.Run(change, func(t *testing.T) {
			f := newDelegFx(t)
			child := f.childOf(f.parent)
			f.hub.SetChildTurnHook(nil)
			if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET `+change+` WHERE id=$1`, f.parent); err != nil {
				t.Fatal(err)
			}
			f.childEvent(child, "", "assistant_text", `{"text":"Complete","done":true}`)
			f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
			if err := f.api.reconcileChildTurns(t.Context()); err != nil {
				t.Fatal(err)
			}
			var pending int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM child_turn_notices WHERE child_id=$1 AND NOT delivered`, child).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("invalid parent retries forever: %d %v", pending, err)
			}
		})
	}
}

func TestChildTurnDisconnectedClusterWaitsForReconnect(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetChildTurnHook(nil)
	f.dc.SetInputReceipts(true)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster' WHERE id=$1`, f.parent); err != nil {
		t.Fatal(err)
	}
	f.hub.RemoveSessionOwner(f.parent)
	f.childEvent(child, "", "assistant_text", `{"text":"Complete","done":true}`)
	f.childTurnDoneReady(child, "end_turn")
	if err := f.api.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
	inputs, err := db.PendingSessionInputs(t.Context(), f.pool, f.parent)
	if err != nil || len(inputs) != 0 {
		t.Fatalf("disconnected cluster parent got resume input: %+v %v", inputs, err)
	}
	if problem := f.api.sendWorkerNotice(t.Context(), runnerPrincipal{Kind: "human", Sub: "owner-1"}, f.parent, workerEvent{child, childTurnFinished, child}, "child-turn-fixture-finished-1"); problem == nil || problem.Status != http.StatusConflict {
		t.Fatalf("relay may resume disconnected coordinator: %+v", problem)
	}
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	drain(f.dc.send)
	if err := f.api.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := f.parentNote(f.parent, time.Second); !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("reconnect lost notice: %+v", n)
	}
}

func TestChildTurnConsumedNoticeAllowsNextContinuation(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.childEvent(child, "", "assistant_text", `{"text":"First report","done":true}`)
	f.childTurnDoneReady(child, "end_turn")
	first := f.parentNote(f.parent, 2*time.Second)
	if first.ClientID == "" {
		t.Fatal("turn_done did not deliver immediately", first)
	}
	raw, _ := json.Marshal(map[string]string{"text": first.Text, "source": first.Source, "client_id": first.ClientID})
	f.childEvent(f.parent, "", "user_message", string(raw))
	f.childEvent(child, "", "assistant_text", `{"text":"Latest report","done":true}`)
	f.childTurnDoneReady(child, "end_turn")
	second := f.parentNote(f.parent, 2*time.Second)
	if second.ClientID == first.ClientID || !strings.Contains(second.Text, "kind=finished") {
		t.Fatalf("consumed notice suppressed later completion: %+v", second)
	}
}

func TestChildTurnUnconsumedFailureCoalesces(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.childEvent(child, "", "assistant_text", `{"text":"Earlier report","done":true}`)
	f.childTurnDoneReady(child, "end_turn")
	first := f.parentNote(f.parent, 2*time.Second)
	if first.ClientID == "" {
		t.Fatal("first notice missing")
	}
	f.childEvent(child, "", "assistant_text", `{"text":"Next report","done":true}`)
	f.childEvent(child, "", "error", `{"message":"engine unavailable"}`)
	// Runtime failures persist their reason before reporting the stopped state.
	if err := db.SetSessionError(t.Context(), f.pool, child, "engine unavailable"); err != nil {
		t.Fatal(err)
	}
	f.childEvent(child, "", "status_changed", `{"status":"error"}`)
	// Status errors report immediately, before the harness sends turn_done.
	deadline := time.Now().Add(2 * time.Second)
	for {
		pending, err := db.PendingSessionInputs(t.Context(), f.pool, f.parent)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) > 1 {
			t.Fatalf("error bypassed completion coalescing: %+v", pending)
		}
		if len(pending) == 1 && strings.Contains(pending[0].Frame, "kind=failed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("coalesced failure was not updated")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.childEvent(child, "", "turn_done", `{"stop_reason":"error"}`)
	if extra := f.parentNote(f.parent, 100*time.Millisecond); extra.Type != "" {
		t.Fatalf("failure woke unconsumed parent again: %+v", extra)
	}
}

func TestChildTurnMigrationCoalescesAcceptedBacklog(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetChildTurnHook(nil)
	// Restore the previous trigger to exercise an upgrade with real queued inputs.
	previous, err := os.ReadFile("../db/migrations/113_child_turn_notices.sql")
	if err != nil {
		t.Fatal(err)
	}
	old := string(previous)
	start, end := strings.Index(old, "CREATE FUNCTION"), strings.Index(old, "CREATE TRIGGER")
	if start < 0 || end < start {
		t.Fatal("previous migration function missing")
	}
	if _, err = f.pool.Exec(t.Context(), strings.Replace(old[start:end], "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", 1)); err != nil {
		t.Fatal(err)
	}
	for _, report := range []string{"Earlier report", "Middle report", "Latest report"} {
		raw, _ := json.Marshal(map[string]any{"text": report, "done": true})
		f.childEvent(child, "", "assistant_text", string(raw))
		f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	}
	drain(f.dc.send)
	// Deliver each queued notice the way a pre-BLG-447 server did: reconcileChildTurns now holds
	// back a notice superseded by a newer turn_done of its own (childTurnFinishedReadiness), which
	// the old trigger never produced reason to distinguish from a genuinely separate completion.
	// Call noteChildTurn directly, exactly as reconcileChildTurns itself would without that gate,
	// to recreate the accepted backlog an old server would already have queued before upgrading.
	rows, err := f.pool.Query(t.Context(), `SELECT turn_seq,kind,event_seq FROM child_turn_notices WHERE child_id=$1 AND NOT delivered ORDER BY event_seq`, child)
	if err != nil {
		t.Fatal(err)
	}
	var queued []childTurnDelivery
	for rows.Next() {
		n := childTurnDelivery{child: child}
		if err := rows.Scan(&n.turn, &n.kind, &n.event); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		queued = append(queued, n)
	}
	rows.Close()
	if len(queued) != 3 {
		t.Fatalf("old trigger did not queue 3 separate notices: %+v", queued)
	}
	for _, n := range queued {
		if !f.api.noteChildTurn(t.Context(), child, n.kind, "", n) {
			t.Fatalf("simulated pre-upgrade delivery failed for turn %d", n.turn)
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE child_turn_notices SET delivered=true WHERE child_id=$1 AND turn_seq=$2`, child, n.turn); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := db.PendingSessionInputs(t.Context(), f.pool, f.parent)
	if err != nil || len(pending) != 3 {
		t.Fatalf("upgrade fixture missing old backlog: %+v %v", pending, err)
	}
	drain(f.dc.send) // the old connection closes before the engine consumes these
	upgrade, err := os.ReadFile("../db/migrations/115_coalesce_child_turn_notices.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), string(upgrade)); err != nil {
		t.Fatal(err)
	}
	pending, err = db.PendingSessionInputs(t.Context(), f.pool, f.parent)
	if err != nil || len(pending) != 1 || !strings.Contains(pending[0].Frame, "kind=finished") {
		t.Fatalf("upgrade retained billed wake backlog: %+v %v", pending, err)
	}
	replayPendingSessionInputs(t.Context(), f.hub, f.pool, f.dc, []string{f.parent})
	if n := f.parentNote(f.parent, time.Second); !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("upgrade lost latest report: %+v", n)
	}
	if extra := f.parentNote(f.parent, 100*time.Millisecond); extra.Type != "" {
		t.Fatalf("upgrade replayed stale report: %+v", extra)
	}
}

// Delay the real notifier until the next input has arrived. A notification must
// read the completed event's snapshot, rather than whatever is now the last turn.
func TestChildTurnNoteKeepsCompletedSyncResultDuringNextTurn(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)
	var notify func()
	f.hub.SetChildTurnHook(func(id, kind, detail string) {
		notify = func() { f.api.onChildTurnEnded(id, kind, detail) }
	})
	f.childEvent(child, "", "user_message", `{"text":"first","source":"session"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"First committed.","done":true}`)
	f.childEvent(child, "", "error", `{"code":"child_sync","message":"first fetch refused"}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	if notify == nil {
		t.Fatal("completed turn did not schedule a notice")
	}
	f.childEvent(child, "", "user_message", `{"text":"second","source":"session"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Second in progress.","done":false}`)
	f.childEvent(child, "", "error", `{"code":"child_sync","message":"second fetch refused"}`)
	notify()
	n := f.parentNote(f.parent, 10*time.Second)
	if n.Source != childTurnSource || !strings.Contains(n.Text, "kind=finished") || strings.Contains(n.Text, "first fetch refused") || strings.Contains(n.Text, "Second") || strings.Contains(n.Text, "second fetch refused") {
		t.Fatalf("completed turn mixed with the next turn: %+v", n)
	}
	// Join the asynchronous delivery before the fixture's database is removed.
	if err := f.api.reconcileChildTurns(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// BLG-447: production evidence (worker 5a7b00c0) showed the real engine emit turn_done, then
// start a fresh "agent turn" on its own about a second later, continuing the same exchange — not
// a worker that needed waking, but Main being told "finished" too early by a transient turn_done.
// A quick resume (here, well inside 2s) must suppress that notice for good; only the later,
// genuine finish — the child staying idle — is news.
func TestChildTurnFinishedNoticeSuppressedByQuickResume(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)

	f.childEvent(child, "", "user_message", `{"text":"Watch the deploy","source":"session"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Starting the watch.","done":true}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`) // transient
	f.childEvent(child, "", "status_changed", `{"status":"idle"}`)
	f.childEvent(child, "", "status_changed", `{"status":"running"}`) // the engine's own resume, <2s later
	// Outlast childTurnFinishedGrace and its scheduled retry: the grace window elapsing must not
	// be enough on its own to send this once the resume is on the record — only the resume signal
	// itself (not just elapsed time) must be what keeps this one suppressed.
	if n := f.parentNote(f.parent, childTurnFinishedGrace+3*time.Second); n.Type != "" {
		t.Fatalf("a transient turn_done followed by a quick resume was reported finished: %+v", n)
	}

	// The genuine finish, once the child actually stays idle past the grace window, is still news.
	f.childEvent(child, "", "assistant_text", `{"text":"Deploy is live.","done":true}`)
	f.childTurnDoneReady(child, "end_turn")
	if n := f.parentNote(f.parent, 10*time.Second); !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("the genuine finish after the resume was not reported: %+v", n)
	}
}

// BLG-447: a turn_done with no open background work and no sign of resumption is held for
// childTurnFinishedGrace, not sent on sight — but it is not held forever: once that window has
// genuinely passed with nothing contradicting it, the notice goes out on the very next
// reconciliation, with no need for a real sleep to prove it (the event's own ts is backdated).
func TestChildTurnFinishedNoticeAfterGenuineIdle(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)

	f.childEvent(child, "", "user_message", `{"text":"Run the audit","source":"session"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Audit complete.","done":true}`)
	f.childTurnDoneReady(child, "end_turn")
	n := f.parentNote(f.parent, 2*time.Second)
	if !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("a genuinely idle turn_done was not reported finished: %+v", n)
	}
}

// BLG-447 review (item 2): 'failed' (stop_reason=error) and 'asked' must never be held or
// suppressed, even while background work is open — childTurnFinishedReadiness only ever applies
// to kind=finished. A real error is news the instant it happens; a question needs the person's
// answer regardless of what else the worker is doing.
func TestChildTurnFailedAndAskedAreNeverHeld(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, title: "Child"})
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)

	f.childEvent(child, "", "user_message", `{"text":"Run the migration","source":"session"}`)
	f.childEvent(child, "", "background_task_started", `{"task_id":"t1","call_id":"c1","kind":"bash"}`)
	f.childEvent(child, "", "error", `{"message":"engine unavailable"}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"error"}`)
	if n := f.parentNote(f.parent, 2*time.Second); !strings.Contains(n.Text, "kind=failed") {
		t.Fatalf("a failed turn with background work still open was held, not delivered immediately: %+v", n)
	}

	// 'asked' is a wholly separate notice path (never routed through child_turn_notices at all);
	// confirm it still reaches Main immediately while the same background work remains open.
	raw, _ := json.Marshal(map[string]any{"kind": "ask", "body": "Which branch?"})
	rec := privDo(f.api.HandlePostMessages, "POST", "/x", "", token, string(raw))
	if rec.Code != 201 {
		t.Fatal(rec.Body.String())
	}
	if n := f.parentNote(f.parent, 2*time.Second); !strings.Contains(n.Text, "kind=asked") {
		t.Fatalf("a question was held: %+v", n)
	}
}

// BLG-447 review (item 3): a self-started turn that only runs tools — no root assistant_text,
// tool_call or user_message — keeps the same anchor (turn_seq) migration 115's trigger computes.
// Since a held notice is never marked delivered, the real finish's turn_done still finds this
// SAME row "pending" and updates it in place; it must never be dropped via ON CONFLICT DO NOTHING
// against an already-delivered row with the same anchor.
func TestChildTurnFinishedSurvivesToolOnlySelfStartedTurn(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)

	f.childEvent(child, "", "user_message", `{"text":"Clean up the branch","source":"session"}`)
	f.childEvent(child, "", "background_task_started", `{"task_id":"t1","call_id":"c1","kind":"bash"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Running the cleanup in the background.","done":true}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`) // transient: background work still open
	if n := f.parentNote(f.parent, 1*time.Second); n.Type != "" {
		t.Fatalf("the transient turn_done was reported finished: %+v", n)
	}

	// The self-started resume runs a tool with no root-level output at all — no assistant_text,
	// tool_call, or user_message outside a subagent — so the anchor the trigger would compute for
	// a FRESH row never advances past the one already held.
	f.childEvent(child, "", "background_task_done", `{"task_id":"t1","status":"completed"}`)
	f.childEvent(child, "", "tool_call", `{"tool":"Bash","call_id":"c2","parent_call_id":"p1"}`)
	f.childEvent(child, "", "tool_result", `{"call_id":"c2","output":"done","parent_call_id":"p1"}`)
	f.childTurnDoneReady(child, "end_turn")
	n := f.parentNote(f.parent, 2*time.Second)
	if !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("the real finish after a tool-only self-started turn was dropped: %+v", n)
	}
}

// BLG-447 review (item 1): the failure rule requires a real finish eventually reach Main even
// when held forever by a reason that never resolves — a stale background_tasks count (it has
// stayed open before), an engine that never resumes, or a daemon wake that never fires.
// childTurnFinishedCeiling is that circuit breaker: held past it, a notice is delivered anyway,
// background work or not.
func TestChildTurnFinishedCeilingForcesDeliveryDespiteOpenBackgroundWork(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)

	f.childEvent(child, "", "user_message", `{"text":"Run the long job","source":"session"}`)
	f.childEvent(child, "", "background_task_started", `{"task_id":"t1","call_id":"c1","kind":"bash"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Running in the background.","done":true}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	if n := f.parentNote(f.parent, 1*time.Second); n.Type != "" {
		t.Fatalf("the transient turn_done was reported finished: %+v", n)
	}
	// The background task never ends (a stuck or stale count) — but the notice must still reach
	// Main once held past the ceiling, not forever.
	f.backdateChildTurnNotice(child, childTurnFinishedCeiling+time.Second)
	if err := f.api.reconcileChildTurns(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := f.parentNote(f.parent, 2*time.Second)
	if !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("held past the ceiling with open background work still never delivered: %+v", n)
	}
}

// BLG-447 review (item 1): "re-check it when the background count changes" — the background task
// ending on its own, with no new turn of the engine's own at all, must promptly release a held
// notice, rather than only on the next unrelated turn_done, the periodic sweep, or the ceiling.
func TestChildTurnFinishedReleasedWhenBackgroundCountDropsWithNoNewTurn(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)

	f.childEvent(child, "", "user_message", `{"text":"Run the job","source":"session"}`)
	f.childEvent(child, "", "background_task_started", `{"task_id":"t1","call_id":"c1","kind":"bash"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Running in the background.","done":true}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	f.backdateChildTurnNotice(child, 2*childTurnFinishedGrace)
	if err := f.api.reconcileChildTurns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.parentNote(f.parent, 500*time.Millisecond); n.Type != "" {
		t.Fatalf("delivered while the background task was still open: %+v", n)
	}
	// The task ends; no new turn_done of the engine's own follows at all.
	f.childEvent(child, "", "background_task_done", `{"task_id":"t1","status":"completed"}`)
	n := f.parentNote(f.parent, 2*time.Second)
	if !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("the background task ending did not release the held notice: %+v", n)
	}
}

// BLG-447 review (item 4): the grace window is measured from child_turn_notices.queued_at, the
// server's own insertion time (migration 145) — never the daemon-reported event ts (clamped to
// within 10 minutes of the server's clock by agentevents.go's eventTimeSkew either way) — so a
// fast daemon clock cannot delay every notice.
func TestChildTurnFinishedGraceUsesServerTimeNotDaemonClock(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)

	f.childEvent(child, "", "user_message", `{"text":"Do the thing","source":"session"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Done.","done":true}`)
	// A daemon clock running an hour fast for this one event.
	f.childEventAt(child, "turn_done", `{"stop_reason":"end_turn"}`, time.Now().Add(time.Hour))
	f.backdateChildTurnNotice(child, 2*childTurnFinishedGrace)
	if err := f.api.reconcileChildTurns(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := f.parentNote(f.parent, 2*time.Second)
	if !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("a fast daemon clock delayed delivery: %+v", n)
	}
}

// BLG-447 review round 2 (item 1): a self-started turn or background job that outlasts
// childTurnFinishedCeiling — exactly this card's own case — must never produce a false
// 'finished' at the ceiling. Only the real finish, once it lands, is news, and exactly once.
func TestChildTurnFinishedCeilingNeverFiresWhileGenuinelyBusy(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.hub.SetSessionOwner(child, f.dc.ID)

	f.childEvent(child, "", "user_message", `{"text":"Run the long job","source":"session"}`)
	f.childEvent(child, "", "background_task_started", `{"task_id":"t1","call_id":"c1","kind":"bash"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Running in the background.","done":true}`)
	f.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`) // transient
	// A self-started turn begins and runs well past the ceiling — faked by backdating the held
	// notice's own queued_at, not by a real wait.
	f.childEvent(child, "", "status_changed", `{"status":"idle"}`)
	f.childEvent(child, "", "status_changed", `{"status":"running"}`)
	f.backdateChildTurnNotice(child, childTurnFinishedCeiling+time.Minute)
	if err := f.api.reconcileChildTurns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.parentNote(f.parent, 500*time.Millisecond); n.Type != "" {
		t.Fatalf("a false 'finished' fired at the ceiling while a self-started turn was still on the record: %+v", n)
	}

	// The real finish, once it lands, is still news — exactly once.
	f.childEvent(child, "", "background_task_done", `{"task_id":"t1","status":"completed"}`)
	f.childEvent(child, "", "assistant_text", `{"text":"Job complete.","done":true}`)
	f.childTurnDoneReady(child, "end_turn")
	n := f.parentNote(f.parent, 2*time.Second)
	if !strings.Contains(n.Text, "kind=finished") {
		t.Fatalf("the real finish was not reported: %+v", n)
	}
	if extra := f.parentNote(f.parent, 300*time.Millisecond); extra.Type != "" {
		t.Fatalf("more than one notice was sent: %+v", extra)
	}
}
