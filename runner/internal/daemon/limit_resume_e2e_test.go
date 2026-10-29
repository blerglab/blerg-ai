package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/server"
	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
	"github.com/gorilla/websocket"
)

type limitResumeEmitter func(agent.Event)

func (f limitResumeEmitter) Emit(ev agent.Event) { f(ev) }

// Exercise actual CLI decoding, normalized daemon events, durable server
// scheduling, and delivery back over the daemon connection. No provider calls.
func TestClaudeWeeklyRefusalSchedulesAndFiresResume(t *testing.T) {
	t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "true")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := testsupport.Open(t, "TEST_DATABASE_URL", "runner", db.RunMigrations)
	hub := server.NewHub()
	srv := httptest.NewServer(hub.ServeDaemon("invented-limit-test-token", nil))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Welcome: true, Name: "limit-fixture", Mode: "daemon", Token: "invented-limit-test-token"}))
	check(conn.SetReadDeadline(time.Now().Add(20 * time.Second)))
	var welcome protocol.DaemonWelcome
	check(conn.ReadJSON(&welcome))
	if welcome.DaemonID == "" {
		t.Fatal("no daemon identity")
	}
	deadline := time.Now().Add(5 * time.Second)
	for hub.GetDaemon(welcome.DaemonID) == nil {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not attach")
		}
		time.Sleep(time.Millisecond)
	}
	check(db.UpsertDaemon(ctx, pool, welcome.DaemonID, "limit-fixture", "daemon", ""))
	id := ccUUID()
	check(db.InsertSession(ctx, pool, id, welcome.DaemonID, "idle", "/work/proj", "proj", "Task", ""))
	_, err = pool.Exec(ctx, `UPDATE sessions SET kind='agent',engine='claude' WHERE id=$1`, id)
	check(err)
	check(db.SetLimitResume(ctx, pool, id, true, "invented-auth"))
	hub.SetSessionOwner(id, welcome.DaemonID)
	em := limitResumeEmitter(func(ev agent.Event) {
		raw, err := json.Marshal(ev.Payload)
		check(err)
		server.HandleAgentEvent(ctx, hub, pool, hub.GetDaemon(welcome.DaemonID), protocol.AgentEvent{Type: "agent_event", SessionID: id, ClientEventID: ev.ClientEventID, Ts: ev.Ts.Format(time.RFC3339Nano), Kind: ev.Kind, Payload: raw})
	})
	d := newClaudeCodeDriver("", "", "", em, nil)
	now := time.Now().UTC()
	d.now = func() time.Time { return now }
	d.emitTurnMessage(agent.UserMessagePayload{Text: "continue", Source: "chat"})
	text := "You've hit your weekly limit · resets " + now.AddDate(0, 0, 3).Format("Jan 2, 2006, 3:04pm") + " (UTC)"
	line := fmt.Sprintf(`{"type":"assistant","error":"rate_limit","message":{"model":"<synthetic>","content":[{"type":"text","text":%q}]}}`, text)
	p := &ccProc{sawInit: true}
	d.onLine(p, []byte(line))
	result := fmt.Sprintf(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":%q}`, text)
	d.onLine(p, []byte(result))
	state, err := db.GetLimitResume(ctx, pool, id)
	check(err)
	if state.State != "pending" || state.ResetAt == nil || state.ResetAt.Sub(now) < 2*24*time.Hour {
		t.Fatalf("weekly refusal not scheduled: %+v", state)
	}
	// Advance the persisted deadline, without sleeping until the weekly reset.
	_, err = pool.Exec(ctx, `UPDATE session_limit_resume SET reset_at=now()-interval '3 minutes' WHERE session_id=$1`, id)
	check(err)
	api := server.NewAPI(hub, pool, "", nil, "")
	done := make(chan struct{})
	go func() { defer close(done); api.RunLimitResumes(ctx) }()
	defer func() { cancel(); <-done }()
	for {
		var msg protocol.AgentUserMessage
		if err := conn.ReadJSON(&msg); err != nil {
			state, _ := db.GetLimitResume(ctx, pool, id)
			row, _ := db.GetSession(ctx, pool, id)
			t.Fatalf("delivery: %v; state=%s attempts=%d; status=%s paused=%q tasks=%d", err, state.State, state.Attempts, row.Status, row.PausedReason, row.BackgroundTasks)
		}
		if msg.Type != "agent_user_message" {
			continue
		}
		if msg.Source != "limit_resume" || msg.ExpectedTurnID == "" || msg.ExpectedTurnID != state.TurnID {
			t.Fatalf("wrong continuation: %+v", msg)
		}
		d.EnqueueInput(agent.Input{Text: msg.Text, Source: msg.Source, ExpectedTurnID: msg.ExpectedTurnID})
		if len(d.queue) != 1 {
			t.Fatal("daemon rejected current idle continuation")
		}
		state, err = db.GetLimitResume(ctx, pool, id)
		check(err)
		if state.State != "dispatched" || state.Attempts != 1 {
			t.Fatalf("not durably dispatched: %+v", state)
		}
		break
	}
}
