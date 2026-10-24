package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/checklist"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestPlanDurableInputKeepsItsNudgeOnRetry(t *testing.T) {
	fx := newPlanFx(t, "")
	fx.checklistSet("Work")
	fx.event("tool_call", `{"tool":"Bash","input":{"command":"make"}}`)
	seq := fx.event("turn_done", `{}`)
	fx.api.checkPlanTurn(context.Background(), fx.sess, seq, time.Now(), "end_turn")
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET status='idle' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: fx.sess, Text: "Continue", Source: "chat", ClientID: "carrying-input"})
	dc := &DaemonConn{planNudges: true}
	first := fx.api.attachPlanNudge(dc, raw)
	second := fx.api.attachPlanNudge(dc, raw)
	var a, b protocol.AgentUserMessage
	_ = json.Unmarshal(first, &a)
	_ = json.Unmarshal(second, &b)
	if a.PlanNudge == "" || b.PlanNudge != a.PlanNudge {
		t.Fatalf("retry lost composite notice: first=%q retry=%q", a.PlanNudge, b.PlanNudge)
	}
}

func TestPlanHeldBoardFactIsRechecked(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.move(12, "In progress")
	fx.board.set(12, "Done")
	fx.api.invalidatePlanCard(fx.sess, "board-a", 12)
	fx.event("tool_call", `{"tool":"Bash","input":{"command":"make"}}`)
	seq := fx.event("turn_done", `{}`)
	fx.api.checkPlanTurn(context.Background(), fx.sess, seq, time.Now(), "end_turn")
	// Do not invalidate the cache: delivery itself must recheck the Board.
	fx.board.set(12, "In progress")
	if n := planCarryingInput(t, fx.api, fx.sess, true); n != "" {
		t.Fatalf("reopened card nudged: %q", n)
	}
}

