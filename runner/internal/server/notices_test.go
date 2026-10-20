package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// noticeFixture is one session on a hub with one browser and one push subscription, for the
// notice rule (notices.go). Requires TEST_DATABASE_URL.
type noticeFixture struct {
	t       *testing.T
	ctx     context.Context
	hub     *Hub
	pool    *pgxpool.Pool
	browser *BrowserConn
	dc      *DaemonConn
	sid     string
	pushes  *atomic.Int32
	n       int
}

func newNoticeFixture(t *testing.T) *noticeFixture {
	t.Helper()
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	daemonID := "00000000-0000-0000-0000-0000000000c1"
	sid := "00000000-0000-0000-0000-0000000000c2"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "laptop", "daemon", ""); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sid, daemonID, "running", "/w/proj", "proj", "Fix the widget", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "x")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "y")
	if err := db.UpsertPushSubscription(ctx, pool, "http://127.0.0.1:1/never", "p", "a"); err != nil {
		t.Fatal(err)
	}
	pushes := &atomic.Int32{}
	saved := sendWebPush
	sendWebPush = func([]byte, *webpush.Subscription, *webpush.Options) (*http.Response, error) {
		pushes.Add(1)
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	hub := NewHub()
	browser := &BrowserConn{ID: "b1", send: make(chan []byte, 256)}
	hub.RegisterBrowser(browser)
	return &noticeFixture{
		t: t, ctx: ctx, hub: hub, pool: pool, browser: browser,
		dc: &DaemonConn{ID: daemonID, send: make(chan []byte, 256)}, sid: sid, pushes: pushes,
	}
}

func (f *noticeFixture) ingest(kind, payload string) {
	f.t.Helper()
	f.n++
	HandleAgentEvent(f.ctx, f.hub, f.pool, f.dc, protocol.AgentEvent{
		Type: "agent_event", SessionID: f.sid, ClientEventID: fmt.Sprintf("00000000-0000-4000-8000-%012d", f.n), Kind: kind,
		Payload: json.RawMessage(payload),
	})
}

func (f *noticeFixture) state(status string) {
	f.t.Helper()
	if err := HandleSessionStateChanged(f.ctx, f.hub, f.pool, protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: f.sid, Status: status,
	}); err != nil {
		f.t.Fatalf("state %s: %v", status, err)
	}
}

func (f *noticeFixture) taskStarted(id string) {
	f.ingest("background_task_started", `{"task_id":"`+id+`","call_id":"c-`+id+`","kind":"bash","description":"build"}`)
}

func (f *noticeFixture) taskDone(id string) {
	f.ingest("background_task_done", `{"task_id":"`+id+`","status":"completed"}`)
}

