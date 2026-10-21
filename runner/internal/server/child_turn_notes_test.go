package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

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
	id := newUUID()
	HandleAgentEvent(context.Background(), fx.hub, fx.pool, fx.dc, protocol.AgentEvent{
		Type: "agent_event", SessionID: child, ClientEventID: id, Kind: kind, Payload: json.RawMessage(payload),
		Ts: time.Now().UTC().Format(time.RFC3339Nano),
	})
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
	want := `Child "Child" finished its turn: "All done here.". blerg-runner session result ` + child
	if note.Source != childTurnSource || note.Text != want {
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
	if !strings.HasPrefix(failed.Text, `Child "Child" failed its turn: "model unavailable"`) {
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
	if want := `Child "Child" asked a question: "Which 'branch' should I use?". Answer it with blerg-runner session send ` + child; !strings.HasPrefix(asked.Text, want) {
		t.Fatalf("question note = %q", asked.Text)
	}
	fx.api.noteChildTurn(ctx, child, childTurnApproval, "Run rm -rf build?")
	approval := fx.parentNote(fx.parent, 5*time.Second)
	if !strings.HasPrefix(approval.Text, `Child "Child" is waiting on an approval from the person: "Run rm -rf build?". Only the person can decide it`) {
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
	if !strings.HasPrefix(note.Text, `Child "Child" finished its turn: "Committed the fix.".`) ||
		!strings.Contains(note.Text, "Its commits from this turn were NOT brought back to wip/"+child+`: "This turn's commits were not brought back to wip/x: fetch refused".`) {
		t.Fatalf("parent note = %q", note.Text)
	}

	// The next turn, with nothing wrong, says nothing about commits.
	fx.childEvent(child, "", "user_message", `{"text":"again","source":"session"}`)
	fx.childEvent(child, "", "turn_done", `{"stop_reason":"end_turn"}`)
	if got := fx.api.lastTurnSyncProblem(ctx, child); got != "" {
		t.Fatalf("an earlier turn's sync problem was read as the last turn's: %q", got)
	}
	if next := fx.parentNote(fx.parent, 10*time.Second); strings.Contains(next.Text, "NOT brought back") {
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
