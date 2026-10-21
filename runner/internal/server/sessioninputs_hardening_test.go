package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionInputRejectsOversizeAndConflictingIDs(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "original", Text: "First"}
	if fresh, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil || !fresh {
		t.Fatalf("first acceptance: %v %v", fresh, err)
	}
	msg.Text = "Different"
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); !errors.Is(err, db.ErrSessionInputConflict) {
		t.Fatalf("conflicting id: %v", err)
	}
	msg.ClientID = "large-message"
	msg.Text = strings.Repeat("x", db.SessionInputMaxBytes+1)
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); !errors.Is(err, db.ErrSessionInputTooLarge) {
		t.Fatalf("oversize message: %v", err)
	}
	msg.Text = "Short"
	msg.ClientID = strings.Repeat("x", db.SessionInputMaxClientIDBytes+1)
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); !errors.Is(err, db.ErrSessionInputID) {
		t.Fatalf("oversize id: %v", err)
	}
	rows, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rejections changed pending queue: count=%d err=%v", len(rows), err)
	}
}

func TestSessionInputPendingQueueIsBoundedAndSettlingFreesSpace(t *testing.T) {
	f := newIngestFixture(t)
	for i := 0; i < db.SessionInputMaxPending; i++ {
		msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: fmt.Sprintf("pending-%d", i), Text: "Keep working"}
		if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
	}
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "overflow", Text: "Next"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); !errors.Is(err, db.ErrSessionInputQueue) {
		t.Fatalf("unbounded queue: %v", err)
	}
	if err := db.SettleSessionInput(f.ctx, f.pool, f.sessionID, "pending-0"); err != nil {
		t.Fatal(err)
	}
	if fresh, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil || !fresh {
		t.Fatalf("settlement did not free capacity: %v %v", fresh, err)
	}
}

func TestSessionInputPendingBytesAreBounded(t *testing.T) {
	f := newIngestFixture(t)
	for i := 0; i < db.SessionInputMaxPending; i++ {
		msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: fmt.Sprintf("large-%d", i), Text: strings.Repeat("x", db.SessionInputMaxBytes-1024)}
		_, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg)
		if errors.Is(err, db.ErrSessionInputQueue) {
			if i == 0 || i >= db.SessionInputMaxPending-1 {
				t.Fatalf("aggregate byte bound did not act independently of count: %d", i)
			}
			return
		}
		if err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
	}
	t.Fatal("aggregate pending bytes were not bounded")
}

func TestSessionInputConcurrentAcceptanceCannotExceedBound(t *testing.T) {
	f := newIngestFixture(t)
	for i := 0; i < db.SessionInputMaxPending-1; i++ {
		msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: fmt.Sprintf("pending-%d", i), Text: "Next"}
		if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
			t.Fatal(err)
		}
	}
	cfg := f.pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = "test_blerg_runner_srv"
	pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: fmt.Sprintf("racing-%d", i), Text: "Next"}
			_, err := acceptSessionInput(f.ctx, f.hub, pool, msg)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	accepted, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, db.ErrSessionInputQueue):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("concurrent queue bound: accepted=%d refused=%d", accepted, refused)
	}
}

func TestSessionInputEndingSettlesWithDurableFailure(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "undelivered", Text: "Next task"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	HandleSessionEnded(f.ctx, f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sessionID})
	failPendingSessionInputs(f.ctx, f.hub, f.pool, f.sessionID) // ending replay is idempotent
	pending, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("ended input still queued: count=%d err=%v", len(pending), err)
	}
	var count int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='error' AND payload->>'client_id'=$2`, f.sessionID, msg.ClientID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable correlated failure: count=%d err=%v", count, err)
	}
	msg.ClientID = "after-ending"
	if _, err = acceptSessionInput(f.ctx, f.hub, f.pool, msg); !errors.Is(err, db.ErrSessionInputEnded) {
		t.Fatalf("accepted new work after session ended: %v", err)
	}
}

func TestSessionInputFailureSettlementRollsBackWithEvent(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "rollback-input", Text: "Next task"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE FUNCTION reject_input_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='error' THEN RAISE EXCEPTION 'test failure'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER reject_input_failure BEFORE INSERT ON agent_events FOR EACH ROW EXECUTE FUNCTION reject_input_failure()`,
	} {
		if _, err := f.pool.Exec(f.ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	failPendingSessionInputs(f.ctx, f.hub, f.pool, f.sessionID)
	pending, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("failed event write lost pending input: count=%d err=%v", len(pending), err)
	}
	if _, err = f.pool.Exec(f.ctx, `DROP TRIGGER reject_input_failure ON agent_events`); err != nil {
		t.Fatal(err)
	}
	failPendingSessionInputs(f.ctx, f.hub, f.pool, f.sessionID)
	pending, err = db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("retry did not settle input: count=%d err=%v", len(pending), err)
	}
}