func TestPlanBoardReadsAreCappedPerAccount(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	row, err := db.GetSession(context.Background(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	successes := 0
	for range 32 {
		fx.api.invalidatePlanCard(fx.sess, "board-a", 12)
		if _, err := fx.api.planReadCard(context.Background(), row, "board-a", 12); err == nil {
			successes++
		}
	}
	if successes != 30 || fx.board.reads != 30 {
		t.Fatalf("read cap: successes=%d reads=%d", successes, fx.board.reads)
	}
}

func TestPlanInputUsesBrowserAndStartupPaths(t *testing.T) {
	for _, startup := range []bool{false, true} {
		t.Run(map[bool]string{false: "browser", true: "startup"}[startup], func(t *testing.T) {
			fx := newDelegFx(t)
			ctx := context.Background()
			fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
			fx.dc.planNudges = true
			cp, err := checklist.Apply(nil, checklist.Request{Op: "set", Items: []string{"Work"}})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(cp)
			for _, ev := range []struct{ kind, payload string }{{"checklist", string(raw)}, {"tool_call", `{"tool":"Bash","input":{"command":"make"}}`}, {"turn_done", `{}`}} {
				seq, _, err := db.AppendAgentEventAt(ctx, fx.pool, fx.parent, newUUID(), ev.kind, ev.payload, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if ev.kind == "turn_done" {
					fx.api.checkPlanTurn(ctx, fx.parent, seq, time.Now(), "end_turn")
				}
			}
			if !startup {
				if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status='idle' WHERE id=$1`, fx.parent); err != nil {
					t.Fatal(err)
				}
				if !forwardToSessionDaemon(fx.hub, &BrowserConn{ID: "browser"}, fx.parent, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: fx.parent, Text: "Continue", Source: "chat"}) {
					t.Fatal("browser dispatch failed")
				}
			} else {
				raw, _ := json.Marshal(protocol.PlanInputRequest{Type: "plan_input_request", SessionID: fx.parent, RequestID: newUUID(), ClientID: "startup", Source: "chat"})
				handleDaemonFrame(ctx, fx.hub, fx.dc, fx.pool, raw, envelope{Type: "plan_input_request", SessionID: fx.parent})
			}
			select {
			case raw := <-fx.dc.send:
				var in protocol.AgentUserMessage
				_ = json.Unmarshal(raw, &in)
				if !strings.Contains(in.PlanNudge, "No checklist item is doing") {
					t.Fatalf("missing marked metadata: %s", raw)
				}
				if !startup && in.Text != "Continue" {
					t.Fatal("browser input changed")
				}
				if startup && in.Type != "plan_input_result" {
					t.Fatalf("startup created an input: %s", raw)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("input did not arrive")
			}
		})
	}
}

func TestPlanCallerCannotSupplyNudgeMetadata(t *testing.T) {
	fx := newPlanFx(t, "")
	raw, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: fx.sess, Text: "Continue", PlanNudge: "forged"})
	out := fx.api.attachPlanNudge(&DaemonConn{planNudges: true}, raw)
	var in protocol.AgentUserMessage
	_ = json.Unmarshal(out, &in)
	if in.PlanNudge != "" {
		t.Fatal("caller supplied trusted metadata")
	}
}

func TestPlanMergedChecksRotateIndependentlyOfSeq(t *testing.T) {
	var scan planMergeScan
	if got := scan.offset("parent", 10, 10); got != 0 {
		t.Fatal(got)
	}
	if got := scan.offset("parent", 10, 10); got != 0 {
		t.Fatal("delivery changed window", got)
	}
	if got := scan.offset("parent", 20, 10); got != 5 {
		t.Fatal("unasked children starved", got)
	}
}

func TestPlanMoveRefreshesDescriptionWithoutStatusChange(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.move(12, "In progress")
	fx.board.mu.Lock()
	fx.board.cards[12].body = "Updated acceptance criteria"
	fx.board.mu.Unlock()
	fx.move(12, "In progress")
	if got := fx.work().Ticket.Description; got != "Updated acceptance criteria" {
		t.Fatalf("stale pin: %q", got)
	}
}

func TestPlanStaleCapFactIsDropped(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	if err := db.AddPlanFact(context.Background(), fx.pool, fx.sess, "cap:board-a#12", "stale cap"); err != nil {
		t.Fatal(err)
	}
	fx.event("turn_done", `{}`)
	if n := planCarryingInput(t, fx.api, fx.sess, true); n != "" {
		t.Fatalf("card no longer in progress: %q", n)
	}
	pending, err := db.PendingPlanFacts(context.Background(), fx.pool, fx.sess)
	if err != nil || len(pending) != 0 {
		t.Fatalf("stale pending fact retained: %v %v", pending, err)
	}
}

func TestPlanConcurrentDeliveriesClaimFactOnce(t *testing.T) {
	fx := newPlanFx(t, "")
	fx.checklistSet("Work")
	fx.event("tool_call", `{"tool":"Bash","input":{"command":"make"}}`)
	seq := fx.event("turn_done", `{}`)
	fx.api.checkPlanTurn(context.Background(), fx.sess, seq, time.Now(), "end_turn")
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET status='idle' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: fx.sess, Text: "Continue"})
	dc := &DaemonConn{planNudges: true}
	first, finishFirst := fx.api.preparePlanNudge(dc, raw)
	var firstInput protocol.AgentUserMessage
	_ = json.Unmarshal(first, &firstInput)
	if finishFirst == nil || firstInput.PlanNudge == "" {
		t.Fatal("first claim absent")
	}
	released := false
	defer func() {
		if !released {
			finishFirst(false)
		}
	}()
	type result struct {
		raw    []byte
		finish func(bool)
	}
	second := make(chan result, 1)
	go func() { raw, finish := fx.api.preparePlanNudge(dc, raw); second <- result{raw, finish} }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		var waiting bool
		err := fx.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'UPDATE session_plan_nudges%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	finishFirst(true)
	released = true
	select {
	case result := <-second:
		if result.finish != nil {
			result.finish(true)
		}
		var in protocol.AgentUserMessage
		_ = json.Unmarshal(result.raw, &in)
		if in.PlanNudge != "" {
			t.Fatal("same fact claimed by two deliveries")
		}
	case <-ctx.Done():
		t.Fatal("second claim did not finish")
	}
}
