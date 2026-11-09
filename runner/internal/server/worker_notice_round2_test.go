package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestDelegatedEventsRedactQuestionAnswersInEveryPage(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	const secret = "private person answer"
	for _, kind := range []string{"user_message", "input_accepted"} {
		for _, source := range []string{questionAnswerSource, "ask_answer"} {
			raw, _ := json.Marshal(map[string]string{"source": source, "text": secret, "answer": secret, "message_id": newUUID()})
			if _, _, err := db.AppendAgentEvent(t.Context(), f.pool, child, newUUID(), kind, string(raw)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, query := range []string{"?after_seq=0", "?before_seq=0", "?after_seq=0&limit=1", "?before_seq=0&limit=1"} {
		code, out, _ := f.do("GET", childRoute(f.parent, child, "/events")+query, f.parentTok, nil, nil)
		raw, _ := json.Marshal(out)
		if code != 200 || strings.Contains(string(raw), secret) {
			t.Fatalf("delegated answer leak on %s: status %d", query, code)
		}
		events := out["events"].([]any)
		if len(events) == 0 {
			t.Fatal("redaction must preserve pagination and resolution events")
		}
		for _, event := range events {
			payload := event.(map[string]any)["payload"].(map[string]any)
			if payload["redacted"] != true || payload["message_id"] == nil || payload["source"] == nil {
				t.Fatal("resolution identity lost")
			}
		}
	}
	owner, problem := f.api.Events(t.Context(), runnerPrincipal{Kind: "human", Sub: "owner-1"}, child, 0, 200)
	raw, _ := json.Marshal(owner)
	if problem != nil || !strings.Contains(string(raw), secret) {
		t.Fatal("owner transcript must retain the answer")
	}
}

func TestWorkerAskReadPrioritizesOldBlockingQuestions(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	blocking, err := db.CreateAsk(t.Context(), f.pool, child, "old blocking question", false)
	if err != nil {
		t.Fatal(err)
	}
	oldest, err := db.CreateQuestion(t.Context(), f.pool, child, "old no-wait question")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE messages SET created_at=statement_timestamp()-interval '2 days' WHERE id=$1`, blocking); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE messages SET created_at=statement_timestamp()-interval '1 day' WHERE id=$1`, oldest); err != nil {
		t.Fatal(err)
	}
	for range 25 {
		if _, err = db.CreateQuestion(t.Context(), f.pool, child, "new no-wait chatter"); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ := f.do("GET", childRoute(f.parent, child, "/result"), f.parentTok, nil, nil)
	if code != 200 {
		t.Fatalf("result: %d", code)
	}
	asks := out["open_asks"].([]any)
	if len(asks) != 20 || asks[0].(map[string]any)["message_id"] != blocking || asks[1].(map[string]any)["message_id"] != oldest {
		t.Fatal("new no-wait asks hid the older blocking question")
	}
}

func TestChannelClosedNoticeDoesNotRecommendForbiddenRead(t *testing.T) {
	child, event := newUUID(), newUUID()
	notice := workerNotice(child, "channel_closed", event)
	if strings.Contains(notice, "session result") || !strings.Contains(notice, "Only the person") || !strings.Contains(notice, "id="+event) {
		t.Fatal("channel closure advertises an inaccessible read")
	}
}

type workerFailureWriter struct{ events chan string }

func (w workerFailureWriter) Write(p []byte) (int, error) { w.events <- string(p); return len(p), nil }

func TestChannelCloseHookLogsBothFailureStages(t *testing.T) {
	for _, stage := range []string{"record", "reconcile"} {
		t.Run(stage, func(t *testing.T) {
			f := newDelegFx(t)
			child := f.childOf(f.parent)
			// Each fixture owns its disposable database. Fail the real boundary at
			// insertion, or just after insertion when reconciliation reads events.
			statement := `DROP TABLE channel_close_notices`
			if stage == "reconcile" {
				statement = `DROP TABLE agent_events CASCADE`
			}
			if _, err := f.pool.Exec(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
			events := make(chan string)
			original := failurelog.Default
			failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(workerFailureWriter{events}, nil)))
			t.Cleanup(func() { failurelog.Default = original })
			f.api.onChildTurnEnded(child, "recommended_channel_closed", "")
			select {
			case entry := <-events:
				var fields map[string]any
				if json.Unmarshal([]byte(entry), &fields) != nil || fields["operation"] != "channel_close" || fields["stage"] != stage || fields["session_id"] != child || !strings.HasPrefix(fmt.Sprint(fields["cause"]), "*pgconn.") {
					t.Fatalf("missing safe %s failure: %s", stage, entry)
				}
				if strings.Contains(entry, "does not exist") {
					t.Fatal("raw database error logged")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("channel-close failure was silently dropped")
			}
		})
	}
}

func TestWorkerSettledReplayAfterPruningDoesNotSpend(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.dc.SetInputReceipts(true)
	child := f.childOf(f.parent)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.parent, Source: childTurnSource, ClientID: "settled-replay-review", Text: workerNotice(child, childTurnAsked, newUUID())}
	raw, _ := json.Marshal(msg)
	if _, _, err := db.AcceptSessionInput(t.Context(), f.pool, f.parent, msg.ClientID, string(raw), newUUID(), string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := db.SettleSessionInput(t.Context(), f.pool, f.parent, msg.ClientID); err != nil {
		t.Fatal(err)
	}
	drain(f.dc.send)
	// A replay may have fetched this input before its acknowledgement and
	// receipt pruning. The settled outbox row must still prevent charging.
	if !sendWorkerFrame(t.Context(), f.hub, f.pool, f.dc, msg, raw) {
		t.Fatal("settled replay was not recognized")
	}
	var n int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM worker_wake_receipts`).Scan(&n); err != nil || n != 0 || len(f.dc.send) != 0 {
		t.Fatalf("settled replay was charged or resent: receipts=%d err=%v", n, err)
	}
}

func TestWorkerWakeReceiptsPruneOnlyAfterDeliverySettles(t *testing.T) {
	f := newDelegFx(t)
	for _, key := range []string{"settled", "pending"} {
		raw, _ := json.Marshal(map[string]string{"type": "agent_user_message", "session_id": f.parent, "client_id": key, "text": "fixed notice", "source": "child_turn"})
		if _, _, err := db.AcceptSessionInput(t.Context(), f.pool, f.parent, key, string(raw), newUUID(), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SettleSessionInput(t.Context(), f.pool, f.parent, "settled"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"settled", "pending", "legacy-no-receipt"} {
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO worker_wake_receipts(parent_id,delivery_key) VALUES($1,$2)`, f.parent, key); err != nil {
			t.Fatal(err)
		}
	}
	f.api.reconcileClusterJobsOnce(t.Context())
	var keys []string
	if err := f.pool.QueryRow(t.Context(), `SELECT array_agg(delivery_key ORDER BY delivery_key) FROM worker_wake_receipts WHERE parent_id=$1`, f.parent).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "legacy-no-receipt" || keys[1] != "pending" {
		t.Fatalf("unsafe or absent receipt pruning: %v", keys)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET status='ended',ended_at=statement_timestamp() WHERE id=$1`, f.parent); err != nil {
		t.Fatal(err)
	}
	f.api.reconcileClusterJobsOnce(t.Context())
	var n int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM worker_wake_receipts WHERE parent_id=$1`, f.parent).Scan(&n); err != nil || n != 0 {
		t.Fatalf("ended Main retained receipts: %d %v", n, err)
	}
}