// notices drains the browser's queue and returns the session_notice broadcasts in it.
func (f *noticeFixture) notices() []protocol.SessionNotice {
	var out []protocol.SessionNotice
	for {
		select {
		case raw := <-f.browser.send:
			if !strings.Contains(string(raw), `"session_notice"`) {
				continue
			}
			var m protocol.SessionNotice
			if err := json.Unmarshal(raw, &m); err != nil {
				f.t.Fatalf("notice: %v", err)
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

// expect checks how many notices (and pushes, nobody being active) went out since the last check.
func (f *noticeFixture) expect(what string, want int) []protocol.SessionNotice {
	f.t.Helper()
	got := f.notices()
	if len(got) != want {
		f.t.Fatalf("%s: %d notice(s) %+v, want %d", what, len(got), got, want)
	}
	if p := int(f.pushes.Swap(0)); p != want {
		f.t.Fatalf("%s: %d push(es), want %d", what, p, want)
	}
	return got
}

// An idle session with nothing running is told at once, worded as waiting for the person.
func TestIdleNotice_NothingRunning(t *testing.T) {
	f := newNoticeFixture(t)
	f.state("idle")
	got := f.expect("idle with nothing running", 1)
	if n := got[0]; n.Status != "idle" || n.Title != "Session idle" || n.Body != "Fix the widget is waiting for you." || n.URL != "/sessions/"+f.sid {
		t.Fatalf("notice = %+v", n)
	}
	// Waiting (a turn paused for input) is told at once too.
	f.state("running")
	f.state("waiting")
	if got := f.expect("waiting", 1); got[0].Body != "Fix the widget is waiting for your input." {
		t.Fatalf("waiting notice = %+v", got[0])
	}
}

// A turn that ends while background work runs says nothing until the last of it ends, then once.
func TestIdleNotice_DeferredUntilLastWorkEnds(t *testing.T) {
	f := newNoticeFixture(t)
	f.taskStarted("t1")
	f.taskStarted("t2")
	// A background subagent: started, then its launch acknowledged (it carries on in the background).
	f.ingest("subagent_started", `{"child_id":"a1","agent_type":"general-purpose","summary":"look around"}`)
	f.ingest("subagent_done", `{"child_id":"a1","background":true}`)
	f.notices()
	f.state("idle")
	f.expect("idle while two tasks and a subagent run", 0)

	f.taskDone("t1")
	f.expect("one task of two ended", 0)
	f.ingest("subagent_done", `{"child_id":"a1","summary":"done"}`)
	f.expect("the subagent ended, a task still runs", 0)
	f.taskDone("t2")
	got := f.expect("the last running item ended", 1)
	if got[0].Body != "Fix the widget is waiting for you." {
		t.Fatalf("deferred notice = %+v", got[0])
	}
	// Nothing more is owed: a stray end says nothing.
	f.taskDone("t3")
	f.expect("after the deferred notice went out", 0)
}

// A new turn cancels a deferred notice; the turn's own end decides afresh.
func TestIdleNotice_NewTurnCancels(t *testing.T) {
	f := newNoticeFixture(t)
	f.taskStarted("t1")
	f.state("idle")
	f.state("running")
	f.taskDone("t1")
	f.expect("the task ended during a new turn", 0)
	f.state("idle")
	f.expect("the new turn ended with nothing running", 1)
}

// A question cancels a deferred notice (its own notice covers it); so does the session's end.
func TestIdleNotice_AskAndEndCancel(t *testing.T) {
	f := newNoticeFixture(t)
	f.taskStarted("t1")
	f.state("idle")
	f.ingest("ask", `{"body":"which colour?"}`)
	f.taskDone("t1")
	f.expect("ask then the last task ended", 0)

	f.taskStarted("t2")
	f.state("running")
	f.state("idle")
	HandleSessionEnded(f.ctx, f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sid})
	f.taskDone("t2")
	f.expect("the session ended, then the last task", 0)
	if _, held := f.hub.idleNotices.heldAt(f.sid); held {
		t.Fatal("an ended session still holds a deferred notice")
	}
}

// A session nobody watches calls no one: no notice, no push, and an ask of its own is listed quietly.
func TestIdleNotice_HeadlessSessionsSilent(t *testing.T) {
	for _, tc := range []struct{ name, set string }{
		{"unattended", `interaction = 'unattended'`},
		{"cron", `cron_id = '00000000-0000-0000-0000-0000000000c9', started_by_kind = 'cron', interaction = 'unattended'`},
		{"cron, no interaction recorded", `cron_id = '00000000-0000-0000-0000-0000000000c9'`},
		{"operator key", `started_by_kind = 'runner_key', interaction = 'interactive'`},
		{"agent token, unattended", `started_by_kind = 'agent', started_by_name = 'tool', interaction = 'unattended'`},
		{"agent token, no interaction recorded", `started_by_kind = 'agent', started_by_name = 'tool'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNoticeFixture(t)
			if _, err := f.pool.Exec(f.ctx, `UPDATE sessions SET `+tc.set+` WHERE id = $1`, f.sid); err != nil {
				t.Fatal(err)
			}
			f.state("idle")
			f.state("running")
			f.state("waiting")
			f.expect("headless idle and waiting", 0)

			row, err := db.GetSession(f.ctx, f.pool, f.sid)
			if err != nil || row == nil {
				t.Fatalf("GetSession: %v", err)
			}
			if !headlessSession(row) {
				t.Fatalf("%s is not read as headless", tc.name)
			}
			api := &API{hub: f.hub, dbPool: f.pool}
			api.pushForMessage(*row, db.MessageRow{Kind: "ask", Body: "q"})
			if p := f.pushes.Load(); p != 0 {
				t.Fatalf("an ask from a headless session pushed %d time(s)", p)
			}
		})
	}
}

// A board's Discuss and board sessions (agent-token starts recorded interactive) and its Assist
// sessions are driven by a person: they keep their notices.
func TestIdleNotice_PersonDrivenBoardSessionsNotify(t *testing.T) {
	for _, tc := range []struct{ name, set string }{
		{"board discuss", `started_by_kind = 'agent', started_by_name = 'board', interaction = 'interactive'`},
		{"board assist", `assist = true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNoticeFixture(t)
			if _, err := f.pool.Exec(f.ctx, `UPDATE sessions SET `+tc.set+` WHERE id = $1`, f.sid); err != nil {
				t.Fatal(err)
			}
			f.state("idle")
			f.expect("a person-driven board session's turn ended", 1)
			row, _ := db.GetSession(f.ctx, f.pool, f.sid)
			if headlessSession(row) {
				t.Fatalf("%s read as headless", tc.name)
			}
		})
	}
}

// A person's interactive session keeps its notices; a session becoming ready is not one.
func TestIdleNotice_InteractiveKeepsNotices(t *testing.T) {
	f := newNoticeFixture(t)
	if _, err := f.pool.Exec(f.ctx, `UPDATE sessions SET interaction = 'interactive', status = 'starting' WHERE id = $1`, f.sid); err != nil {
		t.Fatal(err)
	}
	f.state("idle")
	f.expect("a session becoming ready", 0)
	f.state("running")
	f.state("idle")
	f.expect("an interactive session's turn ended", 1)
	row, _ := db.GetSession(f.ctx, f.pool, f.sid)
	if headlessSession(row) {
		t.Fatal("an interactive session started by a person read as headless")
	}
	api := &API{hub: f.hub, dbPool: f.pool}
	api.pushForMessage(*row, db.MessageRow{Kind: "ask", Body: "q"})
	if p := f.pushes.Load(); p != 1 {
		t.Fatalf("an ask from an interactive session pushed %d time(s), want 1", p)
	}
}

// A held notice is looked at again: a background task that never reports its end (a Monitor, a dev
// server) lets it go out at the cap, saying a task still runs — but a running subagent holds it.
func TestIdleNotice_CapForTaskWithNoEnd(t *testing.T) {
	f := newNoticeFixture(t)
	f.ingest("background_task_started", `{"task_id":"mon","call_id":"c-mon","kind":"monitor","description":"watch the log"}`)
	f.notices()
	f.state("idle")
	f.expect("idle while a monitor runs", 0)
	now := time.Now()
	recheckIdleNotices(f.ctx, f.hub, f.pool, now.Add(5*time.Minute))
	f.expect("a recheck before the cap", 0)
	recheckIdleNotices(f.ctx, f.hub, f.pool, now.Add(idleTaskCap+time.Minute))
	got := f.expect("a recheck past the cap", 1)
	if want := "Fix the widget is waiting for you (a background task is still running)."; got[0].Body != want {
		t.Fatalf("capped notice body = %q, want %q", got[0].Body, want)
	}
	recheckIdleNotices(f.ctx, f.hub, f.pool, now.Add(2*idleTaskCap))
	f.expect("after the capped notice went out", 0)

	// A subagent still at work holds the notice past the cap.
	f.state("running")
	f.ingest("subagent_started", `{"child_id":"a2","agent_type":"general-purpose","summary":"dig"}`)
	f.ingest("subagent_done", `{"child_id":"a2","background":true}`)
	f.notices()
	f.state("idle")
	f.expect("idle while a monitor and a subagent run", 0)
	recheckIdleNotices(f.ctx, f.hub, f.pool, time.Now().Add(idleTaskCap+time.Minute))
	f.expect("past the cap with a subagent running", 0)
}

// A subagent that went silent past the live state's stale rule no longer counts as running: the
// next recheck sends the notice, worded as plain waiting.
func TestIdleNotice_StaleSubagentAtRecheck(t *testing.T) {
	f := newNoticeFixture(t)
	f.ingest("subagent_started", `{"child_id":"a1","agent_type":"general-purpose","summary":"look around"}`)
	f.ingest("subagent_done", `{"child_id":"a1","background":true}`)
	f.notices()
	f.state("idle")
	f.expect("idle while a subagent runs", 0)
	recheckIdleNotices(f.ctx, f.hub, f.pool, time.Now().Add(time.Hour))
	f.expect("a recheck while the subagent is fresh", 0)
	recheckIdleNotices(f.ctx, f.hub, f.pool, time.Now().Add(13*time.Hour))
	if got := f.expect("a recheck once the subagent is stale", 1); got[0].Body != "Fix the widget is waiting for you." {
		t.Fatalf("notice = %+v", got[0])
	}
}

// The recheck loop finds an end that came without its usual hook, and stops with its context.
func TestIdleNotice_RecheckLoop(t *testing.T) {
	f := newNoticeFixture(t)
	f.taskStarted("t1")
	f.state("idle")
	f.expect("idle while a task runs", 0)
	// The end is recorded behind the ingest path's back: only the loop can notice it.
	if _, _, err := db.AppendAgentEventAt(f.ctx, f.pool, f.sid, "00000000-0000-4000-8000-0000000000ff",
		"background_task_done", `{"task_id":"t1","status":"completed"}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() {
		RunIdleNoticeChecks(ctx, f.hub, f.pool, 10*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(15 * time.Second)
	for f.pushes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("RunIdleNoticeChecks did not stop with its context")
	}
	f.expect("the loop's recheck", 1)
}

// A session the reconcile paths end drops its held notice.
func TestIdleNotice_ReconcileDropsHold(t *testing.T) {
	f := newNoticeFixture(t)
	f.taskStarted("t1")
	f.state("idle")
	if _, held := f.hub.idleNotices.heldAt(f.sid); !held {
		t.Fatal("no notice held while a task runs")
	}
	now := time.Now()
	setSessionStatus(f.ctx, f.hub, f.pool, f.sid, "error", &now, nil, false)
	if _, held := f.hub.idleNotices.heldAt(f.sid); held {
		t.Fatal("reconcileSessions' status write left the notice held")
	}

	f.hub.idleNotices.hold(f.sid, now)
	api := &API{hub: f.hub, dbPool: f.pool}
	api.broadcastReconciledStatus(f.ctx, f.sid, "error")
	if _, held := f.hub.idleNotices.heldAt(f.sid); held {
		t.Fatal("the cluster reconcile left the notice held")
	}
	f.expect("ended sessions", 0)
}

// headlessSession, without a database: who is watching, by the row.
func TestHeadlessSession(t *testing.T) {
	cron := "00000000-0000-0000-0000-0000000000c9"
	for _, tc := range []struct {
		name string
		row  db.SessionRow
		want bool
	}{
		{"a person's session (no interaction recorded)", db.SessionRow{}, false},
		{"a person's interactive session", db.SessionRow{Interaction: "interactive"}, false},
		{"a person's unattended session", db.SessionRow{Interaction: "unattended"}, true},
		{"a cron's", db.SessionRow{CronID: &cron, StartedByKind: "cron", Interaction: "unattended"}, true},
		{"a cron's, older row", db.SessionRow{CronID: &cron}, true},
		{"the operator key, even interactive", db.SessionRow{StartedByKind: "runner_key", Interaction: "interactive"}, true},
		{"an agent token, unattended", db.SessionRow{StartedByKind: "agent", Interaction: "unattended"}, true},
		{"an agent token, older row", db.SessionRow{StartedByKind: "agent"}, true},
		{"a board Discuss (agent token, interactive)", db.SessionRow{StartedByKind: "agent", StartedByName: "board", Interaction: "interactive"}, false},
	} {
		if got := headlessSession(&tc.row); got != tc.want {
			t.Errorf("%s: headless = %v, want %v", tc.name, got, tc.want)
		}
	}
	if headlessSession(nil) {
		t.Error("a missing row reads as headless")
	}
}

// HandlePostMessages marks an ask from a headless session quiet, and only that.
func TestPostMessage_QuietForHeadlessAsk(t *testing.T) {
	f := newNoticeFixture(t)
	api := NewAPI(f.hub, f.pool, "daemonTok", nil, "")
	post := func(kind string) protocol.MessageCreated {
		t.Helper()
		f.notices() // drain
		req := httptest.NewRequest("POST", "/api/messages", bytes.NewBufferString(
			`{"session_id":"`+f.sid+`","kind":"`+kind+`","body":"which one?"}`))
		req.Header.Set("Authorization", "Bearer daemonTok")
		rec := httptest.NewRecorder()
		api.HandlePostMessages(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("post %s = %d: %s", kind, rec.Code, rec.Body.String())
		}
		for {
			select {
			case raw := <-f.browser.send:
				if strings.Contains(string(raw), `"message_created"`) {
					var m protocol.MessageCreated
					if err := json.Unmarshal(raw, &m); err != nil {
						t.Fatal(err)
					}
					return m
				}
			default:
				t.Fatalf("no message_created for %s", kind)
			}
		}
	}
	if m := post("ask"); m.Quiet {
		t.Fatal("an interactive session's ask is quiet")
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE sessions SET interaction = 'unattended' WHERE id = $1`, f.sid); err != nil {
		t.Fatal(err)
	}
	if m := post("ask"); !m.Quiet {
		t.Fatal("an unattended session's ask is not quiet")
	}
	if m := post("note"); m.Quiet {
		t.Fatal("a note is quiet")
	}
}