func TestSessionInputReplayDoesNotForwardEndedWork(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "ended-replay", Text: "Next task"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.UpdateSessionStatus(f.ctx, f.pool, f.sessionID, "error", &now); err != nil {
		t.Fatal(err)
	}
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	select {
	case raw := <-f.dc.send:
		var frame protocol.AgentUserMessage
		_ = json.Unmarshal(raw, &frame)
		t.Fatalf("forwarded ended input type %s", frame.Type)
	default:
	}
	pending, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("replay did not settle ended input: count=%d err=%v", len(pending), err)
	}
}

func TestSessionInputChatReopenRepairsFailedTerminalSettlement(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "reopen-repair", Text: "Next task"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE FUNCTION reject_input_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='error' THEN RAISE EXCEPTION 'test failure'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER reject_input_failure BEFORE INSERT ON agent_events FOR EACH ROW EXECUTE FUNCTION reject_input_failure()`,
	} {
		if _, err := f.pool.Exec(f.ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	HandleSessionEnded(f.ctx, f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sessionID})
	if pending, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID); err != nil || len(pending) != 1 {
		t.Fatalf("failed final write did not retain input: count=%d err=%v", len(pending), err)
	}
	if _, err := f.pool.Exec(f.ctx, `DROP TRIGGER reject_input_failure ON agent_events`); err != nil {
		t.Fatal(err)
	}
	bc := &BrowserConn{ID: "reopened", send: make(chan []byte, 16)}
	replayAgentEvents(f.ctx, f.hub, bc, f.pool, protocol.SubscribeAgentEvents{SessionID: f.sessionID, Tail: 10, Limit: 10})
	if pending, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID); err != nil || len(pending) != 0 {
		t.Fatalf("reopen left terminal input queued: count=%d err=%v", len(pending), err)
	}
	found := false
	for len(bc.send) > 0 {
		var ev protocol.AgentEvent
		if json.Unmarshal(<-bc.send, &ev) == nil && ev.Kind == "error" {
			found = true
		}
	}
	if !found {
		t.Fatal("chat reopening omitted delivery failure")
	}
}

func TestSessionInputForeignDaemonCannotReplay(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "private-input", Text: "Next task"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	foreign := &DaemonConn{ID: newUUID(), send: make(chan []byte, 8)}
	foreign.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, foreign, []string{f.sessionID})
	if len(foreign.send) != 0 {
		t.Fatal("input disclosed to a daemon that does not own its session")
	}
}

func TestSessionInputRevivableErrorKeepsPendingWork(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "revivable", Text: "Next task"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSessionStatus(f.ctx, f.pool, f.sessionID, "error", nil); err != nil {
		t.Fatal(err)
	}
	repairEndedSessionInputs(f.ctx, f.hub, f.pool, f.sessionID)
	if pending, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID); err != nil || len(pending) != 1 {
		t.Fatalf("revivable error lost queued work: count=%d err=%v", len(pending), err)
	}
}

func TestSessionInputReplayRequiresDaemonReceipts(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "legacy-pending", Text: "Run once", Source: "chat"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	f.dc.SetInputReceipts(false)
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	select {
	case <-f.dc.send:
		t.Fatal("redelivered to a legacy daemon without input deduplication")
	default:
	}
	f.dc.SetInputReceipts(true)
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	select {
	case <-f.dc.send:
	default:
		t.Fatal("updated daemon did not receive its durable input")
	}
}
